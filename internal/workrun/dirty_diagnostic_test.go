package workrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type dirtyDiagnosticRepository struct{ *fakeRepository }

func (r *dirtyDiagnosticRepository) Inspect(context.Context, string) (Revision, error) {
	return Revision{Head: testHead, Tree: testTree, Clean: false}, nil
}

func dirtyDiagnosticFixture(t *testing.T) (string, *Journal, *fakeProvider, *fakeChecker, *fakePublisher, Runner) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "checking", "commit the candidate changes before requesting isolated verification"
	j.SessionID = testNative
	j.Outcome = diagnosticOutcome("probe")
	j.Outcome.Limitations = []string{"The formatter failed and pending edits require attention"}
	mergeDiagnostics(j, j.Outcome.DiagnosticRequests)
	earlier := diagnosticRequest("earlier")
	j.LiveDiagnostics = []VerificationResult{{Request: earlier, Key: verificationKey(j, earlier, testHead, testTree), Head: testHead, Tree: testTree, Passed: true}}
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true, Key: verificationKey(j, VerificationRequest{}, testHead, testTree)}
	if err := os.WriteFile(filepath.Join(j.Workspace, "pending.txt"), []byte("uncommitted work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("blocked")}}
	checker, pub, repo := &fakeChecker{}, &fakePublisher{}, &fakeRepository{}
	runner := fakeRunner(provider, checker, pub, repo)
	runner.Repository = &dirtyDiagnosticRepository{fakeRepository: repo}
	return dir, j, provider, checker, pub, runner
}

func TestDirtyDiagnosticResumeContinuesOriginalImplementationWithoutChecks(t *testing.T) {
	dir, j, provider, checker, pub, runner := dirtyDiagnosticFixture(t)
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected implementation hold: %v", err)
	}
	if len(provider.calls) != 1 || provider.calls[0].ResumeID != testNative || !strings.Contains(provider.calls[0].Prompt, "did not run the requested diagnostics") || !strings.Contains(provider.calls[0].Prompt, "formatter failed") || !strings.Contains(provider.calls[0].Prompt, `"id":"probe"`) {
		t.Fatal("original diagnostic and limitations did not reach the same session")
	}
	if checker.calls != 0 || pub.calls != 0 || pub.ci != 0 || j.Evidence.Passed || j.State != "blocked" || j.ResumeState != "implementing" || len(j.Diagnostics) != 1 || j.Diagnostics[0].Passed {
		t.Fatal("dirty recovery ran checks or granted readiness")
	}
	if len(j.LiveDiagnostics) != 1 || !j.LiveDiagnostics[0].Passed || j.LiveDiagnostics[0].Request.ID != "earlier" {
		t.Fatal("previous checkpoint diagnostic was lost")
	}
	data, err := os.ReadFile(filepath.Join(j.Workspace, "pending.txt"))
	if err != nil || string(data) != "uncommitted work" {
		t.Fatal("pending edits were altered")
	}
}

func TestDirtyDiagnosticRecoveryPreservesCheckpointOnProviderFailure(t *testing.T) {
	dir, j, provider, checker, pub, runner := dirtyDiagnosticFixture(t)
	provider.statusErr = errors.New("provider unavailable")
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected hold: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ResumeState != "implementing" || saved.Outcome.Status != "diagnostics_requested" || !strings.Contains(saved.Feedback, "did not run") || saved.SessionID != testNative {
		t.Fatal("recovery checkpoint did not retain the original handoff")
	}
	provider.statusErr = nil
	if err := runner.Run(context.Background(), dir, &saved, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected implementation hold: %v", err)
	}
	if len(provider.calls) != 1 || provider.calls[0].ResumeID != testNative || checker.calls != 0 || pub.calls != 0 {
		t.Fatal("recovery duplicated work or switched sessions")
	}
}

func TestDirtyDiagnosticRecoveryRejectsOtherPendingWork(t *testing.T) {
	_, j, _, _, _, _ := dirtyDiagnosticFixture(t)
	cases := []struct {
		name   string
		change func(*Journal)
	}{
		{"other outcome", func(j *Journal) { j.Outcome = testOutcome("checks_requested") }},
		{"missing session", func(j *Journal) { j.SessionID = "" }},
		{"published", func(j *Journal) { j.Publication.Number = 1 }},
		{"pending inputs", func(j *Journal) { j.PendingInputs = &PendingInputRecovery{} }},
		{"pending candidate", func(j *Journal) { j.CandidatePhasePending = true }},
		{"human question", func(j *Journal) { j.Outcome.Questions = []string{"choose?"} }},
		{"feedback", func(j *Journal) { j.PRFeedback = &PRFeedbackHistory{Active: true} }},
		{"reconciliation", func(j *Journal) { j.Reconciliation = &Reconciliation{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *j
			tc.change(&copy)
			if _, ok := dirtyDiagnosticFeedback(&copy); ok {
				t.Fatal("unsafe recovery admitted")
			}
		})
	}
}

func TestDirtyConfiguredCheckRequestStillStops(t *testing.T) {
	dir, j, provider, checker, pub, runner := dirtyDiagnosticFixture(t)
	j.Outcome = testOutcome("checks_requested")
	j.Diagnostics = nil
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected dirty hold: %v", err)
	}
	if j.ResumeState != "checking" || j.StopReason != "commit the candidate changes before requesting isolated verification" || len(provider.calls) != 0 || checker.calls != 0 || pub.calls != 0 {
		t.Fatal("configured check hold was bypassed")
	}
}

func TestDirtyDiagnosticRecoveryDoesNotAcceptUncommittedImplementation(t *testing.T) {
	dir, j, provider, checker, pub, runner := dirtyDiagnosticFixture(t)
	provider.outcomes = []Outcome{testOutcome("implemented")}
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected dirty verification hold: %v", err)
	}
	if j.ResumeState != "checking" || j.Evidence.Passed || checker.calls != 0 || pub.calls != 0 || j.Publication.Number != 0 {
		t.Fatal("uncommitted implementation bypassed checks or publication gates")
	}
}

func TestDirtyDiagnosticRecoveryReturnsRepeatedRequestsWithoutGrantingChecks(t *testing.T) {
	dir, j, provider, checker, pub, runner := dirtyDiagnosticFixture(t)
	provider.outcomes = []Outcome{diagnosticOutcome("retry"), testOutcome("blocked")}
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected implementation hold: %v", err)
	}
	if len(provider.calls) != 2 || provider.calls[0].ResumeID != testNative || provider.calls[1].ResumeID != testNative || !strings.Contains(provider.calls[1].Prompt, `"id":"retry"`) || checker.calls != 0 || pub.calls != 0 || j.Rounds != 0 {
		t.Fatal("repeated dirty diagnostic crossed gates or reset the session")
	}
}
