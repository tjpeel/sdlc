package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func savedFeedbackReplayFixture(t *testing.T) (string, *Journal, Runner, *supplementaryChecker, *fakeProvider) {
	t.Helper()
	directory, j, runner, _, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Inspect related source")
	old := assessedOutcome(comment, "unaddressable").PRFeedbackAssessments[0]
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), StartedAttempt: 24, Assessment: &old}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	entry.ReplyBody = old.Reason
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	j.Attempt = 25
	j.State, j.ResumeState, j.StopReason = "blocked", "implementing", noPendingFeedbackHold
	j.Outcome = assessedOutcome(comment, "addressed")
	j.Outcome.Status = "checks_requested"
	j.Outcome.PRFeedbackAssessments[0].Commits = []string{testSigned}
	j.Outcome.VerificationRequests = []VerificationRequest{{ID: "correction", Purpose: "verify response", Commands: [][]string{{"go", "test", "./example"}}}}
	overrideGrant(j, "native_turns", 24)
	checker := &supplementaryChecker{}
	provider := &fakeProvider{}
	runner.Provider = provider
	runner.Checker = checker
	runner.Repository = feedbackHistoryRepository{fakeRepository: &fakeRepository{revisions: []Revision{{Head: testSigned, Tree: testBase, Clean: true}}}, commits: []string{testSigned}}
	if err := Save(directory, j); err != nil {
		t.Fatal(err)
	}
	return directory, j, runner, checker, provider
}

func TestSavedFeedbackOutcomeReplayChecksBeforeAnotherNativeTurn(t *testing.T) {
	directory, j, runner, checker, provider := savedFeedbackReplayFixture(t)
	session, attempt, rounds, grant := j.SessionID, j.Attempt, j.Rounds, *j.RunLimitOverride
	completed := errors.New("saved correction verified")
	runner.OnState = func(saved Journal) error {
		if saved.State == "implementing" && checker.calls == 1 && len(checker.requests) == 1 {
			return completed
		}
		return nil
	}
	if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, completed) {
		t.Fatal("saved outcome did not reach machine checks", err)
	}
	if len(provider.calls) != 0 || j.Attempt != attempt || j.Rounds != rounds || j.SessionID != session || !reflect.DeepEqual(grant, *j.RunLimitOverride) || !j.Evidence.Passed || !j.Verification[0].Passed {
		t.Fatal("replay consumed native capacity or reset saved evidence/accounting")
	}
	if len(j.PRFeedback.Entries) != 1 || len(j.PRFeedback.Entries[0].AssessmentHistory) != 1 || j.PRFeedback.Entries[0].AssessmentHistory[0].Disposition != "unaddressable" || !j.PRFeedback.Entries[0].Provisional || j.PRFeedback.Entries[0].Reply != nil {
		t.Fatal("replay discarded prior assessment or prematurely replied")
	}
}

func TestSavedFeedbackReplayRejectsUncorroboratedRevisionAndCommits(t *testing.T) {
	for _, scenario := range []string{"stale", "replied", "deleted", "resolved", "out-of-order", "commit"} {
		t.Run(scenario, func(t *testing.T) {
			directory, j, runner, _, provider := savedFeedbackReplayFixture(t)
			switch scenario {
			case "stale":
				j.Outcome.PRFeedbackAssessments[0].Fingerprint = strings.Repeat("d", 64)
			case "replied":
				j.PRFeedback.Entries[0].Reply = &PRFeedbackReply{ID: "reply", URL: "https://example.invalid/reply"}
			case "deleted":
				j.PRFeedback.Entries[0].Comment.Deleted = true
				j.PRFeedback.Entries[0].Fingerprint = feedbackFingerprint(j.PRFeedback.Entries[0].Comment)
				j.PRFeedback.Entries[0].Assessment.Fingerprint = j.PRFeedback.Entries[0].Fingerprint
				j.PRFeedback.Entries[0].Marker = feedbackEntryMarker(j.ID, j.PRFeedback.Entries[0])
				j.Outcome.PRFeedbackAssessments[0].Fingerprint = j.PRFeedback.Entries[0].Fingerprint
			case "resolved":
				j.PRFeedback.Entries[0].Comment.Resolved = true
				j.PRFeedback.Entries[0].Fingerprint = feedbackFingerprint(j.PRFeedback.Entries[0].Comment)
				j.PRFeedback.Entries[0].Assessment.Fingerprint = j.PRFeedback.Entries[0].Fingerprint
				j.PRFeedback.Entries[0].Marker = feedbackEntryMarker(j.ID, j.PRFeedback.Entries[0])
				j.Outcome.PRFeedbackAssessments[0].Fingerprint = j.PRFeedback.Entries[0].Fingerprint
			case "out-of-order":
				other := generalFeedback("comment:2", "Current pending revision")
				j.PRFeedback.Entries = append(j.PRFeedback.Entries, PRFeedbackEntry{Comment: other, Fingerprint: feedbackFingerprint(other)})
			case "commit":
				j.Outcome.PRFeedbackAssessments[0].Commits = []string{testBase}
			}
			if err := Save(directory, j); err != nil {
				t.Fatal(err)
			}
			old := *j.PRFeedback.Entries[0].Assessment
			if err := runner.Run(context.Background(), directory, j, ""); err == nil {
				t.Fatal("unsafe saved replay admitted")
			}
			if len(provider.calls) != 0 || !reflect.DeepEqual(old, *j.PRFeedback.Entries[0].Assessment) || j.Attempt != 25 {
				t.Fatal("invalid replay dispatched native work or overwrote history")
			}
		})
	}
}
