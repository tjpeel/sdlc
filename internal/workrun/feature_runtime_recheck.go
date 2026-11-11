package workrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func validateRuntimeRecheck(j *Journal) error {
	c := j.RuntimeRecheck
	if c == nil {
		return nil
	}
	if c.HoldReason == "" || c.Publication.Number < 1 || c.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, c.Publication.Number) || !objectID.MatchString(c.Publication.BaseSHA) || !objectID.MatchString(c.Publication.HeadSHA) || !objectID.MatchString(c.CandidateHead) || !objectID.MatchString(c.CandidateTree) {
		return fmt.Errorf("invalid feature runtime recheck checkpoint")
	}
	if c.ImplementationAttempt < 0 || c.ImplementationAttempt > j.Attempt || c.ImplementationAttempt > 0 && (len(j.RuntimeMigrations) != 1 || c.ImplementationAttempt <= j.RuntimeMigrations[0].PreviousAttempt) {
		return fmt.Errorf("invalid migrated implementation attempt")
	}
	if c.Pending && c.Publication != j.Publication {
		return fmt.Errorf("feature runtime recheck PR boundary changed")
	}
	if c.Pending && c.AbandonedAt != nil {
		return fmt.Errorf("abandoned runtime recheck cannot remain pending")
	}
	if c.Pending && j.State == "ready" {
		return fmt.Errorf("pending runtime recheck cannot be ready")
	}
	return nil
}

func (r Runner) runtimeRecheckBoundary(ctx context.Context, j *Journal) (Revision, error) {
	c := j.RuntimeRecheck
	if c == nil || !c.Pending || c.Publication != j.Publication {
		return Revision{}, fmt.Errorf("runtime recheck checkpoint changed")
	}
	revision, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil || !revision.Clean || revision.Head != c.CandidateHead || revision.Tree != c.CandidateTree {
		return Revision{}, fmt.Errorf("candidate changed; runtime recheck did not publish the PR")
	}
	p, ok := r.Publisher.(interface {
		ValidateFeedbackBoundary(context.Context, Plan, Publication) error
	})
	if !ok {
		return Revision{}, fmt.Errorf("publisher cannot verify runtime recheck PR boundary")
	}
	if err := p.ValidateFeedbackBoundary(ctx, j.Plan, c.Publication); err != nil {
		return Revision{}, fmt.Errorf("runtime recheck PR boundary: %w", err)
	}
	return revision, nil
}

// recheckFeatureRuntime only runs isolated verification. It never publishes,
// changes the candidate or starts an implementation/review session.
func (r Runner) recheckFeatureRuntime(ctx context.Context, dir string, j *Journal) error {
	revision, err := r.runtimeRecheckBoundary(ctx, j)
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if err := resolveCandidateChecks(ctx, j, revision); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if len(effectiveChecks(j)) == 0 {
		return r.stop(dir, j, "blocked", "runtime recheck has no configured checks; the PR remains unchanged")
	}
	ready, err := r.prepareCandidate(ctx, dir, j, revision)
	if err != nil || !ready {
		return err
	}
	j.CheckAttempt++
	if err := r.save(dir, j); err != nil {
		return err
	}
	name := fmt.Sprintf("runtime-recheck-%d.log", j.CheckAttempt)
	log, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	j.Evidence.Log = name
	capture := newCommandOutputCollector(io.MultiWriter(log, r.rawOutput))
	checkErr := r.Checker.Check(ctx, j.Workspace, effectiveChecks(j), capture)
	closeErr := log.Close()
	if checkErr != nil || closeErr != nil {
		j.Evidence.Passed = false
		return r.stop(dir, j, "blocked", "runtime recheck failed; inspect "+name+"; the published PR was not changed")
	}
	after, err := r.runtimeRecheckBoundary(ctx, j)
	if err != nil {
		j.Evidence.Passed = false
		return r.stop(dir, j, "blocked", fmt.Sprintf("runtime recheck could not confirm the candidate and PR: %v; the published PR was not changed", err))
	}
	if after.Head != revision.Head || after.Tree != revision.Tree {
		j.Evidence.Passed = false
		return r.stop(dir, j, "blocked", "candidate or PR changed during runtime recheck; the published PR was not changed")
	}
	j.Evidence = CheckEvidence{Head: revision.Head, Tree: revision.Tree, Key: verificationKey(j, VerificationRequest{}, revision.Head, revision.Tree), Commands: effectiveChecks(j), Log: name, Output: capture.results(), Passed: true}
	boundJournalOutput(j)
	j.RuntimeRecheck.Pending = false
	reason := j.RuntimeRecheck.HoldReason
	j.State = "ci"
	return r.stop(dir, j, "blocked", reason)
}

// ValidateAbandonRuntimeRecheck requires a stopped recheck and never grants
// readiness. Existing logs and formatter artifacts remain in the journal.
func ValidateAbandonRuntimeRecheck(j Journal) error {
	c := j.RuntimeRecheck
	feedback := PRFeedbackStatus(j)
	if ciHeldReviewOutcomeHasWork(j.Outcome) || feedback.Pending > 0 || feedback.Unresolved > 0 || feedback.OutstandingReplies > 0 {
		return fmt.Errorf("abandon cannot bypass saved outcome or feedback work")
	}
	if c == nil || !c.Pending || j.State != "blocked" || j.ResumeState != "runtime_recheck" || j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingRole != "" || pendingCIFeedback(&j) != nil || j.PRFeedback != nil && j.PRFeedback.Active || j.PushRepair != nil && j.PushRepair.Pending != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay {
		return fmt.Errorf("abandon requires a stopped runtime recheck without another pending recovery")
	}
	return validateRuntimeRecheck(&j)
}

func (r Runner) abandonRuntimeRecheck(dir string, j *Journal) error {
	if err := ValidateAbandonRuntimeRecheck(*j); err != nil {
		return err
	}
	now := time.Now().UTC()
	j.RuntimeRecheck.Pending = false
	j.RuntimeRecheck.AbandonedAt = &now
	j.Evidence.Passed = false
	j.CI = CIResult{}
	j.State, j.ResumeState, j.StopReason = "blocked", "implementing", "Runtime recheck was explicitly abandoned; normal checks and independent review remain required"
	j.Feedback = "The operator explicitly stopped the unchanged-PR runtime recheck. Inspect the retained check log and formatter artifacts. Continue only the selected ticket using normal verification, signing and publication gates; do not treat previous runtime checks, CI or review as current."
	if err := r.save(dir, j); err != nil {
		return err
	}
	if r.Output != nil {
		fmt.Fprintln(r.Output, "Runtime recheck abandoned; no PR was published and checks, CI and independent review remain required.")
	}
	return nil
}
