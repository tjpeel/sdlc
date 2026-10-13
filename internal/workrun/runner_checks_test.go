package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type controllerPhaseChecker struct {
	supplementaryChecker
	phases        int
	cleanupErr    error
	beforeCleanup func()
}

func (checker *controllerPhaseChecker) WithCandidate(_ context.Context, _ string, _ io.Writer, callback func(Checker) error) error {
	checker.phases++
	err := callback(&checker.supplementaryChecker)
	if checker.beforeCleanup != nil {
		checker.beforeCleanup()
	}
	return errors.Join(err, checker.cleanupErr)
}

func TestControllerCandidatePhaseKeepsPassingEvidenceAndClosesBeforePublishing(t *testing.T) {
	directory, journal := testRun(t)
	implemented := testOutcome("implemented")
	implemented.VerificationRequests = []VerificationRequest{regressionRequest()}
	provider := &fakeProvider{outcomes: []Outcome{implemented, testOutcome("reviewed")}}
	checker := &controllerPhaseChecker{}
	checker.beforeCleanup = func() {
		checkpoint, err := Load(directory)
		if err != nil || checkpoint.State != "checking" || !checkpoint.CandidatePhasePending {
			t.Fatal("publication became eligible before cleanup", checkpoint.State, err)
		}
	}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	if checker.phases != 1 || checker.calls != 1 || len(checker.requests) != 1 || publisher.calls != 1 || journal.State != "ready" {
		t.Fatal("candidate phase repeated or lost its evidence")
	}
}

func TestControllerPendingCandidatePhaseRechecksProvisionalEvidence(t *testing.T) {
	directory, journal := testRun(t)
	journal.State = "checking"
	journal.CandidatePhasePending = true
	journal.CheckAttempt = 1
	journal.Outcome = testOutcome("implemented")
	journal.Plan.PRTitle, journal.Plan.PRBody = journal.Outcome.PRTitle, journal.Outcome.PRBody
	journal.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	request := regressionRequest()
	journal.Verification = []VerificationResult{{Request: request, Key: verificationKey(journal, request, testHead, testTree), Passed: true}}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	checker := &controllerPhaseChecker{}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	if checker.phases != 1 || checker.calls != 1 || len(checker.requests) != 1 || journal.CandidatePhasePending || publisher.calls != 1 {
		t.Fatal("provisional evidence survived interrupted phase cleanup")
	}
}

func TestControllerCandidateCleanupFailureInvalidatesEvidenceBeforeResume(t *testing.T) {
	directory, journal := testRun(t)
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &controllerPhaseChecker{cleanupErr: errors.New("disposable cleanup failure")}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); err == nil || journal.State != "blocked" || journal.ResumeState != "checking" || journal.Evidence.Passed || publisher.calls != 0 || !strings.Contains(journal.StopReason, "cleanup failure") {
		t.Fatal("cleanup failure allowed publication or reusable evidence", err)
	}
	checker.cleanupErr = nil
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), directory, &loaded, ""); err != nil {
		t.Fatal(err)
	}
	if checker.phases != 2 || checker.calls != 2 || publisher.calls != 1 {
		t.Fatal("resume skipped fresh verification after cleanup failure")
	}
}

func TestControllerConfiguredFailureRunsPendingVerificationBeforeRepair(t *testing.T) {
	for _, additionalFailed := range []bool{false, true} {
		t.Run(fmt.Sprint(additionalFailed), func(t *testing.T) {
			directory, journal := testRun(t)
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{regressionRequest()}
			provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("waiting_for_human")}}
			checker := &controllerPhaseChecker{}
			checker.fakeChecker.err = &CheckFailure{Command: 1, ExitCode: 1, csharpier: true, paths: []string{"src/Example.cs"}, formatterLocations: map[string]csharpierLocation{"src/Example.cs": {kind: "Expected", line: "42"}}}
			if additionalFailed {
				checker.err = &VerificationFailure{Phase: "candidate", Err: &CheckFailure{Command: 1, ExitCode: 2, compiler: &compilerDiagnostic{code: "CS0104", path: "src/Other.cs", line: "7", column: "3"}}}
			}
			publisher := &fakePublisher{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
			runner.Checker = checker
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			if checker.calls != 1 || len(checker.requests) != 1 || journal.Evidence.Passed || publisher.calls != 0 || len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative {
				t.Fatalf("pending diagnostic skipped or failed suite bypassed: suite=%d requested=%d publish=%d sessions=%d", checker.calls, len(checker.requests), publisher.calls, len(provider.calls))
			}
			prompt := provider.calls[1].Prompt
			if !strings.Contains(prompt, "src/Example.cs (CSharpier Expected around line 42)") || !strings.Contains(prompt, `"additional"`) || journal.Verification[0].Passed == additionalFailed {
				t.Fatal("configured or additional evidence missing", prompt)
			}
			if additionalFailed && !strings.Contains(prompt, "src/Other.cs:7:3") {
				t.Fatal("mixed failure lost compiler location", prompt)
			}
			if strings.Contains(prompt, "private test output") {
				t.Fatal("private log reached feedback")
			}
		})
	}
}

func TestControllerConfiguredFailureStopsOnAdditionalOperationalFailure(t *testing.T) {
	directory, journal := testRun(t)
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{regressionRequest()}
	provider := &fakeProvider{outcomes: []Outcome{asked}}
	checker := &controllerPhaseChecker{}
	checker.fakeChecker.err = ErrCheckFailed
	checker.err = errors.New("disposable transport failure")
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) || journal.State != "blocked" || journal.ResumeState != "checking" || journal.Evidence.Passed || publisher.calls != 0 || len(provider.calls) != 1 || len(checker.requests) != 1 {
		t.Fatal("additional operational failure reached repair or publication", err)
	}
	if journal.Verification[0].Key != "" || !strings.Contains(journal.Feedback, "Configured checks failed") {
		t.Fatal("operational failure cached or configured feedback lost")
	}
}

func TestControllerConfiguredImmediateStopsSkipPendingVerification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		rounds  int
	}{
		{"exhausted bound", ErrCheckFailed, 1},
		{"operational failure", errors.New("disposable transport failure"), 0},
		{"cancellation", context.Canceled, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, journal := testRun(t)
			journal.Rounds = tc.rounds
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{regressionRequest()}
			provider := &fakeProvider{outcomes: []Outcome{asked}}
			checker := &supplementaryChecker{}
			checker.fakeChecker.err = tc.failure
			publisher := &fakePublisher{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
			runner.Checker = checker
			runner.MaxRounds = 1
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) || journal.Evidence.Passed || publisher.calls != 0 || len(provider.calls) != 1 || len(checker.requests) != 0 {
				t.Fatal("immediate stop ran pending verification or repair", err)
			}
		})
	}
}

func TestControllerConfiguredFailureReusesCompletedSuiteForFreshDiagnostic(t *testing.T) {
	directory, journal := testRun(t)
	first := testOutcome("checks_requested")
	first.VerificationRequests = []VerificationRequest{regressionRequest()}
	next := first
	next.VerificationRequests = []VerificationRequest{{ID: "new-diagnostic", Purpose: "inspect another assertion", Commands: [][]string{{"tests", "diagnostic"}}}}
	provider := &fakeProvider{outcomes: []Outcome{first, next, testOutcome("waiting_for_human")}}
	checker := &controllerPhaseChecker{}
	checker.fakeChecker.err = &CheckFailure{Command: 1, ExitCode: 1, csharpier: true, paths: []string{"src/Example.cs"}, formatterLocations: map[string]csharpierLocation{"src/Example.cs": {kind: "Expected", line: "42"}}}
	checker.err = ErrCheckFailed
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if checker.calls != 1 || len(checker.requests) != 2 || checker.requests[1].ID != "new-diagnostic" || publisher.calls != 0 || journal.Evidence.Passed || journal.Rounds != 2 {
		t.Fatalf("fresh diagnostic repeated failed suite or bypassed failure: suites=%d diagnostics=%d publications=%d rounds=%d", checker.calls, len(checker.requests), publisher.calls, journal.Rounds)
	}
	if len(provider.calls) != 3 || !strings.Contains(provider.calls[2].Prompt, "src/Example.cs (CSharpier Expected around line 42)") || !strings.Contains(provider.calls[2].Prompt, regressionRequest().ID) || journal.Verification[0].Passed || journal.Verification[1].Passed {
		t.Fatal("prior configured or additional failure evidence disappeared")
	}
}

func TestControllerFreshDiagnosticsRetainConfiguredRepairBound(t *testing.T) {
	directory, journal := testRun(t)
	outcomes := make([]Outcome, 6)
	for i := range outcomes {
		outcomes[i] = testOutcome("checks_requested")
		outcomes[i].VerificationRequests = []VerificationRequest{{ID: "diagnostic", Purpose: "inspect assertion", Commands: [][]string{{"tests", fmt.Sprint(i)}}}}
	}
	provider := &fakeProvider{outcomes: outcomes}
	checker := &controllerPhaseChecker{}
	checker.fakeChecker.err = ErrCheckFailed
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	runner.MaxRounds = 3
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if !strings.Contains(journal.StopReason, configuredCheckRepairBound) || journal.Rounds != 4 || checker.calls != 1 || len(checker.requests) != 3 || len(provider.calls) != 4 || publisher.calls != 0 || journal.Evidence.Passed {
		t.Fatalf("diagnostics evaded repair bound: rounds=%d suite=%d diagnostics=%d provider=%d stop=%s", journal.Rounds, checker.calls, len(checker.requests), len(provider.calls), journal.StopReason)
	}
}
