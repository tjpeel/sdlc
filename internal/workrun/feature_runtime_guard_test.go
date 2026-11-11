package workrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPendingRuntimeRecheckRejectsRecoveryActionsBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Runner) string
	}{
		{"rebase", func(r *Runner) string { r.Rebase = true; return "" }},
		{"PR feedback", func(r *Runner) string { r.ProcessPRFeedback = true; return "" }},
		{"CI feedback", func(r *Runner) string { r.CIFeedback = "failure"; return "" }},
		{"review", func(r *Runner) string { r.ReviewCIHold = true; return "" }},
		{"discard review", func(r *Runner) string { r.DiscardCIHeldReview = true; return "" }},
		{"manual checkpoint", func(r *Runner) string { r.StartAt = "checks"; return "" }},
		{"override limits", func(r *Runner) string { r.OverrideRunLimits = true; return "" }},
		{"CI repair budget", func(r *Runner) string { r.InitializeCIRepairBudget = true; return "" }},
		{"review repair budget", func(r *Runner) string { r.InitializeReviewRepairBudget = 1; return "" }},
		{"review repair limit", func(r *Runner) string { r.ReviewRepairLimit = 1; return "" }},
		{"turn extension", func(r *Runner) string { r.ExtendSessionTurnBudget = 1; return "" }},
		{"answer", func(r *Runner) string { return "answer" }},
		{"recommendations", func(r *Runner) string { b := true; r.AutoAcceptRecommendationsOverride = &b; return "" }},
		{"abandon and rebase", func(r *Runner) string { r.AbandonRuntimeRecheck = true; r.Rebase = true; return "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, j, checker, pub, repo, runner := runtimeRecheckFixture(t)
			before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			provider := runner.Provider.(*fakeProvider)
			callbacks := 0
			runner.ValidateDependencies = func(context.Context, string, Plan) error { callbacks++; return nil }
			answer := tc.change(&runner)
			if err := runner.Run(context.Background(), dir, j, answer); err == nil {
				t.Fatal("recovery action was admitted")
			}
			after, err := os.ReadFile(filepath.Join(dir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || repo.calls != 0 || repo.bundles != 0 || len(provider.calls) != 0 || checker.calls != 0 || pub.calls != 0 || pub.ci != 0 || callbacks != 0 {
				t.Fatalf("rejected action caused side effects: repository=%d provider=%d checks=%d publish=%d CI=%d callbacks=%d", repo.calls, len(provider.calls), checker.calls, pub.calls, pub.ci, callbacks)
			}
		})
	}
}

func TestAbandonRuntimeRecheckRetainsHoldWithoutPublishing(t *testing.T) {
	dir, j, checker, pub, repo, runner := runtimeRecheckFixture(t)
	prior := j.Publication
	runner.AbandonRuntimeRecheck = true
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Publication != prior || saved.State != "blocked" || saved.ResumeState != "implementing" || saved.RuntimeRecheck.Pending || saved.RuntimeRecheck.AbandonedAt == nil || saved.Evidence.Passed || saved.CI.Status != "" {
		t.Fatalf("abandon did not retain a non-ready checkpoint: %+v", saved)
	}
	if repo.calls != 0 || checker.calls != 0 || pub.calls != 0 || pub.ci != 0 || len(runner.Provider.(*fakeProvider).calls) != 0 {
		t.Fatal("abandon invoked a work or publication boundary")
	}
	if ValidatePRFeedbackResume(saved) == nil {
		t.Fatal("abandon granted PR feedback readiness")
	}
}

func TestAbandonRuntimeRecheckRejectsOtherPendingWork(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Journal)
	}{
		{"input", func(j *Journal) { j.PendingInputs = &PendingInputRecovery{} }},
		{"candidate", func(j *Journal) { j.CandidatePhasePending = true }},
		{"reconciliation", func(j *Journal) { j.Reconciliation = &Reconciliation{} }},
		{"review continuation", func(j *Journal) { j.ReviewContinuation = &ReviewContinuation{} }},
		{"review interruption", func(j *Journal) { j.ReviewInterruption = &ReviewInterruption{} }},
		{"pending role", func(j *Journal) { j.PendingRole = "review" }},
		{"active feedback", func(j *Journal) { j.PRFeedback = &PRFeedbackHistory{Active: true} }},
		{"review outcome", func(j *Journal) { j.Outcome.Questions = []string{"question"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, j, _, _, _, _ := runtimeRecheckFixture(t)
			tc.change(j)
			if err := ValidateAbandonRuntimeRecheck(*j); err == nil {
				t.Fatal("pending work was admitted")
			}
		})
	}
}
