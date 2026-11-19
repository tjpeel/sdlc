package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResumeRebaseDispatchesRecordedAnswerToSameSession(t *testing.T) {
	dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
	pub.Publisher = &fakePublisher{}
	repo.Repository = &fakeRepository{}
	p := &fakeProvider{executeErr: errors.New("intentional stop")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Publisher, r.Repository, r.Rebase = pub, repo, true
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("provider should stop")
	}
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || !strings.Contains(p.calls[0].Prompt, "Use the separate follow-up.") || !strings.Contains(p.calls[0].Prompt, "Pending feedback") {
		t.Fatalf("recorded answer and feedback did not reach the original session: %d calls", len(p.calls))
	}
}

func answeredFeedbackRebaseFixture(t *testing.T) (string, *Journal, *resumeTestPublisher, *resumeTestRepository) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", "native codex headless session failed; inspect the private run log before retrying"
	j.Plan.Base = "parent"
	j.Publication = Publication{Number: 40, URL: "https://github.com/example/project/pull/40", HeadSHA: testHead, BaseSHA: testBase}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	j.CI = CIResult{Status: "passed"}
	j.SessionID, j.PendingRole = testNative, "implementation"
	j.Attempt, j.Rounds = 4, 2
	j.Outcome = Outcome{Status: "waiting_for_human", Questions: []string{"Which option?"}}
	j.Feedback = "Recorded human questions:\nWhich option?\nHuman answer:\nUse the separate follow-up.\nIgnored PR comment IDs (do not act on these or earlier transcript versions of them): []"
	comment := generalFeedback("comment:1", "Pending feedback")
	assessment := assessedOutcome(comment, "unaddressable").PRFeedbackAssessments[0]
	j.Outcome.PRFeedbackAssessments = []PRFeedbackAssessment{assessment}
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), StartedAttempt: 4, Assessment: &assessment, Provisional: true}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	entry.ReplyBody = assessment.Reason
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3, Used: 1, StartRound: 0, LastGrantedHead: testHead}
	j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Findings: reviewedFinding("P2").Findings, Summary: "Owner accepted after assessment.", StartedAttempt: 3, Acknowledged: true}
	pub := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	repo := &resumeTestRepository{}
	return dir, j, pub, repo
}

func TestResumeRebasePreservesAnsweredPendingFeedback(t *testing.T) {
	dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
	beforeFeedback, beforeOutcome, beforeHistory, beforeAssessment := j.Feedback, j.Outcome, *j.PRFeedback, *j.ReviewAssessment
	if !rebaseAnsweredFeedback(j) {
		t.Fatal("answered feedback fixture was not recognised")
	}
	changed, err := (Runner{Publisher: pub, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("cannot rebase answered feedback: %v", err)
	}
	if j.State != "implementing" || j.Evidence.Passed || !j.RestackNeedsChecks || j.RebaseMonitoring != nil || j.Plan.BaseSHA != testTree {
		t.Fatal("rebase skipped fresh implementation or verification")
	}
	if !strings.Contains(j.Feedback, beforeFeedback) || !strings.Contains(j.Feedback, "controller performed a Git rebase") || !reflect.DeepEqual(j.Outcome, beforeOutcome) || j.PendingRole != "implementation" || j.SessionID != testNative {
		t.Fatal("human answer or implementation session lost")
	}
	beforeHistory.ReplayedCommits = j.PRFeedback.ReplayedCommits
	if !reflect.DeepEqual(*j.PRFeedback, beforeHistory) || !reflect.DeepEqual(*j.ReviewAssessment, beforeAssessment) {
		t.Fatal("feedback or review decision lost")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Feedback, beforeFeedback) || !saved.PRFeedback.Active || !saved.RestackNeedsChecks {
		t.Fatal("recovery state was not persisted")
	}
}

func TestResumeRebaseRejectsUnsafeAnsweredFeedback(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Journal)
	}{
		{"unanswered", func(j *Journal) { j.Feedback = "Recorded human questions:\nWhich option?\nHuman answer:\n" }},
		{"blank before context", func(j *Journal) {
			j.Feedback = "Recorded human questions:\nWhich option?\nHuman answer:\n \nIgnored PR comment IDs: []"
		}},
		{"different question", func(j *Journal) { j.Outcome.Questions = []string{"Other?"} }},
		{"reviewer question", func(j *Journal) { j.PendingRole = "review" }},
		{"missing session", func(j *Journal) { j.SessionID = "" }},
		{"missing evidence", func(j *Journal) { j.Evidence.Passed = false }},
		{"pending source", func(j *Journal) { j.CandidatePhasePending = true }},
		{"unsaved response", func(j *Journal) { j.State = "waiting_for_human" }},
		{"unrelated failure", func(j *Journal) { j.StopReason = "repository failure" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
			tc.mutate(j)
			if rebaseAnsweredFeedback(j) {
				t.Fatal("unsafe state admitted")
			}
			if changed, err := (Runner{Publisher: pub, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil || changed || repo.calls != 0 {
				t.Fatal("unsafe state rebased", err)
			}
		})
	}
}

func TestResumeRebaseAnsweredFeedbackRequiresUnchangedCandidateAndPR(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*resumeTestPublisher, *resumeTestRepository, *Journal)
	}{
		{"dirty candidate", func(_ *resumeTestPublisher, r *resumeTestRepository, _ *Journal) { r.dirty = true }},
		{"changed tree", func(_ *resumeTestPublisher, _ *resumeTestRepository, j *Journal) { j.Evidence.Tree = testBase }},
		{"changed PR", func(p *resumeTestPublisher, _ *resumeTestRepository, _ *Journal) { p.remote.HeadSHA = testBase }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
			tc.change(pub, repo, j)
			if changed, err := (Runner{Publisher: pub, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil || changed || repo.calls != 0 {
				t.Fatal("changed boundary accepted", err)
			}
		})
	}
}

func TestResumeRebaseAnsweredFeedbackRetainsInterruptedCheckpoint(t *testing.T) {
	dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
	answer := j.Feedback
	repo.fail = true
	r := Runner{Publisher: pub, Repository: repo, Rebase: true}
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("expected interrupted replay")
	}
	if j.Reconciliation == nil || !j.ResumeRebase || j.Feedback != answer || !j.PRFeedback.Active {
		t.Fatal("interrupted replay lost the answer or feedback")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	repo.fail = false
	r.Rebase = false
	if changed, err := r.checkResumeUpstream(context.Background(), dir, &loaded); err != nil || !changed {
		t.Fatalf("cannot recover interrupted replay: %v", err)
	}
	if !strings.Contains(loaded.Feedback, answer) || !loaded.PRFeedback.Active || loaded.Reconciliation != nil {
		t.Fatal("resumed replay lost the answer or feedback")
	}
}

func TestResumeRebaseAnsweredFeedbackRetainsConflict(t *testing.T) {
	dir, j, pub, repo := answeredFeedbackRebaseFixture(t)
	answer := j.Feedback
	repo.conflict = true
	if _, err := (Runner{Publisher: pub, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("conflicted replay was accepted")
	}
	if j.Reconciliation == nil || j.Reconciliation.State != "conflict" || j.Feedback != answer || !j.PRFeedback.Active || !j.Evidence.Passed {
		t.Fatal("conflict discarded feedback or evidence")
	}
}
