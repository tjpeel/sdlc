package workrun

import (
	"context"
	"errors"
	"testing"
)

func TestNoChangeFeedbackReusesExactReviewWithoutChecksOrModelReview(t *testing.T) {
	for _, status := range []string{"implemented", "checks_requested"} {
		t.Run(status, func(t *testing.T) {
			dir, j, r, pub, checker := feedbackReadyRun(t)
			session, attempt, reports, head := j.SessionID, j.Attempt, len(j.ReviewReports), j.Publication.HeadSHA
			comment := generalFeedback("comment:unchanged", "Is this already covered?")
			pub.comments = []PRFeedbackComment{comment}
			out := assessedOutcome(comment, "no_change")
			out.Status = status
			out.LocalReview = false
			provider := &feedbackScriptProvider{outcomes: []Outcome{out}}
			r.Provider = provider
			r.ProcessPRFeedback = true
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.State != "ready" || checker.calls != 1 || len(provider.sessions) != 1 || provider.sessions[0].Role != "implementation" || provider.sessions[0].ResumeID != session || j.Attempt != attempt+1 || len(j.ReviewReports) != reports || j.Publication.HeadSHA != head || pub.replies != 1 || j.PRFeedback.Active {
				t.Fatal("unchanged feedback repeated gates or changed boundary", j.State, checker.calls, len(provider.sessions), pub.replies)
			}
		})
	}
}

func TestNoChangeFeedbackReplyFailureCanResumeWithoutReviews(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	comment := generalFeedback("comment:unchanged", "Already covered?")
	pub.comments = []PRFeedbackComment{comment}
	pub.replyErr = errors.New("temporary reply failure")
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	pub.replyErr = nil
	r.ProcessPRFeedback = false
	err := r.Run(context.Background(), dir, j, "")
	if err != nil || j.State != "ready" || checker.calls != 1 || len(provider.sessions) != 1 {
		t.Fatal("reply retry lost unchanged boundary", err, j.State)
	}
}

func TestNoChangeFeedbackDoesNotBypassChangedOrMissingEvidence(t *testing.T) {
	for _, scenario := range []string{"publication", "candidate", "missing evidence", "verification key", "requested diagnostic", "requested verification", "partial batch", "correction commits"} {
		t.Run(scenario, func(t *testing.T) {
			dir, j, r, pub, _ := feedbackReadyRun(t)
			comment := generalFeedback("comment:unchanged", "Already covered?")
			pub.comments = []PRFeedbackComment{comment}
			if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
				t.Fatal(err)
			}
			j.Outcome = assessedOutcome(comment, "no_change")
			switch scenario {
			case "publication":
				j.PRFeedback.ReviewedCandidate.Publication.HeadSHA = testBase
			case "candidate":
				j.PRFeedback.ReviewedCandidate.Tree = testBase
			case "missing evidence":
				j.Evidence.Passed = false
			case "verification key":
				j.Evidence.Key = "stale"
			case "requested diagnostic":
				j.Outcome.DiagnosticRequests = []VerificationRequest{{}}
			case "requested verification":
				j.Outcome.VerificationRequests = []VerificationRequest{{}}
			case "partial batch":
				j.Outcome.PRFeedbackAssessments = nil
			case "correction commits":
				j.Outcome.PRFeedbackAssessments[0].Commits = []string{testHead}
			}
			handled, err := r.finishUnchangedFeedback(context.Background(), dir, j)
			if handled || err != nil || pub.replies != 0 {
				t.Fatal("unsafe unchanged shortcut", handled, err)
			}
		})
	}
}

func TestNoChangeFeedbackStillRequiresCurrentGreenCI(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:unchanged", "Already covered?")
	pub.comments = []PRFeedbackComment{comment}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.Outcome = assessedOutcome(comment, "no_change")
	pub.ciErr = errors.New("PR revision changed")
	handled, err := r.finishUnchangedFeedback(context.Background(), dir, j)
	if !handled || !errors.Is(err, ErrStopped) || j.State != "blocked" || pub.replies != 0 {
		t.Fatal("ignored changed CI", handled, err, j.State)
	}
}
