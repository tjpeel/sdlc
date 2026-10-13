package workrun

import (
	"context"
	"errors"
	"io"
	"testing"
)

type provisionalFeedbackProvider struct {
	feedbackScriptProvider
	before func(int, Session)
}

func (p *provisionalFeedbackProvider) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	if p.before != nil {
		p.before(len(p.sessions), s)
	}
	return p.feedbackScriptProvider.Execute(ctx, s, out, diag)
}

func TestPRFeedbackSourceThenCorrectionKeepsRevisionPendingUntilFinalized(t *testing.T) {
	directory, j, runner, publisher, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Inspect related source before correcting response")
	publisher.comments = []PRFeedbackComment{comment}
	initialPublications := publisher.calls
	initial := assessedOutcome(comment, "unaddressable")
	initial.Status = "checks_requested"
	initial.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example/companion", Purpose: "inspect response contract"}}
	corrected := assessedOutcome(comment, "addressed")
	corrected.Status = "checks_requested"
	corrected.PRFeedbackAssessments[0].Commits = []string{testSigned}
	corrected.VerificationRequests = []VerificationRequest{{ID: "correction", Purpose: "verify corrected response", Commands: [][]string{{"go", "test", "./example"}}}}
	finalized := corrected
	finalized.Status = "implemented"
	finalized.VerificationRequests = nil
	checker := &supplementaryChecker{}
	repository := &fakeRepository{}
	runner.Repository = feedbackHistoryRepository{fakeRepository: repository, commits: []string{testSigned}}
	runner.Checker = checker
	cloner := &fakeCompanionCloner{revision: testBase}
	runner.CompanionCloner = cloner
	provider := &provisionalFeedbackProvider{feedbackScriptProvider: feedbackScriptProvider{outcomes: []Outcome{initial, corrected, finalized}}}
	provider.before = func(turn int, s Session) {
		if s.ResumeID != j.SessionID {
			t.Fatal("feedback replaced native session")
		}
		if turn == 1 {
			if checker.calls != 1 || len(cloner.calls) != 1 || PRFeedbackStatus(*j).Pending != 1 || publisher.replies != 0 {
				t.Error("source request advanced revision or skipped checks")
			}
			repository.revisions = []Revision{{Head: testSigned, Tree: testBase, Clean: true}}
		}
		if turn == 2 {
			if checker.calls != 2 || len(checker.requests) != 1 || PRFeedbackStatus(*j).Pending != 1 || publisher.replies != 0 {
				t.Fatal("correction finalized before requested checks")
			}
		}
	}
	runner.Provider = provider
	runner.ProcessPRFeedback = true
	completed := errors.New("stop at verified publication gate")
	runner.OnState = func(saved Journal) error {
		if saved.State == "publishing" {
			return completed
		}
		return nil
	}
	if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, completed) {
		t.Fatal("same revision refinement failed", err)
	}
	entry := j.PRFeedback.Entries[0]
	if len(provider.sessions) != 3 || len(j.PRFeedback.Entries) != 1 || entry.Assessment == nil || entry.Assessment.Disposition != "addressed" || PRFeedbackStatus(*j).Pending != 0 || publisher.replies != 0 || publisher.calls != initialPublications || entry.Comment.Resolved {
		t.Fatal("feedback finalization lost revision or bypassed publication/author review gates")
	}
}

func TestPRFeedbackHumanAnswerRetainsSameProvisionalRevision(t *testing.T) {
	directory, j, runner, publisher, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Choose the documented response contract")
	publisher.comments = []PRFeedbackComment{comment}
	question := assessedOutcome(comment, "no_change")
	question.Status = "waiting_for_human"
	question.Questions = []string{"Which documented response should be used?"}
	if err := validateOutcome(question, "implementation"); err != nil {
		t.Fatal("question with feedback assessment is not schema-valid", err)
	}
	finalized := assessedOutcome(comment, "no_change")
	provider := &feedbackScriptProvider{outcomes: []Outcome{question, finalized, testOutcome("implemented"), testOutcome("reviewed")}}
	runner.Provider = provider
	runner.ProcessPRFeedback = true
	if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal("human question did not stop controller", err)
	}
	if j.State != "waiting_for_human" || PRFeedbackStatus(*j).Pending != 1 || publisher.replies != 0 || len(provider.sessions) != 1 {
		t.Fatal("human question finalized or replied to feedback")
	}
	runner.ProcessPRFeedback = false
	if err := runner.Run(context.Background(), directory, j, "Use the documented response."); err != nil {
		t.Fatal("same revision could not resume after answer", err)
	}
	if j.State != "ready" || len(j.PRFeedback.Entries) != 1 || publisher.replies != 1 || j.PRFeedback.Entries[0].Comment.Resolved || provider.sessions[1].ResumeID != testNative || provider.sessions[len(provider.sessions)-1].Role != "review" {
		t.Fatal("answer lost original revision/session or bypassed author-owned review")
	}
}
