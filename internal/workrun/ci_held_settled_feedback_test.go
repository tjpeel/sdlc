package workrun

import (
	"context"
	"errors"
	"testing"
)

type replyingCIHeldPublisher struct {
	*ciHeldFeedbackPublisher
	replies int
}

func (p *replyingCIHeldPublisher) ReplyFeedback(_ context.Context, _ Plan, _ Publication, _ PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	p.replies++
	return PRFeedbackReply{ID: marker, URL: body}, nil
}

func TestCIHeldReviewAfterFeedbackAssessmentAndReplyWithFailedCI(t *testing.T) {
	dir, j, base, runner := ciHeldFeedbackFixture(t)
	j.Plan.Ticket = "01-example.md"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	comment := generalFeedback("comment:1", "Please verify the boundary")
	base.comments = []PRFeedbackComment{comment}
	base.statuses = []string{"failed"}
	base.heads = []string{testTree}
	pub := &replyingCIHeldPublisher{ciHeldFeedbackPublisher: base}
	runner.Publisher = pub
	if err := runner.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	corrected := assessedOutcome(comment, "addressed")
	corrected.PRFeedbackAssessments[0].Commits = []string{testSigned}
	implementer := &feedbackScriptProvider{outcomes: []Outcome{corrected, testOutcome("implemented")}}
	runner.Repository = feedbackHistoryRepository{fakeRepository: runner.Repository.(*fakeRepository), commits: []string{testSigned}}
	runner.Provider = implementer
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ci" || j.CI.Status != "failed" {
		t.Fatalf("feedback did not retain failed CI hold: %v, state=%s resume=%s ci=%s", err, j.State, j.ResumeState, j.CI.Status)
	}
	if j.Publication.HeadSHA != testTree || j.CIHeldReview == nil || !j.CIHeldReview.Invalidated {
		t.Fatal("changed published head did not invalidate its old review")
	}
	status := PRFeedbackStatus(*j)
	if !j.PRFeedback.Active || status.Pending != 0 || status.Unresolved != 0 || status.OutstandingReplies != 0 || pub.replies != 1 {
		t.Fatalf("feedback is not settled: %+v", status)
	}
	if err := ValidateCIHeldReviewStart(*j); err != nil {
		t.Fatalf("settled feedback blocked fresh review: %v", err)
	}
	reviewer := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	runner.Provider, runner.ReviewCIHold = reviewer, true
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("review: %v", err)
	}
	if j.State != "blocked" || j.ResumeState != "ci" || j.CI.Status != "failed" || len(reviewer.calls) != 1 || !j.PRFeedback.Active {
		t.Fatal("review changed CI, readiness or retained feedback cycle")
	}
	runner.ReviewCIHold = false
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("post-review scan: %v", err)
	}
	if j.PRFeedback.Active || j.PRFeedback.CIHeldScan == nil {
		t.Fatal("fresh scan did not settle feedback cycle")
	}
	if _, err := ProvisionalProofForCIHold(*j); err != nil {
		t.Fatalf("settled parent proof: %v", err)
	}
}

func TestCIHeldReviewCannotBypassUnsettledActiveFeedback(t *testing.T) {
	for _, kind := range []string{"pending", "unresolved", "reply", "fetch"} {
		t.Run(kind, func(t *testing.T) {
			_, j, _, _ := ciHeldFeedbackFixture(t)
			c := generalFeedback("comment:1", "Please verify")
			entry := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c)}
			switch kind {
			case "unresolved":
				entry.Assessment = &PRFeedbackAssessment{ID: c.ID, Fingerprint: entry.Fingerprint, Disposition: "unaddressable", Reason: "decision needed"}
				entry.Reply = &PRFeedbackReply{ID: "reply"}
			case "reply":
				entry.Assessment = &PRFeedbackAssessment{ID: c.ID, Fingerprint: entry.Fingerprint, Disposition: "no_change", Reason: "verified"}
			}
			j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
			if kind == "fetch" {
				j.PRFeedback.FetchPending = true
				j.PRFeedback.FetchResumeState = "ci"
			}
			if err := ValidateCIHeldReviewStart(*j); err == nil {
				t.Fatal("unfinished feedback was bypassed")
			}
		})
	}
}

func TestCIHeldReviewDoesNotDiscardUnrecordedFeedbackAssessment(t *testing.T) {
	_, j, _, _ := ciHeldFeedbackFixture(t)
	comment := generalFeedback("comment:1", "Reviewed change")
	assessment := PRFeedbackAssessment{ID: comment.ID, Fingerprint: feedbackFingerprint(comment), Disposition: "no_change", Reason: "already correct"}
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: assessment.Fingerprint, StartedAttempt: j.Attempt, Assessment: &assessment}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	entry.Reply = &PRFeedbackReply{ID: "recorded-reply"}
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	j.Outcome = testOutcome("implemented")
	j.Outcome.PRFeedbackAssessments = []PRFeedbackAssessment{assessment}
	if err := ValidateCIHeldReviewStart(*j); err != nil {
		t.Fatalf("recorded settled assessment rejected: %v", err)
	}
	j.Outcome.PRFeedbackAssessments[0].Reason = "not recorded"
	if ValidateCIHeldReviewStart(*j) == nil {
		t.Fatal("unrecorded assessment was discarded")
	}
}
