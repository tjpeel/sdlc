package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestLocalNoChangeFeedbackReusesChecksAndReview(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	r.Publisher = &localFeedbackPublisher{feedbackPublisherFake: pub}
	r.LocalPRFeedback = "Confirm the existing behaviour already satisfies this recommendation."
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	entry := pendingFeedback(j)
	if entry == nil || j.PRFeedback.ReviewedCandidate == nil {
		t.Fatal("local feedback lost existing reviewed boundary")
	}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(entry.Comment, "no_change")}}
	r.Provider = provider
	r.LocalPRFeedback = ""
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 || len(provider.sessions) != 1 || pub.replies != 0 || j.State != "ready" {
		t.Fatalf("local no-change feedback repeated work: checks %d turns %d replies %d state %s", checker.calls, len(provider.sessions), pub.replies, j.State)
	}
}

func TestNoChangeFeedbackAfterEquivalentRebaseUsesCIOnlyBoundary(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	candidate := Revision{Head: j.Evidence.Head, Tree: j.Evidence.Tree, Clean: true}
	j.RebaseMonitoring = &RebaseMonitoring{Source: j.Publication, Published: j.Publication, OldBase: testBase, Base: j.Plan.BaseSHA, Proof: RebaseProof{SourceHead: candidate.Head, Head: candidate.Head, Tree: candidate.Tree, Equivalent: true}}
	j.Evidence = CheckEvidence{}
	j.Outcome = Outcome{Status: "implemented"}
	comment := generalFeedback("comment:no-change-after-rebase", "Is this already covered?")
	pub.comments = []PRFeedbackComment{comment}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 || len(provider.sessions) != 1 || pub.replies != 1 || j.State != "ready" || j.Evidence.Passed || j.Outcome.Status == "reviewed" {
		t.Fatalf("CI-only feedback repeated work or invented evidence: checks %d turns %d replies %d state %s", checker.calls, len(provider.sessions), pub.replies, j.State)
	}
}

func TestReadyResumeWaitsForCIWithoutAnotherReview(t *testing.T) {
	dir, j, provider, pub, r := completedReviewFixture(t)
	j.State = "ready"
	j.ResumeState = ""
	j.StopReason = ""
	pub.statuses = []string{"pending", "passed"}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 0 || j.State != "ready" || pub.ci < 2 {
		t.Fatalf("CI-only ready resume repeated review: state %s turns %d CI %d", j.State, len(provider.calls), pub.ci)
	}
}

type checkpointFinalBoundaryFailure struct {
	feedbackScriptProvider
	publisher *localFeedbackPublisher
}

func (p *checkpointFinalBoundaryFailure) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	result, err := p.feedbackScriptProvider.Execute(ctx, s, out, diag)
	p.publisher.boundaryErr = errors.New("temporary PR boundary read failure")
	return result, err
}

func TestCompletedCheckpointReviewRetriesBoundaryWithoutReviewer(t *testing.T) {
	dir, j, r, pub, _, _ := checkpointFixture(t, "review")
	provider := &checkpointFinalBoundaryFailure{publisher: pub, feedbackScriptProvider: feedbackScriptProvider{outcomes: []Outcome{testOutcome("reviewed")}}}
	r.Provider = provider
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(provider.sessions) != 1 || len(j.ReviewReports) != 1 || latestPRCheckpoint(j).ReviewOutcome == nil {
		t.Fatal("completed review not retained")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.StartAt = ""
	pub.boundaryErr = nil
	if err := r.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "awaiting_feedback" || len(provider.sessions) != 1 || len(saved.ReviewReports) != 1 {
		t.Fatal("completed checkpoint review was repeated")
	}
}

func TestReadOnlyFeedbackDiagnosticPreservesReviewedCandidate(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	comment := generalFeedback("comment:diagnose", "Confirm the existing behaviour.")
	pub.comments = []PRFeedbackComment{comment}
	provider := &feedbackScriptProvider{outcomes: []Outcome{diagnosticOutcome("inspect"), assessedOutcome(comment, "no_change")}}
	diagnosticChecker := &diagnosticPhaseChecker{fakeChecker: *checker}
	r.Provider = provider
	r.Checker = diagnosticChecker
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || diagnosticChecker.calls != 1 || len(provider.sessions) != 2 || len(diagnosticChecker.requests) != 1 {
		t.Fatalf("read-only diagnostic repeated gates: state=%s checks=%d turns=%d diagnostics=%d", j.State, diagnosticChecker.calls, len(provider.sessions), len(diagnosticChecker.requests))
	}
}

type checkpointBoundaryChecker struct {
	fakeChecker
	publisher  *localFeedbackPublisher
	cleanupErr error
}

func (c *checkpointBoundaryChecker) Check(ctx context.Context, workspace string, commands [][]string, out io.Writer) error {
	err := c.fakeChecker.Check(ctx, workspace, commands, out)
	c.publisher.boundaryErr = errors.New("temporary boundary failure after check teardown")
	if c.cleanupErr != nil {
		return c.cleanupErr
	}
	return err
}

func TestCompletedCheckpointChecksRetryBoundaryWithoutRerunningCommands(t *testing.T) {
	dir, j, r, pub, provider, _ := checkpointFixture(t, "checks")
	checker := &checkpointBoundaryChecker{publisher: pub}
	r.Checker = checker
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if checker.calls != 1 || j.Evidence.Passed || latestPRCheckpoint(j).PendingCheckKey == "" {
		t.Fatal("successful check not retained provisionally")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.StartAt = ""
	pub.boundaryErr = nil
	if err := r.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "awaiting_feedback" || checker.calls != 1 || !saved.Evidence.Passed || len(provider.sessions) != 0 {
		t.Fatal("completed checkpoint commands repeated")
	}
}

func TestCheckpointChecksDoNotReuseFailedCleanupOrChangedCommands(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			dir, j, r, pub, _, _ := checkpointFixture(t, "checks")
			checker := &checkpointBoundaryChecker{publisher: pub}
			r.Checker = checker
			if !changed {
				checker.cleanupErr = errors.New("cleanup failed")
			}
			_ = r.Run(context.Background(), dir, j, "")
			if !changed && latestPRCheckpoint(j).PendingCheckKey != "" {
				t.Fatal("failed cleanup retained passing proof")
			}
			r.StartAt = ""
			pub.boundaryErr = nil
			checker.cleanupErr = nil
			if changed {
				j.Plan.Checks = [][]string{{"go", "vet", "./..."}}
			}
			_ = r.Run(context.Background(), dir, j, "")
			if checker.calls != 2 || j.Evidence.Passed {
				t.Fatal("failed cleanup or changed commands reused provisional evidence")
			}
		})
	}
}

func TestNoChangeAfterRebaseDoesNotBypassRequiredVerification(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	candidate := Revision{Head: j.Evidence.Head, Tree: j.Evidence.Tree, Clean: true}
	j.RebaseMonitoring = &RebaseMonitoring{Source: j.Publication, Published: j.Publication, OldBase: testBase, Base: j.Plan.BaseSHA, Proof: RebaseProof{SourceHead: candidate.Head, Head: candidate.Head, Tree: candidate.Tree, Equivalent: true}}
	j.Evidence = CheckEvidence{}
	j.Outcome = Outcome{Status: "implemented"}
	comment := generalFeedback("comment:required-after-rebase", "Verify the existing result.")
	pub.comments = []PRFeedbackComment{comment}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.Outcome = assessedOutcome(comment, "no_change")
	j.Verification = []VerificationResult{{Request: diagnosticRequest("required")}}
	handled, err := r.finishUnchangedFeedback(context.Background(), dir, j)
	if err != nil || handled || j.State == "ready" {
		t.Fatal("unexecuted required verification bypassed", handled, err)
	}
}
