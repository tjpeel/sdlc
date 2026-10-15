package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func checkpointFixture(t *testing.T, mode string) (string, *Journal, Runner, *localFeedbackPublisher, *feedbackScriptProvider, *fakeChecker) {
	t.Helper()
	dir, j := testRun(t)
	a := adoptionBoundary()
	j.Plan.Adoption = &a
	j.Plan.SourceSHA, j.Plan.StartingSHA = testHead, testHead
	j.Plan.PRTitle, j.Plan.PRBody = a.Title, a.Body
	j.Plan.PublicationIdentity = frozenTestIdentity()
	j.State = "adopting"
	j.Publication = Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
	pub := &localFeedbackPublisher{feedbackPublisherFake: &feedbackPublisherFake{fakePublisher: &fakePublisher{}}}
	provider := &feedbackScriptProvider{}
	checker := &fakeChecker{}
	r := fakeRunner(&fakeProvider{}, checker, pub.fakePublisher, &fakeRepository{})
	r.Publisher, r.Provider, r.StartAt = pub, provider, mode
	return dir, j, r, pub, provider, checker
}

func TestAwaitingFeedbackAdoptionDoesNotRunProvidersChecksOrPublish(t *testing.T) {
	dir, j, r, pub, provider, checker := checkpointFixture(t, "awaiting-feedback")
	r.HumanReviewed = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || j.SessionID != "" || len(provider.sessions) != 0 || checker.calls != 0 || pub.calls != 0 || pub.reads != 0 || j.Evidence.Passed || j.CI.Status == "passed" || j.Outcome.Status == "reviewed" {
		t.Fatal("parking fabricated or executed a gate", j.State)
	}
	c := latestPRCheckpoint(j)
	if c.HumanReviewedAt == nil || c.Head != testHead || c.Tree != testTree || !c.Completed {
		t.Fatal("human declaration is not bound to the PR", c)
	}
	saved, err := Load(dir)
	if err != nil || saved.State != "awaiting_feedback" {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, c.Archive))
	if err != nil || !strings.Contains(string(backup), `"state":"adopting"`) {
		t.Fatal("prior checkpoint was not archived", err)
	}
	if err := ValidatePRFeedbackResume(*j); err != nil {
		t.Fatal("parked run cannot accept feedback", err)
	}
	// Ordinary resume and an empty remote pass remain parked without creating sessions.
	r.StartAt = ""
	r.HumanReviewed = false
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.sessions) != 0 || checker.calls != 0 {
		t.Fatal("empty feedback pass started engineering work")
	}
}

func TestParkedAdoptionFeedbackStartsSessionAndRetainsNormalGates(t *testing.T) {
	dir, j, r, pub, provider, checker := checkpointFixture(t, "awaiting-feedback")
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	comment := generalFeedback("comment:1", "Check the API contract")
	pub.comments = []PRFeedbackComment{comment}
	provider.outcomes = []Outcome{assessedOutcome(comment, "no_change"), testOutcome("implemented"), testOutcome("reviewed")}
	r.StartAt = ""
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(provider.sessions) != 3 || provider.sessions[0].ResumeID != "" || j.SessionID == "" || checker.calls != 1 || pub.calls != 1 || pub.replies != 1 || provider.sessions[2].Role != "review" {
		t.Fatal("feedback bypassed normal gates", j.State, len(provider.sessions), checker.calls, pub.calls)
	}
}

func TestCheckpointChecksAndReviewDoNotImplementOrPublish(t *testing.T) {
	for _, mode := range []string{"checks", "review"} {
		t.Run(mode, func(t *testing.T) {
			dir, j, r, pub, provider, checker := checkpointFixture(t, mode)
			if mode == "review" {
				provider.outcomes = []Outcome{testOutcome("reviewed")}
			}
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.State != "awaiting_feedback" || j.SessionID != "" || pub.calls != 0 || pub.replies != 0 || pub.reads != 0 || j.CI.Status == "passed" {
				t.Fatal("standalone stage crossed into implementation/publication", j.State)
			}
			if mode == "checks" && (checker.calls != 1 || !j.Evidence.Passed || len(provider.sessions) != 0 || j.Outcome.Status == "reviewed") {
				t.Fatal("checks-only contract lost")
			}
			if mode == "review" && (checker.calls != 0 || j.Evidence.Passed || len(provider.sessions) != 1 || provider.sessions[0].Role != "review" || latestPRCheckpoint(j).ReviewOutcome == nil) {
				t.Fatal("review-only contract lost")
			}
		})
	}
}

func TestPRCheckpointRecoveryArchivesRejectedOutcomeWithoutResettingUsage(t *testing.T) {
	for _, reason := range []string{"claimed feedback commit is absent from candidate history", noPendingFeedbackHold} {
		t.Run(reason, func(t *testing.T) {
			dir, j, r, pub, provider, checker := checkpointFixture(t, "awaiting-feedback")
			j.State, j.ResumeState, j.StopReason = "blocked", "implementing", reason
			j.SessionID = testNative
			j.Attempt = 3
			j.Rounds = 1
			j.CheckAttempt = 4
			j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3}
			c := generalFeedback("comment:1", "Previous selection")
			if reason == noPendingFeedbackHold {
				c.Resolved = true
			}
			j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{{Comment: c, Fingerprint: feedbackFingerprint(c)}}}
			j.Outcome = testOutcome("checks_requested")
			if reason == noPendingFeedbackHold {
				j.Outcome = assessedOutcome(c, "no_change")
			}
			original := j.Outcome
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.State != "awaiting_feedback" || j.SessionID != testNative || j.Attempt != 3 || j.Rounds != 1 || j.CheckAttempt != 4 || j.ReviewRepairBudget.Limit != 3 || j.PRFeedback != nil || len(provider.sessions) != 0 || checker.calls != 0 || pub.calls != 0 {
				t.Fatal("recovery lost accounting or dispatched work")
			}
			archiveDir := filepath.Dir(filepath.Join(dir, latestPRCheckpoint(j).Archive))
			archive, err := loadCheckpointArchiveForTest(archiveDir, filepath.Base(latestPRCheckpoint(j).Archive))
			if err != nil || !reflect.DeepEqual(archive.Outcome, original) {
				t.Fatal("rejected outcome not retained", err)
			}
		})
	}
}

func loadCheckpointArchiveForTest(dir, name string) (Journal, error) {
	var j Journal
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(data, &j)
	return j, err
}

func TestPRCheckpointRejectsOtherHoldsAndChangedCandidates(t *testing.T) {
	for _, reason := range []string{"provider usage exhausted", "Actions job log API could not be read", "independent review is incomplete"} {
		_, j, _, _, _, _ := checkpointFixture(t, "awaiting-feedback")
		j.State, j.ResumeState, j.StopReason = "blocked", "implementing", reason
		if ValidatePRCheckpointResume(*j) == nil {
			t.Fatal("unrelated hold bypassed", reason)
		}
	}
	dir, j, r, pub, provider, checker := checkpointFixture(t, "awaiting-feedback")
	r.Repository = &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}}
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("changed candidate parked")
	}
	if len(j.PRCheckpoints) != 0 || len(provider.sessions) != 0 || checker.calls != 0 || pub.boundaries != 0 {
		t.Fatal("changed candidate mutated or dispatched")
	}
}

func TestParkedFeedbackFetchFailureRecoversWithoutLosingParkedBoundary(t *testing.T) {
	dir, j, r, pub, provider, _ := checkpointFixture(t, "awaiting-feedback")
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	r.StartAt = ""
	r.ProcessPRFeedback = true
	pub.readErr = errors.New("offline read failure")
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if err := ValidatePRFeedbackResume(*j); err != nil {
		t.Fatal(err)
	}
	r.ProcessPRFeedback = false
	pub.readErr = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.sessions) != 0 {
		t.Fatal("empty recovered pass certified or dispatched")
	}
}

func TestResolvedFeedbackAcknowledgementDoesNotBlockDefaultAdoption(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(map[bool]string{false: "harmless acknowledgement", true: "correction claim"}[claim], func(t *testing.T) {
			dir, j, r, pub, provider, checks := checkpointFixture(t, "")
			comment := generalFeedback("comment:1", "Unique resolved comment body")
			comment.Resolved = true
			pub.comments = []PRFeedbackComment{comment}
			out := assessedOutcome(comment, "no_change")
			if claim {
				out.PRFeedbackAssessments[0].Disposition = "addressed"
				out.PRFeedbackAssessments[0].Commits = []string{testHead}
			}
			provider.outcomes = []Outcome{out, testOutcome("implemented"), testOutcome("reviewed")}
			err := r.Run(context.Background(), dir, j, "")
			if claim {
				if !errors.Is(err, ErrStopped) || j.StopReason != noPendingFeedbackHold {
					t.Fatal("inactive correction claim was accepted", err)
				}
				return
			}
			if err != nil || j.State != "ready" || checks.calls != 1 || pub.replies != 0 {
				t.Fatal("resolved acknowledgement blocked adoption", j.State, err)
			}
			for _, session := range provider.sessions {
				if strings.Contains(session.Prompt, comment.Body) {
					t.Fatal("resolved body reached provider prompt")
				}
			}
		})
	}
}

func TestParkedRunAcceptsPrivateFileFeedbackWithoutPriorSession(t *testing.T) {
	dir, j, r, _, _, _ := checkpointFixture(t, "awaiting-feedback")
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	provider := &localAssessmentProvider{j: j}
	r.Provider = provider
	r.StartAt = ""
	r.ProcessPRFeedback = true
	r.LocalPRFeedback = "Review the response handling."
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(provider.sessions) != 3 || provider.sessions[0].ResumeID != "" || j.PRFeedback.Entries[0].Comment.Kind != "local" {
		t.Fatal("private feedback did not start the saved PR workflow")
	}
}

func TestStandaloneReviewRetainsFindingsWithoutStartingRepair(t *testing.T) {
	dir, j, r, pub, provider, checks := checkpointFixture(t, "review")
	out := testOutcome("reviewed")
	out.Findings = []Finding{{Priority: "P2", Path: "api.go", Line: 1, Scenario: "The request fails for an empty value.", Recommendation: "Validate the value."}}
	provider.outcomes = []Outcome{out}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.sessions) != 1 || checks.calls != 0 || pub.calls != 0 || len(latestPRCheckpoint(j).ReviewOutcome.Findings) != 1 {
		t.Fatal("standalone findings triggered engineering or disappeared")
	}
}

func TestStandaloneReviewHumanAnswerRemainsReviewOnly(t *testing.T) {
	dir, j, r, pub, provider, checks := checkpointFixture(t, "review")
	question := testOutcome("waiting_for_human")
	question.Questions = []string{"Which contract applies?"}
	provider.outcomes = []Outcome{question, testOutcome("reviewed")}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "waiting_for_human" {
		t.Fatal(j.State, err)
	}
	r.StartAt = ""
	if err := r.Run(context.Background(), dir, j, "The documented public contract applies."); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.sessions) != 2 || provider.sessions[1].Role != "review" || j.SessionID != "" || pub.calls != 0 || checks.calls != 0 {
		t.Fatal("review answer started implementation")
	}
}

func TestPrepareStandaloneReviewUsesCapturedSourceWithoutPublicationCycle(t *testing.T) {
	root, _ := sourceFixture(t)
	ctx := context.Background()
	raw, err := SafeGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(raw))
	raw, err = SafeGit(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(string(raw))
	dir := realTemp(t)
	j := Journal{Workspace: root, Plan: Plan{Repository: "example/project", PublicationIdentity: frozenTestIdentity()}, Publication: Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: head, BaseSHA: head}, PRCheckpoints: []PRCheckpoint{{StartAt: "review", Head: head, Tree: tree}}}
	// A standalone entry must not depend on a prior publisher checkout.
	if err := os.MkdirAll(filepath.Join(dir, "publisher", "publication.git"), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := PrepareReview(ctx, j, dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = isolatedGit(ctx, snapshot, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(raw)) != head {
		t.Fatal("review snapshot differs from captured published head", err)
	}
	if snapshot == root || j.Evidence.Passed || j.SessionID != "" {
		t.Fatal("review preparation certified verification or reused implementation source")
	}
}
