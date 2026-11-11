package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func migratedLimitedReviewFixture(t *testing.T) (string, *Journal, *ciHeldFeedbackPublisher, Runner) {
	t.Helper()
	dir, j, _, base, _, runner := runtimeRecheckFixture(t)
	j.Plan.Ticket = "01-example.md"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.RuntimeRecheck == nil || j.RuntimeRecheck.Pending {
		t.Fatalf("runtime recheck: %v", err)
	}
	review := testOutcome("reviewed")
	review.Limitations = []string{"Deployment access was unavailable"}
	runner.Provider = &fakeProvider{outcomes: []Outcome{review}}
	runner.ReviewCIHold = true
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.ResumeState != "ci" {
		t.Fatalf("CI-held review: %v", err)
	}
	if j.SessionID != "" || len(j.RuntimeMigrations) != 1 || !reflect.DeepEqual(j.ReviewReports[len(j.ReviewReports)-1].Limitations, review.Limitations) {
		t.Fatal("migration or review lineage lost")
	}
	pub := &ciHeldFeedbackPublisher{ciHeldPublisher: base}
	runner.Publisher, runner.ReviewCIHold = pub, false
	return dir, j, pub, runner
}

func TestMigratedCIHeldLimitedReviewAllowsFeedbackScanButNotProvisional(t *testing.T) {
	dir, j, pub, runner := migratedLimitedReviewFixture(t)
	if err := ValidatePRFeedbackResume(*j); err != nil {
		t.Fatalf("recorded limitation blocked feedback: %v", err)
	}
	runner.ProcessPRFeedback = true
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("feedback scan: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "blocked" || saved.ResumeState != "ci" || saved.CI.Status == "passed" || saved.SessionID != "" || saved.PRFeedback == nil || saved.PRFeedback.CIHeldScan == nil || pub.reads != 1 || pub.calls != 0 || pub.ci != 0 {
		t.Fatal("scan changed readiness, CI or publication")
	}
	if len(saved.Outcome.Limitations) != 1 || len(saved.ReviewReports[len(saved.ReviewReports)-1].Limitations) != 1 {
		t.Fatal("review limitation was not retained")
	}
	if _, err := ProvisionalProofForCIHold(saved); err == nil || !strings.Contains(err.Error(), "limitations") {
		t.Fatal("review limitation was treated as provisional readiness")
	}
}

func TestMigratedCIHeldLimitedReviewFeedbackStartsFreshSession(t *testing.T) {
	dir, j, base, runner := migratedLimitedReviewFixture(t)
	base.comments = []PRFeedbackComment{generalFeedback("comment:1", "Please inspect this")}
	provider := &interruptedMigratedProvider{}
	runner.Provider, runner.ProcessPRFeedback = provider, true
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected provider interruption")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.calls[0].ResumeID != "" || saved.RuntimeRecheck.ImplementationAttempt == 0 || len(saved.ReviewReports[len(saved.ReviewReports)-1].Limitations) != 1 || saved.Publication != saved.CIHeldReview.Publication || base.calls != 0 {
		t.Fatal("fresh session or retained review boundary lost")
	}
	runner.ProcessPRFeedback = false
	if err := runner.Run(context.Background(), dir, &saved, ""); err == nil || len(provider.calls) != 1 {
		t.Fatal("interrupted fresh session restarted")
	}
}

func TestCIHeldFeedbackRejectsUnrecordedReviewWork(t *testing.T) {
	dir, _, _, _ := migratedLimitedReviewFixture(t)
	cases := []struct {
		name   string
		mutate func(*Journal)
	}{
		{"unrecorded limitation", func(j *Journal) { j.Outcome.Limitations = append(j.Outcome.Limitations, "not recorded") }},
		{"findings", func(j *Journal) {
			j.Outcome.Findings = []Finding{{Priority: "P1", Path: "app.go", Line: 1, Scenario: "failure", Recommendation: "fix"}}
		}},
		{"questions", func(j *Journal) { j.Outcome.Questions = []string{"decision?"} }},
		{"stale review", func(j *Journal) { j.ReviewReports[len(j.ReviewReports)-1].Attempt-- }},
		{"pending recheck", func(j *Journal) { j.RuntimeRecheck.Pending = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&j)
			if ValidatePRFeedbackResume(j) == nil {
				t.Fatal("unrecorded or unfinished work was accepted")
			}
		})
	}
}
