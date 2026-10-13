package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runprogress"
)

// formatterPreparationDiscovery supplies a discovery seam for offline repositories.
// Production repositories use committed metadata through SafeGit.
type formatterPreparationDiscovery interface {
	FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error)
}

// preparationRequests discovers only controller-supported policies from the
// committed tree. The captured source boundary, rather than check selection,
// defines the files eligible for correction.
func preparationRequests(ctx context.Context, journal *Journal, revision Revision) ([]VerificationRequest, error) {
	listing, err := SafeGit(ctx, journal.Workspace, "ls-tree", "-z", revision.Head, "--", ".config/dotnet-tools.json")
	if err != nil {
		return nil, err
	}
	if listing == "" {
		return nil, nil
	}
	if !strings.HasPrefix(listing, "100644 blob ") && !strings.HasPrefix(listing, "100755 blob ") {
		return nil, errors.New("formatter manifest must be a committed regular file")
	}
	sizeText, err := SafeGit(ctx, journal.Workspace, "cat-file", "-s", revision.Head+":.config/dotnet-tools.json")
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil || size <= 0 || size > 64*1024 {
		return nil, errors.New("formatter metadata exceeds discovery bound")
	}
	manifest, err := SafeGit(ctx, journal.Workspace, "show", revision.Head+":.config/dotnet-tools.json")
	if err != nil {
		return nil, err
	}
	if len(manifest) > 64*1024 {
		return nil, errors.New("formatter metadata exceeds discovery bound")
	}
	formatters, err := project.DiscoverFormatters([]byte(manifest))
	if err != nil || len(formatters) == 0 {
		return nil, err
	}
	changed, err := SafeGit(ctx, journal.Workspace, "diff", "--name-only", "-z", "--diff-filter=AM", "--no-renames", journal.Plan.SourceSHA, revision.Head, "--")
	if err != nil {
		return nil, err
	}
	var requests []VerificationRequest
	for _, formatter := range formatters {
		formatter, err = project.FormatterForChecks(formatter, effectiveChecks(journal))
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, path := range strings.Split(changed, "\x00") {
			if path == "" || !slices.Contains(formatter.Extensions, filepath.Ext(path)) {
				continue
			}
			request := VerificationRequest{SourcePatch: &SourcePatchRequest{Paths: []string{path}}}
			if err := validateSourcePatchRequest(request, &journal.Plan); err != nil {
				return nil, err
			}
			entry, err := SafeGit(ctx, journal.Workspace, "ls-tree", "-z", revision.Head, "--", ":(literal)"+path)
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(entry, "100644 blob ") || strings.HasPrefix(entry, "100755 blob ") {
				paths = append(paths, path)
			}
		}
		for len(paths) > 0 {
			count := min(16, len(paths))
			selected := append([]string(nil), paths[:count]...)
			commands := append([][]string(nil), formatter.Setup...)
			arguments := make([]string, len(selected))
			for i, path := range selected {
				arguments[i] = "./" + path
			}
			commands = append(commands, append(append([]string(nil), formatter.Command...), arguments...))
			requests = append(requests, VerificationRequest{ID: fmt.Sprintf("formatter-%s-%d", formatter.Name, len(requests)+1), Purpose: "Controller formatter preparation", Commands: commands, SourcePatch: &SourcePatchRequest{Paths: selected}})
			paths = paths[count:]
		}
	}
	if len(requests) > 16 {
		return nil, errors.New("formatter source selection exceeds preparation bound")
	}
	return requests, validateVerificationRequests(requests, &journal.Plan)
}

func (runner Runner) prepareCandidate(ctx context.Context, directory string, journal *Journal, revision Revision) (bool, error) {
	previous := journal.FormatterPreparation
	for i := range journal.FormatterPreparation {
		evidence := &journal.FormatterPreparation[i]
		if evidence.Head != revision.Head || evidence.Tree != revision.Tree {
			discardSourcePatchFeedback(journal, evidence)
		}
	}
	var requests []VerificationRequest
	var err error
	if discovery, ok := runner.Repository.(formatterPreparationDiscovery); ok {
		requests, err = discovery.FormatterPreparation(ctx, journal, revision)
	} else {
		requests, err = preparationRequests(ctx, journal, revision)
	}
	if err != nil {
		invalidateCommandEvidence(journal)
		return false, runner.stop(directory, journal, "blocked", "formatter preparation discovery failed: "+err.Error())
	}
	var started time.Time
	defer func() {
		if !started.IsZero() {
			journal.Timings.ChecksMS += time.Since(started).Milliseconds()
		}
	}()
	journal.FormatterPreparation = nil
	corrections := false
	for _, request := range requests {
		key := verificationKey(journal, request, revision.Head, revision.Tree)
		var evidence VerificationResult
		for _, prior := range previous {
			if prior.Key == key {
				evidence = prior
				break
			}
		}
		if evidence.Key == "" {
			if started.IsZero() {
				started = time.Now()
			}
			checker, ok := runner.Checker.(SourcePatchChecker)
			if !ok {
				return false, runner.stop(directory, journal, "blocked", "selected check runtime does not support formatter preparation")
			}
			journal.CheckAttempt++
			if err := runner.save(directory, journal); err != nil {
				invalidateCommandEvidence(journal)
				return false, err
			}
			name := fmt.Sprintf("preparation-%d.log", journal.CheckAttempt)
			log, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return false, err
			}
			stream := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: "formatter_preparation", Attempt: journal.CheckAttempt}, false)
			output := newCommandOutputCollector(io.MultiWriter(log, runner.rawOutput, stream))
			patch, checkErr := checker.VerifySourcePatch(ctx, journal.Workspace, request, revision.Head, revision.Tree, output)
			closeErr := errors.Join(stream.Finish(), log.Close())
			semanticFailure := errors.Is(checkErr, ErrCheckFailed) && closeErr == nil
			checkErr = errors.Join(checkErr, closeErr)
			if checkErr == nil {
				checkErr = validateSourcePatchResult(patch, request, revision.Head, revision.Tree)
			}
			if checkErr == nil {
				current, inspectErr := runner.Repository.Inspect(ctx, journal.Workspace)
				if inspectErr != nil || !current.Clean || current.Head != revision.Head || current.Tree != revision.Tree {
					checkErr = errors.New("formatter candidate changed before feedback")
				}
			}
			if checkErr != nil && !semanticFailure {
				invalidateCommandEvidence(journal)
				return false, runner.stop(directory, journal, "blocked", "formatter preparation could not complete; inspect its private log")
			}
			evidence = VerificationResult{Request: request, Key: key, Head: revision.Head, Tree: revision.Tree, Log: name, Output: output.results()}
			if semanticFailure {
				evidence.Diagnostic = "Formatter preparation commands failed. Inspect bounded command evidence, repair the committed source or formatting metadata, and request verification again."
				var budget *sourcePatchDiffBudgetError
				if errors.As(checkErr, &budget) {
					evidence.Diagnostic = "Formatter correction exceeds the 32 KiB artifact bound. No source correction was retained. Request smaller source_patch selections to obtain bounded corrections, then review and commit the intended repair before requesting preparation again."
				}
			} else {
				evidence.Passed = patch.Diff == ""
				evidence.SourcePatch = patch
			}
		}
		journal.FormatterPreparation = append(journal.FormatterPreparation, evidence)
		corrections = corrections || !evidence.Passed
		boundJournalOutput(journal)
		if err := validateSourcePatchJournal(journal); err != nil {
			invalidateCommandEvidence(journal)
			return false, runner.stop(directory, journal, "blocked", "formatter artifacts exceed their validation bound")
		}
	}
	boundJournalOutput(journal)
	if err := validateSourcePatchJournal(journal); err != nil {
		invalidateCommandEvidence(journal)
		return false, runner.stop(directory, journal, "blocked", "formatter artifacts exceed their validation bound")
	}
	if err := runner.save(directory, journal); err != nil {
		invalidateCommandEvidence(journal)
		return false, err
	}
	if corrections {
		journal.Evidence.Passed = false
		journal.Rounds++
		if journal.Rounds-journal.RepairWindowStart > runner.MaxRounds {
			return false, runner.stop(directory, journal, "blocked", configuredCheckRepairBound)
		}
		preparationFeedback := "Formatter preparation requires committed repair. Review the private source_patch artifacts, apply intended corrections in the implementation workspace, and commit them before requesting the full configured suite."
		journal.Feedback = preparationFeedback + "\n" + verificationFeedback(journal)
		pending := false
		for _, evidence := range journal.Verification {
			pending = pending || evidence.Key != verificationKey(journal, evidence.Request, revision.Head, revision.Tree)
		}
		if pending {
			if started.IsZero() {
				started = time.Now()
			}
			return false, runner.preparationDiagnostics(ctx, directory, journal, revision, preparationFeedback)
		}
		return false, runner.transition(directory, journal, "repairing")
	}
	return true, nil
}

func clearFormatterPreparation(journal *Journal) {
	for i := range journal.FormatterPreparation {
		discardSourcePatchFeedback(journal, &journal.FormatterPreparation[i])
	}
	journal.FormatterPreparation = nil
}

// Failed preparation does not prevent newly requested machine diagnostics. Its
// failure remains outstanding and the configured suite is still skipped.
func (runner Runner) preparationDiagnostics(ctx context.Context, directory string, journal *Journal, revision Revision, feedback string) error {
	phase, supported := runner.Checker.(CandidatePhaseChecker)
	if !supported {
		return runner.checkAdditionalCandidate(ctx, directory, journal, revision, feedback)
	}
	journal.CandidatePhasePending = true
	if err := runner.save(directory, journal); err != nil {
		return err
	}
	log, err := os.CreateTemp(directory, "preparation-diagnostics-*.log")
	if err != nil {
		return err
	}
	stream := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: "formatter_preparation", Attempt: journal.CheckAttempt}, false)
	var callbackErr error
	err = phase.WithCandidate(ctx, journal.Workspace, io.MultiWriter(log, runner.rawOutput, stream), func(checker Checker) error {
		scoped := runner
		scoped.Checker = checker
		callbackErr = scoped.checkAdditionalCandidate(ctx, directory, journal, revision, feedback)
		return callbackErr
	})
	closeErr := errors.Join(stream.Finish(), log.Close())
	if closeErr != nil || err != callbackErr {
		invalidateCommandEvidence(journal)
		journal.State = "checking"
		return runner.stop(directory, journal, "blocked", errors.Join(err, closeErr).Error())
	}
	journal.CandidatePhasePending = false
	if saveErr := runner.save(directory, journal); saveErr != nil {
		invalidateCommandEvidence(journal)
		return errors.Join(err, saveErr)
	}
	return err
}
