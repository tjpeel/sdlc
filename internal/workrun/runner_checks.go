package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/runprogress"
)

const configuredCheckRepairBound = "repository checks still fail after the repair bound"

func (runner Runner) checkCandidatePhase(ctx context.Context, directory string, journal *Journal, revision Revision) error {
	firstAttempt := journal.CheckAttempt + 1
	if timed, ok := runner.Checker.(TimingChecker); ok {
		runner.Checker = timed.WithTimingObserver(func(observation CheckTiming) {
			observation.Attempt = journal.CheckAttempt
			if observation.Attempt < firstAttempt {
				observation.Attempt = firstAttempt
			}
			journal.Timings.addCheckTiming(observation)
		})
	}

	if journal.CandidatePhasePending {
		// A previous controller did not checkpoint successful phase cleanup.
		// Its evidence is provisional even if every command completed.
		journal.Evidence.Passed = false
		for i := range journal.Verification {
			journal.Verification[i].Key = ""
		}
	}
	phase, supported := runner.Checker.(CandidatePhaseChecker)
	pending := journal.Evidence.Tree != revision.Tree || journal.Evidence.Head != revision.Head || !journal.Evidence.Passed
	for _, evidence := range journal.Verification {
		if evidence.Key != verificationKey(journal, evidence.Request, revision.Head, revision.Tree) {
			pending = true
		}
	}
	if pending {
		phaseStarted := time.Now()
		defer func() { journal.Timings.ChecksMS += time.Since(phaseStarted).Milliseconds() }()
	}
	if !supported || !pending {
		if err := runner.checkCandidate(ctx, directory, journal, revision); err != nil {
			return err
		}
		if journal.State == "checking" {
			return runner.transition(directory, journal, "publishing")
		}
		return nil
	}
	journal.CandidatePhasePending = true
	if err := runner.save(directory, journal); err != nil {
		return err
	}
	log, err := os.CreateTemp(directory, fmt.Sprintf("candidate-phase-%d-*.log", journal.CheckAttempt+1))
	if err != nil {
		return err
	}
	stream := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: journal.State, Attempt: journal.CheckAttempt + 1}, false)
	var callbackErr error
	err = phase.WithCandidate(ctx, journal.Workspace, io.MultiWriter(log, runner.rawOutput, stream), func(checker Checker) error {
		scoped := runner
		scoped.Checker = checker
		callbackErr = scoped.checkCandidate(ctx, directory, journal, revision)
		return callbackErr
	})
	closeErr := errors.Join(stream.Finish(), log.Close())
	if closeErr != nil || err != callbackErr {
		// Cleanup/setup failure must be resolved before publication, including on
		// resume. It cannot leave the candidate's successful evidence reusable.
		journal.Evidence.Passed = false
		for i := range journal.Verification {
			journal.Verification[i].Key = ""
		}
		journal.State = "checking"
		return runner.stop(directory, journal, "blocked", errors.Join(err, closeErr).Error())
	}
	journal.CandidatePhasePending = false
	if saveErr := runner.save(directory, journal); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	if err == nil && journal.State == "checking" {
		return runner.transition(directory, journal, "publishing")
	}
	return err
}

func (runner Runner) checkCandidate(ctx context.Context, directory string, journal *Journal, revision Revision) error {
	configuredFeedback := ""
	if journal.Evidence.Tree != revision.Tree || journal.Evidence.Head != revision.Head || !journal.Evidence.Passed {
		journal.CheckAttempt++
		if err := runner.save(directory, journal); err != nil {
			return err
		}
		path := filepath.Join(directory, fmt.Sprintf("checks-%d.log", journal.CheckAttempt))
		log, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		checkOutput := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: journal.State, Attempt: journal.CheckAttempt}, false)
		checkErr := runner.Checker.Check(ctx, journal.Workspace, journal.Plan.Checks, io.MultiWriter(log, runner.rawOutput, checkOutput))
		progressErr := checkOutput.Finish()
		if progressErr != nil {
			log.Close()
			return errors.Join(checkErr, progressErr)
		}
		closeErr := log.Close()
		journal.Evidence = CheckEvidence{Head: revision.Head, Tree: revision.Tree, Passed: checkErr == nil, Commands: journal.Plan.Checks, Log: filepath.Base(path)}
		if closeErr != nil {
			return runner.stop(directory, journal, "blocked", "cannot retain verification evidence")
		}
		if checkErr != nil && !errors.Is(checkErr, ErrCheckFailed) {
			return runner.stop(directory, journal, "blocked", checkErr.Error())
		}
		if checkErr != nil {
			journal.Rounds++
			configuredFeedback = checkRepairFeedback(revision.Tree, checkErr, journal.Plan.Checks)
			journal.Feedback = configuredFeedback
			if journal.Rounds-journal.RepairWindowStart > runner.MaxRounds {
				return runner.stop(directory, journal, "blocked", configuredCheckRepairBound)
			}
		}
	}
	repaired := false
	for i := range journal.Verification {
		evidence := &journal.Verification[i]
		key := verificationKey(journal, evidence.Request, revision.Head, revision.Tree)
		if evidence.Key == key && evidence.Passed {
			continue
		}
		if evidence.Key == key && !evidence.Passed {
			return runner.stop(directory, journal, "blocked", "additional verification "+evidence.Request.ID+" still fails on the unchanged candidate; change the candidate or request before resuming")
		}
		checker, ok := runner.Checker.(VerificationChecker)
		if !ok {
			return runner.stop(directory, journal, "blocked", "selected check runtime does not support additional verification")
		}
		journal.CheckAttempt++
		if err := runner.save(directory, journal); err != nil {
			return err
		}
		name := fmt.Sprintf("verification-%s-%d.log", evidence.Request.ID, journal.CheckAttempt)
		log, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		checkOutput := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: journal.State, Attempt: journal.CheckAttempt}, false)
		checkErr := checker.Verify(ctx, journal.Workspace, evidence.Request, io.MultiWriter(log, runner.rawOutput, checkOutput))
		progressErr := checkOutput.Finish()
		var diagnosticLogErr error
		if checkErr != nil {
			_, diagnosticLogErr = fmt.Fprintf(log, "\nAdditional verification failure: %v\n", checkErr)
		}
		closeErr := errors.Join(diagnosticLogErr, log.Close())
		if progressErr != nil {
			return errors.Join(checkErr, progressErr, closeErr)
		}
		evidence.Key, evidence.Head, evidence.Tree, evidence.Log, evidence.Passed = key, revision.Head, revision.Tree, name, checkErr == nil && closeErr == nil
		// Operational failures do not establish semantic failure evidence;
		// allow the unchanged candidate to retry after infrastructure recovery.
		if closeErr != nil || (checkErr != nil && !errors.Is(checkErr, ErrCheckFailed)) {
			evidence.Key = ""
		}
		evidence.Diagnostic = ""
		if closeErr != nil {
			return runner.stop(directory, journal, "blocked", "cannot retain additional verification evidence")
		}
		if checkErr != nil {
			// Detailed process output stays in the private log. Repair feedback is bounded controller metadata.
			evidence.Diagnostic = verificationDiagnostic(evidence.Request, revision.Tree, checkErr)
			if !errors.Is(checkErr, ErrCheckFailed) {
				return runner.stop(directory, journal, "blocked", "additional verification "+evidence.Request.ID+" could not complete; inspect its private log")
			}
			journal.Feedback = verificationFeedback(journal)
			if configuredFeedback != "" {
				journal.Feedback = configuredFeedback + "\n" + journal.Feedback
			}
			if err := runner.transition(directory, journal, "repairing"); err != nil {
				return err
			}
			repaired = true
			break
		}
	}
	if repaired {
		return nil
	}
	if configuredFeedback != "" {
		journal.Feedback = configuredFeedback
		if len(journal.Verification) > 0 {
			journal.Feedback += "\n" + verificationFeedback(journal)
		}
		return runner.transition(directory, journal, "repairing")
	}
	if journal.Outcome.Status == "checks_requested" {
		key := verificationFeedbackKey(journal, revision.Head, revision.Tree)
		if journal.VerificationFeedbackKey == key {
			journal.VerificationFeedbackCount++
		} else {
			journal.VerificationFeedbackKey = key
			journal.VerificationFeedbackCount = 1
		}
		if journal.VerificationFeedbackCount > 2 {
			return runner.stop(directory, journal, "blocked", "verification evidence was already supplied for this unchanged candidate; submit a new structured verification request or complete local review before resuming")
		}
		journal.Feedback = verificationFeedback(journal) + ". Submit verification_requests for additional machine work, or complete local review and PR metadata using tjpeel-pr-draft. Use waiting_for_human only for genuine human questions."
		if err := runner.transition(directory, journal, "implementing"); err != nil {
			return err
		}
		return nil
	}
	if revision.Head == journal.Plan.StartingSHA {
		return runner.stop(directory, journal, "blocked", "ticket produced no implementation commit")
	}
	if err := ValidatePRMetadata(journal.Plan); err != nil {
		journal.PRMetadataCorrections++
		if journal.PRMetadataCorrections > 2 {
			return runner.stop(directory, journal, "blocked", "PR metadata still violates the frozen publication contract: "+err.Error())
		}
		journal.Feedback = "Correct only the PR metadata: " + err.Error() + ". Retain the unchanged committed source and passing controller evidence. Return implemented with corrected pr_title/pr_body and retained local review.\n" + prMetadataInstructions(journal.Plan)
		if err := runner.transition(directory, journal, "implementing"); err != nil {
			return err
		}
		return nil
	}
	return nil
}
