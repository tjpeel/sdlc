package workrun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type repeatedRecommendationProvider struct{ localAssessmentProvider }

func (p *repeatedRecommendationProvider) Execute(ctx context.Context, s Session, out, diagnostic io.Writer) (SessionResult, error) {
	result, err := p.localAssessmentProvider.Execute(ctx, s, out, diagnostic)
	if s.Role == "review" {
		copy := *p.j
		withRecommendation(&copy)
		result.Outcome.Recommendations = copy.ReviewReports[0].Recommendations
	}
	return result, err
}

func TestAutomaticRecommendationsUseExistingRepairAndVerification(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	withRecommendation(j)
	j.Plan.Roles.AutoAcceptRecommendations = true
	p := &localFeedbackPublisher{feedbackPublisherFake: pub}
	provider := &localAssessmentProvider{j: j}
	r.Publisher, r.Provider = p, provider
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls != 2 || len(provider.sessions) != 3 || len(j.AutoRecommendationHeads) != 1 {
		t.Fatal("automatic acceptance did not complete the normal cycle", j.State, checks.calls, len(provider.sessions))
	}
	if pub.reads != 0 || pub.replies != 0 {
		t.Fatal("automatic acceptance posted feedback comments")
	}
	saved, err := Load(dir)
	if err != nil || !saved.Plan.Roles.AutoAcceptRecommendations || len(saved.AutoRecommendationHeads) != 1 {
		t.Fatal("automatic authority/history not retained", err)
	}
}

func TestAutomaticRecommendationsStopWithoutSourceProgress(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	withRecommendation(j)
	j.Plan.Roles.AutoAcceptRecommendations = true
	p := &localFeedbackPublisher{feedbackPublisherFake: pub}
	provider := &repeatedRecommendationProvider{localAssessmentProvider{j: j}}
	r.Publisher, r.Provider = p, provider
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.State != "blocked" || !strings.Contains(j.StopReason, "without source progress") || checks.calls != 2 || len(provider.sessions) != 3 {
		t.Fatal("repeated no-progress acceptance continued", j.State, j.StopReason)
	}
}

func TestReviewOnlyEntryNeverAutomaticallyRepairs(t *testing.T) {
	dir, j, r, _, provider, checks := checkpointFixture(t, "review")
	j.Plan.Roles.AutoAcceptRecommendations = true
	out := testOutcome("reviewed")
	out.Recommendations = []Finding{{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Unused state.", Recommendation: "Remove unused state."}}
	provider.outcomes = []Outcome{out}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.sessions) != 1 || checks.calls != 0 || len(j.AutoRecommendationHeads) != 0 {
		t.Fatal("review-only entry started automatic repairs")
	}
}

func TestDecisionAuthorityOnlyResumesRecordedRecommendationChoice(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	withRecommendation(j)
	text, err := RecommendationFeedback(*j)
	if err != nil {
		t.Fatal(err)
	}
	r.LocalPRFeedback = text
	r.Publisher = &localFeedbackPublisher{feedbackPublisherFake: pub}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.State, j.ResumeState, j.PendingRole = "waiting_for_human", "repairing", "implementation"
	j.Outcome = testOutcome("waiting_for_human")
	j.Outcome.Questions = []string{"Remove the unused surface or retain it?"}
	answer, err := RecommendationDecisionAnswer(*j)
	if err != nil || !strings.Contains(answer, "app.go:12") {
		t.Fatal("bound decision could not resume", err)
	}
	original := j.PRFeedback.Entries[0].Comment.Body
	j.PRFeedback.Entries[0].Comment.Body = "Unrelated operator feedback"
	if _, err := RecommendationDecisionAnswer(*j); err == nil {
		t.Fatal("unrelated question auto-answered")
	}
	j.PRFeedback.Entries[0].Comment.Body = original
	j.Publication.HeadSHA = strings.Repeat("e", 40)
	if _, err := RecommendationDecisionAnswer(*j); err == nil {
		t.Fatal("stale decision authority accepted")
	}
}
