package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pendingVerificationChecker struct {
	patchFixtureChecker
	requests        []VerificationRequest
	verificationErr error
}

func (checker *pendingVerificationChecker) Verify(_ context.Context, _ string, request VerificationRequest, output io.Writer) error {
	checker.requests = append(checker.requests, request)
	io.WriteString(output, "private-generic-output\n")
	return checker.verificationErr
}

func TestAdditionalFailureDoesNotSkipPendingSourceCorrection(t *testing.T) {
	for _, cached := range []bool{true, false} {
		name := "new semantic failure"
		if cached {
			name = "cached semantic failure"
		}
		t.Run(name, func(t *testing.T) {
			directory, journal := testRun(t)
			journal.SessionID = testNative
			sourceWrite(t, journal.Workspace, "src/Example.cs", "before\n")
			old := VerificationRequest{ID: "prior-format", Purpose: "check whether formatting preserves current source", Commands: [][]string{{"formatter", "check", "src/Example.cs"}}}
			oldKey := verificationKey(journal, old, testHead, testTree)
			if cached {
				journal.Verification = []VerificationResult{{Request: old, Key: oldKey, Head: testHead, Tree: testTree, Passed: false, Diagnostic: "Prior fixture failure retained"}}
			}
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
			if !cached {
				asked.VerificationRequests = append([]VerificationRequest{old}, asked.VerificationRequests...)
			}
			provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("waiting_for_human")}}
			checker := &pendingVerificationChecker{patchFixtureChecker: patchFixtureChecker{patch: patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")}, verificationErr: ErrCheckFailed}
			publisher := &fakePublisher{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
			runner.Checker = checker
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			wantGeneric := 1
			if cached {
				wantGeneric = 0
			}
			if checker.patchCalls != 1 || len(checker.requests) != wantGeneric || len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative || publisher.calls != 0 {
				t.Fatalf("pending correction skipped or cached failure rerun: patch=%d generic=%d sessions=%d published=%d", checker.patchCalls, len(checker.requests), len(provider.calls), publisher.calls)
			}
			if journal.Verification[0].Passed || journal.Verification[0].Key != oldKey || journal.Verification[1].Passed || journal.Verification[1].SourcePatch == nil {
				t.Fatal("old failure or unapplied correction lost")
			}
			prompt := provider.calls[1].Prompt
			if !strings.Contains(prompt, old.ID) || !strings.Contains(prompt, "+after") || strings.Contains(prompt, "private-generic-output") || strings.Contains(prompt, "untrusted-formatter-output") {
				t.Fatal("combined safe failure and correction feedback missing")
			}
			if data, _ := os.ReadFile(filepath.Join(journal.Workspace, "src/Example.cs")); string(data) != "before\n" {
				t.Fatal("controller applied source correction")
			}
			loaded, err := Load(directory)
			if err != nil {
				t.Fatal(err)
			}
			loaded.State, loaded.ResumeState, loaded.StopReason = "blocked", "checking", "unchanged failed verification"
			if err := runner.Run(context.Background(), directory, &loaded, ""); !errors.Is(err, ErrStopped) || loaded.State != "blocked" || checker.patchCalls != 1 || len(checker.requests) != wantGeneric || len(provider.calls) != 2 || publisher.calls != 0 {
				t.Fatal("unchanged failures retried or published", err)
			}
		})
	}
}

func TestAdditionalPassingPendingWorkDoesNotClearCachedFailure(t *testing.T) {
	directory, journal := testRun(t)
	journal.SessionID = testNative
	old := VerificationRequest{ID: "prior-check", Purpose: "retain the earlier required failure", Commands: [][]string{{"tests", "prior"}}}
	journal.Verification = []VerificationResult{{Request: old, Key: verificationKey(journal, old, testHead, testTree), Head: testHead, Tree: testTree, Passed: false, Diagnostic: "Prior fixture failure retained"}}
	next := VerificationRequest{ID: "new-check", Purpose: "collect new machine evidence", Commands: [][]string{{"tests", "new"}}}
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{next}
	provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("implemented")}}
	checker := &pendingVerificationChecker{}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) || journal.State != "blocked" || publisher.calls != 0 {
		t.Fatal("passing new evidence bypassed old failure", err)
	}
	if len(checker.requests) != 1 || checker.requests[0].ID != next.ID || len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative || journal.Verification[0].Passed || !journal.Verification[1].Passed {
		t.Fatal("cached failure reran, vanished or blocked new work")
	}
	if !strings.Contains(provider.calls[1].Prompt, "Prior fixture failure retained") {
		t.Fatal("old failure absent from combined feedback")
	}
}

func TestAdditionalOperationalFailureStopsBeforeLaterPendingWork(t *testing.T) {
	for _, failure := range []error{errors.New("disposable runtime failure"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			directory, journal := testRun(t)
			old := VerificationRequest{ID: "prior-check", Purpose: "retain earlier semantic evidence", Commands: [][]string{{"tests", "prior"}}}
			journal.Verification = []VerificationResult{{Request: old, Key: verificationKey(journal, old, testHead, testTree), Head: testHead, Tree: testTree, Passed: false}}
			operational := VerificationRequest{ID: "operational-check", Purpose: "collect new machine evidence", Commands: [][]string{{"tests", "new"}}}
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{operational, sourcePatchRequest()}
			provider := &fakeProvider{outcomes: []Outcome{asked}}
			checker := &pendingVerificationChecker{verificationErr: failure}
			publisher := &fakePublisher{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
			runner.Checker = checker
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) || journal.State != "blocked" || journal.ResumeState != "checking" || publisher.calls != 0 {
				t.Fatal("operational stop bypassed", err)
			}
			if len(checker.requests) != 1 || checker.patchCalls != 0 || len(provider.calls) != 1 || journal.Verification[1].Key != "" || journal.Verification[0].Passed {
				t.Fatal("operational failure ran later work or discarded semantic failure")
			}
		})
	}
}
