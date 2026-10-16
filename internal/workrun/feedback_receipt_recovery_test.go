package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestInvalidFeedbackReceiptRequestsSameSessionCorrection(t *testing.T) {
	dir, j, r, checker, provider := savedFeedbackReplayFixture(t)
	j.State, j.ResumeState, j.StopReason = "implementing", "", ""
	j.RunLimitOverride = nil
	j.PRFeedback.Entries[0].Provisional = true
	j.Outcome.PRFeedbackAssessments[0].Commits = []string{testBase}
	old := *j.PRFeedback.Entries[0].Assessment
	session, attempt, rounds := j.SessionID, j.Attempt, j.Rounds
	if err := r.acceptImplementationOutcome(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if j.State != "repairing" || !strings.Contains(j.Feedback, testBase) || !strings.Contains(j.Feedback, testSigned) || !strings.Contains(j.Feedback, "do not edit source") {
		t.Fatal("missing receipt-only correction", j.State, j.Feedback)
	}
	if !reflect.DeepEqual(old, *j.PRFeedback.Entries[0].Assessment) || checker.calls != 0 || len(provider.calls) != 0 || session != j.SessionID || attempt != j.Attempt || rounds != j.Rounds {
		t.Fatal("invalid receipt changed assessments, checks, session or budgets")
	}
}

func TestReceiptCorrectionDoesNotRecoverOperationalFailures(t *testing.T) {
	dir, j, r, _, _ := savedFeedbackReplayFixture(t)
	j.State = "implementing"
	handled, err := r.correctFeedbackReceipt(dir, j, errors.New("repository unavailable"))
	if handled || err != nil {
		t.Fatal("operational failure treated as receipt correction")
	}
}

func TestSavedInvalidReceiptResumeRetainsExactCorrectionContext(t *testing.T) {
	dir, j, r, _, provider := savedFeedbackReplayFixture(t)
	j.StopReason = "claimed feedback commit is absent from candidate history"
	j.RunLimitOverride = nil
	j.PRFeedback.Entries[0].Provisional = true
	j.Outcome.PRFeedbackAssessments[0].Commits = []string{testBase}
	stop := errors.New("correction staged")
	r.OnState = func(saved Journal) error {
		if saved.State == "repairing" && strings.Contains(saved.Feedback, testSigned) {
			return stop
		}
		return nil
	}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, stop) {
		t.Fatal("saved stop not recoverable", err)
	}
	if len(provider.calls) != 0 {
		t.Fatal("dispatched before recording correction context")
	}
}
