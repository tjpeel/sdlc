package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func independentRepairReceiptFixture(t *testing.T) (string, *Journal, Runner, *supplementaryChecker, *fakeProvider) {
	dir, j, r, checker, provider := savedFeedbackReplayFixture(t)
	old := j.Outcome.PRFeedbackAssessments[0]
	j.PRFeedback.Entries[0].Assessment = &old
	j.PRFeedback.Entries[0].ReplyBody = old.Reason
	j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Pending: true, Acknowledged: true, StartedAttempt: j.Attempt, Findings: []Finding{{Priority: "P2", Path: "app.go", Line: 1, Scenario: "Missing durable check", Recommendation: "Add check"}}}
	j.ReviewRepairBudget = &ReviewRepairBudget{Used: 1, StartRound: 0, Limit: 3, LastGrantedHead: j.Publication.HeadSHA}
	j.Rounds = 1
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j, r, checker, provider
}
func TestIndependentRepairReplaysWithoutHumanReassessmentOrModelWork(t *testing.T) {
	dir, j, r, checker, provider := independentRepairReceiptFixture(t)
	original := *j.PRFeedback.Entries[0].Assessment
	session, attempt := j.SessionID, j.Attempt
	stop := errors.New("checks complete")
	r.OnState = func(saved Journal) error {
		if saved.State == "implementing" && checker.calls == 1 && len(checker.requests) == 1 {
			return stop
		}
		return nil
	}
	if !CanReplaySavedPRFeedback(*j) {
		t.Fatal("saved review repair not admitted")
	}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if len(provider.calls) != 0 || j.SessionID != session || j.Attempt != attempt || checker.calls != 1 || !j.Evidence.Passed || len(j.Outcome.PRFeedbackAssessments) != 0 {
		t.Fatal("recovery repeated work or skipped checks")
	}
	before, _ := json.Marshal(original)
	after, _ := json.Marshal(j.PRFeedback.Entries[0].Assessment)
	if string(before) != string(after) || len(j.PRFeedback.Entries[0].AssessmentHistory) != 0 {
		t.Fatal("completed human assessment rewritten")
	}
}
func TestIndependentRepairReplayRejectsUncorroboratedReceipts(t *testing.T) {
	for _, kind := range []string{"fingerprint", "unknown", "provisional", "no-review", "commit", "dirty"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, _, provider := independentRepairReceiptFixture(t)
			switch kind {
			case "fingerprint":
				j.Outcome.PRFeedbackAssessments[0].Fingerprint = strings.Repeat("e", 64)
			case "unknown":
				j.Outcome.PRFeedbackAssessments[0].ID = "comment:other"
			case "provisional":
				j.PRFeedback.Entries[0].Provisional = true
			case "no-review":
				j.ReviewAssessment = nil
			case "commit":
				j.Outcome.PRFeedbackAssessments[0].Commits = []string{strings.Repeat("e", 40)}
			case "dirty":
				r.Repository = feedbackHistoryRepository{fakeRepository: &fakeRepository{revisions: []Revision{{Head: testSigned, Tree: testBase, Clean: false}}}, commits: []string{testSigned}}
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("unsafe replay admitted")
			}
			if len(provider.calls) != 0 {
				t.Fatal("unsafe replay launched model")
			}
		})
	}
}
func TestImplementationSchemaOnlyPermitsPendingHumanReceipts(t *testing.T) {
	_, j, _, _, _ := independentRepairReceiptFixture(t)
	var s map[string]any
	json.Unmarshal([]byte(implementationSchemaForJournal(j)), &s)
	field := s["properties"].(map[string]any)["pr_feedback_assessments"].(map[string]any)
	if field["maxItems"] != float64(0) {
		t.Fatal("completed receipts allowed")
	}
	j.PRFeedback.Entries[0].Assessment = nil
	json.Unmarshal([]byte(implementationSchemaForJournal(j)), &s)
	allowed := s["properties"].(map[string]any)["pr_feedback_assessments"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["enum"].([]any)
	if len(allowed) != 1 || allowed[0] != j.PRFeedback.Entries[0].Comment.ID {
		t.Fatal("schema permits non-pending comment IDs")
	}
	if s["properties"].(map[string]any)["pr_feedback_assessments"].(map[string]any)["maxItems"] == float64(0) {
		t.Fatal("pending receipts disallowed")
	}
}
