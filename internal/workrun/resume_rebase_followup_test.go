package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func acceptedFollowupRebaseFixture(t *testing.T) (string, *Journal, *resumeTestPublisher, *resumeTestRepository) {
	t.Helper()
	dir, j, p, r := answeredFeedbackRebaseFixture(t)
	j.StopReason = invalidImplementationHandoff
	prior := generalFeedback("comment:old", "Previously unresolved")
	prior.Kind = "local"
	prior.Author = "local"
	prior.ID = localFeedbackID(prior.Body)
	a := assessedOutcome(prior, "unaddressable").PRFeedbackAssessments[0]
	e := PRFeedbackEntry{Comment: prior, Fingerprint: feedbackFingerprint(prior), Assessment: &a, ReplyBody: a.Reason, Reply: &PRFeedbackReply{ID: "local-reply"}}
	e.Marker = feedbackEntryMarker(j.ID, e)
	j.PRFeedback.Entries = append([]PRFeedbackEntry{e}, j.PRFeedback.Entries...)
	pending := &j.PRFeedback.Entries[1]
	pending.Comment.Kind = "local"
	pending.Comment.Author = "local"
	pending.Comment.ID = localFeedbackID(pending.Comment.Body)
	pending.Fingerprint = feedbackFingerprint(pending.Comment)
	pending.Assessment.ID = pending.Comment.ID
	pending.Assessment.Fingerprint = pending.Fingerprint
	pending.Marker = feedbackEntryMarker(j.ID, *pending)
	j.Outcome.PRFeedbackAssessments = []PRFeedbackAssessment{*pending.Assessment}
	j.Evidence.Commands = j.Plan.Checks
	now := time.Now().UTC()
	j.ImplementationFollowups = []ImplementationFollowup{{At: now, Checkpoint: now, AfterAttempt: j.Attempt, Candidate: Revision{Head: j.Evidence.Head, Tree: j.Evidence.Tree, Clean: true}, Publication: j.Publication, Text: "Reconciliation remains outstanding before rollout.", HumanAnswer: answeredImplementationQuestion(*j)}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j, p, r
}
func TestResumeRebaseAcceptedFollowupPreservesAnswerAndPendingFeedback(t *testing.T) {
	dir, j, p, r := acceptedFollowupRebaseFixture(t)
	if s := PRFeedbackStatus(*j); s.Pending != 1 || s.Unresolved != 1 || s.OutstandingReplies != 0 {
		t.Fatalf("wrong fixture: %+v", s)
	}
	if !rebaseAcceptedFollowupFeedback(j) {
		t.Fatal("eligible follow-up rejected")
	}
	beforeAnswer, beforeOutcome, beforeHistory, beforeSession := j.Feedback, j.Outcome, *j.PRFeedback, j.SessionID
	changed, err := (Runner{Publisher: p, Repository: r, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("rebase: %v", err)
	}
	f := j.ImplementationFollowups[0]
	review := implementationFollowupContext(*j, "review")
	if !strings.Contains(review, "Use the separate follow-up.") || !strings.Contains(review, f.Text) || !strings.Contains(review, "not review acceptance") || reusableReport(j) != nil || reviewReuseCurrent(j) {
		t.Fatal("independent review lost human decision or reused earlier review")
	}
	if f.Rebase == nil || f.Rebase.Source != f.Candidate || f.Rebase.Target != testTree || !f.Rebase.Candidate.Clean || !j.RestackNeedsChecks || j.Evidence.Passed || j.CI.Status != "" {
		t.Fatal("rebased follow-up did not force fresh checks")
	}
	beforeHistory.ReplayedCommits = j.PRFeedback.ReplayedCommits
	if !strings.Contains(j.Feedback, beforeAnswer) || !reflect.DeepEqual(beforeOutcome, j.Outcome) || !reflect.DeepEqual(beforeHistory, *j.PRFeedback) || j.SessionID != beforeSession || !j.PRFeedback.Active {
		t.Fatal("human answer, feedback or session lost")
	}
	saved, err := Load(dir)
	if err != nil || saved.ImplementationFollowups[0].Rebase == nil {
		t.Fatalf("rebase receipt not durable: %v", err)
	}
}
func TestResumeRebaseAcceptedFollowupRetainsConflictAndInterruptedCheckpoint(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{true: "conflict", false: "interruption"}[conflict], func(t *testing.T) {
			dir, j, p, r := acceptedFollowupRebaseFixture(t)
			before := j.Feedback
			r.conflict = conflict
			r.fail = !conflict
			if _, err := (Runner{Publisher: p, Repository: r, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil {
				t.Fatal("unsafe replay succeeded")
			}
			saved, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !saved.ResumeRebase || saved.Reconciliation == nil || saved.Feedback != before || !saved.PRFeedback.Active || saved.ImplementationFollowups[0].Rebase != nil || !saved.Evidence.Passed {
				t.Fatal("lost pending checkpoint or acceptance")
			}
			if conflict {
				if saved.Reconciliation.State != "conflict" {
					t.Fatal("conflict lost")
				}
				return
			}
			r.fail = false
			if changed, err := (Runner{Publisher: p, Repository: r}).checkResumeUpstream(context.Background(), dir, &saved); err != nil || !changed || saved.ImplementationFollowups[0].Rebase == nil {
				t.Fatalf("interrupted replay failed to resume: %v", err)
			}
		})
	}
}
func TestResumeRebaseAcceptedFollowupRejectsChangedAndUnrelatedState(t *testing.T) {
	cases := []func(*Journal){
		func(j *Journal) { j.StopReason = "provider usage limit" }, func(j *Journal) { j.ImplementationFollowups[0].HumanAnswer = "different" }, func(j *Journal) { j.Evidence.Tree = testBase }, func(j *Journal) { j.ImplementationFollowups[0].FirstAcceptedAttempt = j.Attempt }, func(j *Journal) { j.Outcome.PRFeedbackAssessments = nil }, func(j *Journal) { j.PRFeedback.Entries[1].Comment.Kind = "inline" }, func(j *Journal) { j.PRFeedback.Entries[0].Reply = nil },
	}
	for _, change := range cases {
		_, j, _, _ := acceptedFollowupRebaseFixture(t)
		change(j)
		if rebaseAcceptedFollowupFeedback(j) {
			t.Fatal("unsafe boundary allowed")
		}
	}
}

func TestRebasedFollowupRequiresFreshEvidenceAndRecordedCandidate(t *testing.T) {
	dir, j, p, repo := acceptedFollowupRebaseFixture(t)
	if _, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	f := j.ImplementationFollowups[0]
	out := testOutcome("implemented")
	out.Limitations = []string{f.Text}
	out.PRBody += "\n" + f.Text
	r := Runner{Repository: repo}
	if r.validateImplementationFollowupOutcome(context.Background(), j, out) == nil {
		t.Fatal("rebased candidate accepted without fresh checks")
	}
	j.Evidence = CheckEvidence{Passed: true, Head: f.Rebase.Candidate.Head, Tree: f.Rebase.Candidate.Tree, Commands: j.Plan.Checks}
	if err := r.validateImplementationFollowupOutcome(context.Background(), j, out); err != nil {
		t.Fatal(err)
	}
	j.Plan.BaseSHA = testBase
	if r.validateImplementationFollowupOutcome(context.Background(), j, out) == nil {
		t.Fatal("changed rebase target accepted")
	}
	j.Plan.BaseSHA = f.Rebase.Target
	j.ImplementationFollowups[0].Rebase.Candidate.Tree = testBase
	if r.validateImplementationFollowupOutcome(context.Background(), j, out) == nil {
		t.Fatal("changed rebased candidate accepted")
	}
}

func TestResumeRebaseAcceptedFollowupDispatchesSameSession(t *testing.T) {
	dir, j, p, repo := acceptedFollowupRebaseFixture(t)
	p.Publisher = &fakePublisher{}
	repo.Repository = &fakeRepository{}
	native := &fakeProvider{executeErr: errors.New("intentional offline stop")}
	r := fakeRunner(native, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Publisher = p
	r.Repository = repo
	r.Rebase = true
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected offline stop")
	}
	if len(native.calls) != 1 || native.calls[0].ResumeID != testNative || !strings.Contains(native.calls[0].Prompt, "Use the separate follow-up.") || !strings.Contains(native.calls[0].Prompt, "Reconciliation remains outstanding before rollout.") || !strings.Contains(native.calls[0].Prompt, "Pending feedback") {
		t.Fatal("rebase lost session, answer, follow-up or feedback")
	}
	if j.Evidence.Passed || !j.RestackNeedsChecks || j.ImplementationFollowups[0].Rebase == nil {
		t.Fatal("rebased session bypassed checks")
	}
}
