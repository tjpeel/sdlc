package workrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type ciHeldPublisher struct {
	*fakePublisher
	boundaries int
	failAt     int
}

func (p *ciHeldPublisher) ValidateFeedbackBoundary(context.Context, Plan, Publication) error {
	p.boundaries++
	if p.failAt == p.boundaries {
		return fmt.Errorf("PR revision changed")
	}
	return nil
}

func ciHeldReviewFixture(t *testing.T) (string, *Journal, *fakeProvider, *ciHeldPublisher, Runner) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "ci", "CI unavailable"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	j.SessionID = testNative
	j.Outcome = testOutcome("implemented")
	j.Outcome.Summary = "saved implementation handoff"
	j.CI.Status = "pending"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	pub := &ciHeldPublisher{fakePublisher: &fakePublisher{}}
	runner := fakeRunner(provider, &fakeChecker{}, pub.fakePublisher, &fakeRepository{})
	runner.Publisher = pub
	runner.ReviewCIHold = true
	return dir, j, provider, pub, runner
}

func TestCIHeldReviewPersistsThenResumesCI(t *testing.T) {
	dir, j, provider, pub, runner := ciHeldReviewFixture(t)
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ci" || j.StopReason != "CI unavailable" {
		t.Fatalf("hold not retained: %v %+v", err, j)
	}
	if pub.ci != 0 || len(provider.calls) != 1 || len(j.ReviewReports) != 1 || j.CIHeldReview == nil || j.CIHeldReview.Pending {
		t.Fatalf("review/CI evidence incorrect: calls=%d reviews=%d CI=%d", len(provider.calls), len(j.ReviewReports), pub.ci)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CIHeldReview.PriorOutcome == nil || saved.CIHeldReview.PriorOutcome.Summary != "saved implementation handoff" {
		t.Fatal("original outcome not retained")
	}
	runner.ReviewCIHold = false
	if err := runner.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(provider.calls) != 1 || pub.ci < 1 {
		t.Fatalf("resume failed: state=%s turns=%d CI=%d", saved.State, len(provider.calls), pub.ci)
	}
}

func TestCIHeldReviewRejectsChangedRemoteBoundary(t *testing.T) {
	for _, failAt := range []int{1, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			dir, j, _, pub, runner := ciHeldReviewFixture(t)
			pub.failAt = failAt
			err := runner.Run(context.Background(), dir, j, "")
			if err == nil || !strings.Contains(err.Error(), "PR revision changed") {
				t.Fatalf("changed boundary accepted: %v", err)
			}
			if len(j.ReviewReports) != 0 || j.State == "ready" {
				t.Fatalf("stale review accepted: %+v", j)
			}
		})
	}
}

func TestCIHeldReviewCheckpointRejectsChangedPublication(t *testing.T) {
	dir, j, _, _, _ := ciHeldReviewFixture(t)
	j.CIHeldReview = &CIHeldReview{Publication: j.Publication, HoldReason: j.StopReason, StartedAt: j.UpdatedAt, Pending: true}
	j.Publication.HeadSHA = testTree
	if err := Save(dir, j); err == nil {
		t.Fatal("changed publication accepted")
	}
}

func TestCIHeldReviewBoundaryFailureCanBeDiscardedWithoutCompletingCI(t *testing.T) {
	dir, j, _, pub, runner := ciHeldReviewFixture(t)
	pub.failAt = 3
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected changed boundary")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CIHeldReview == nil || !saved.CIHeldReview.Pending || !saved.CIHeldReview.BoundaryFailed || saved.CIHeldReview.InterruptedOutcome == nil {
		t.Fatal("review evidence or boundary error not retained")
	}
	runner.ReviewCIHold = false
	runner.DiscardCIHeldReview = true
	if err := runner.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "blocked" || saved.ResumeState != "ci" || saved.CIHeldReview.Pending || !saved.CIHeldReview.Invalidated || saved.CI.Status != "pending" || saved.CIHeldReview.InterruptedOutcome == nil {
		t.Fatalf("discard changed CI or lost evidence: %+v", saved.CIHeldReview)
	}
	saved.Publication.HeadSHA = testTree
	if err := Save(dir, &saved); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCIHeldReviewStart(saved); err != nil {
		t.Fatalf("reconciled revision cannot start fresh review: %v", err)
	}
	runner.DiscardCIHeldReview = false
	pub.failAt = 0
	if err := runner.beginCIHeldReview(context.Background(), dir, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.CIHeldReviewHistory) != 1 || !saved.CIHeldReviewHistory[0].Invalidated || saved.CIHeldReviewHistory[0].InterruptedOutcome == nil || saved.CIHeldReviewHistory[0].PriorOutcome == nil || saved.CIHeldReviewHistory[0].PriorOutcome.Summary != "saved implementation handoff" {
		t.Fatal("previous review evidence not retained")
	}
}

func TestCIHeldReviewCannotDiscardKnownFindings(t *testing.T) {
	dir, j, _, pub, runner := ciHeldReviewFixture(t)
	pub.failAt = 3
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected boundary error")
	}
	j.CIHeldReview.InterruptedOutcome.Findings = []Finding{{Priority: "P1", Path: "a.go", Line: 1, Scenario: "Failure", Recommendation: "Fix it"}}
	if err := ValidateDiscardCIHeldReview(*j); err == nil {
		t.Fatal("known findings discarded")
	}
}

func TestCIHeldReviewRejectsOutstandingFeedback(t *testing.T) {
	_, j, _, _, _ := ciHeldReviewFixture(t)
	comment := generalFeedback("comment:1", "Please change the behaviour")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, CompletedHead: testHead, Entries: []PRFeedbackEntry{{Comment: comment, Fingerprint: feedbackFingerprint(comment)}}}
	if err := ValidateCIHeldReviewStart(*j); err == nil {
		t.Fatal("pending PR feedback bypassed")
	}
}

func TestCIHeldReviewSurvivesReviewerFailureAndRestart(t *testing.T) {
	dir, j, provider, _, runner := ciHeldReviewFixture(t)
	provider.executeErr = fmt.Errorf("temporary reviewer failure")
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("reviewer error ignored")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CIHeldReview == nil || !saved.CIHeldReview.Pending || saved.State == "ready" {
		t.Fatal("review checkpoint not retained")
	}
	provider.executeErr = nil
	runner.ReviewCIHold = false
	if err := runner.Run(context.Background(), dir, &saved, ""); !errors.Is(err, ErrStopped) || saved.ResumeState != "ci" || len(saved.ReviewReports) != 1 {
		t.Fatalf("review did not resume to CI hold: %v", err)
	}
}

func TestCIHeldReviewRetainsFindingsWhileCIBlocked(t *testing.T) {
	dir, j, provider, pub, runner := ciHeldReviewFixture(t)
	outcome := testOutcome("reviewed")
	outcome.Findings = []Finding{{Priority: "P1", Path: "a.go", Line: 1, Scenario: "Failure", Recommendation: "Fix it"}}
	provider.outcomes = []Outcome{outcome}
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || j.ResumeState != "ci" || j.StopReason != "CI unavailable" || pub.ci != 0 || len(j.ReviewReports) != 1 || len(j.ReviewReports[0].Findings) != 1 {
		t.Fatalf("finding or CI hold lost: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ciHeldReviewFindingsAwaitingCI(&saved) {
		t.Fatal("saved finding is not eligible for assessment after CI")
	}
	if saved.CIHeldReview.PriorOutcome == nil || saved.CIHeldReview.PriorOutcome.Summary != "saved implementation handoff" {
		t.Fatal("original outcome lost after findings")
	}
	original := saved.Outcome.Findings[0].Scenario
	saved.Outcome.Findings[0].Scenario = "different finding"
	if ciHeldReviewFindingsAwaitingCI(&saved) {
		t.Fatal("changed findings accepted as recorded review")
	}
	saved.Outcome.Findings[0].Scenario = original
	saved.Publication.HeadSHA = testTree
	if ciHeldReviewFindingsAwaitingCI(&saved) {
		t.Fatal("stale finding applied to changed publication")
	}
}

func TestCIHeldReviewCannotOverwriteSavedOutcomeWork(t *testing.T) {
	outcomes := []Outcome{
		{Findings: []Finding{{Scenario: "known finding"}}},
		{Limitations: []string{"unfinished verification"}},
		{RepositoryRequests: []CompanionRepositoryRequest{{}}},
		{VerificationRequests: []VerificationRequest{{}}},
		{DiagnosticRequests: []VerificationRequest{{}}},
		{PRFeedbackAssessments: []PRFeedbackAssessment{{}}},
		{ReviewCoverage: &ReviewCoverage{Gaps: []string{"uninspected path"}}},
	}
	for i, outcome := range outcomes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			dir, j, _, pub, runner := ciHeldReviewFixture(t)
			j.Outcome = outcome
			if err := ValidateCIHeldReviewStart(*j); err == nil {
				t.Fatal("saved work overwritten at start")
			}
			j.Outcome = testOutcome("implemented")
			pub.failAt = 3
			if err := runner.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("expected boundary failure")
			}
			j.Outcome = outcome
			if err := ValidateDiscardCIHeldReview(*j); err == nil {
				t.Fatal("saved work discarded after failure")
			}
		})
	}
}

func TestCIHeldReviewDiscardCannotBypassPendingTransactions(t *testing.T) {
	dir, j, _, pub, runner := ciHeldReviewFixture(t)
	pub.failAt = 3
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected boundary failure")
	}
	j.PendingInputs = &PendingInputRecovery{}
	if err := ValidateDiscardCIHeldReview(*j); err == nil {
		t.Fatal("pending input bypassed")
	}
	j.PendingInputs = nil
	comment := generalFeedback("comment:1", "A pending comment")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, CompletedHead: testHead, Entries: []PRFeedbackEntry{{Comment: comment, Fingerprint: feedbackFingerprint(comment)}}}
	if err := ValidateDiscardCIHeldReview(*j); err == nil {
		t.Fatal("pending feedback bypassed")
	}
}
