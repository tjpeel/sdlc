package workrun

import (
	"context"
	"errors"
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
