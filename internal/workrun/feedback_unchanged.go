package workrun

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
)

// Only an already-reviewed exact candidate may finish feedback without new
// implementation checks and reviews. The agent's no_change claim is insufficient.
func (r Runner) finishUnchangedFeedback(ctx context.Context, dir string, j *Journal) (bool, error) {
	h, out := j.PRFeedback, j.Outcome
	if h == nil || !h.Active || h.ReviewedCandidate == nil || (out.Status != "implemented" && out.Status != "checks_requested") || len(out.PRFeedbackAssessments) == 0 || len(out.Questions) > 0 || len(out.Findings) > 0 || len(out.Limitations) > 0 || len(out.DiagnosticRequests) > 0 || len(out.VerificationRequests) > 0 || len(out.RepositoryRequests) > 0 || j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending {
		return false, nil
	}
	b := h.ReviewedCandidate
	if b.Publication != j.Publication || (!rebaseFeedbackCandidateCurrent(j, b) && (!j.Evidence.Passed || j.Evidence.Head != b.Head || j.Evidence.Tree != b.Tree || j.Evidence.Key != verificationKey(j, VerificationRequest{}, b.Head, b.Tree) || !verificationCurrent(j, b.Head, b.Tree))) {
		return false, nil
	}
	pending := pendingFeedbackEntries(j)
	accepted := len(pending) == 0
	if !accepted && len(pending) != len(out.PRFeedbackAssessments) {
		return false, nil
	}
	expected := map[string]string{}
	for _, e := range pending {
		expected[e.Comment.ID] = e.Fingerprint
	}
	if accepted {
		latest := map[string]PRFeedbackEntry{}
		for _, e := range h.Entries {
			latest[e.Comment.ID] = e
		}
		for _, a := range out.PRFeedbackAssessments {
			e, ok := latest[a.ID]
			if !ok || e.Assessment == nil || e.Provisional || e.Fingerprint != a.Fingerprint || !reflect.DeepEqual(*e.Assessment, a) {
				return false, nil
			}
			expected[a.ID] = a.Fingerprint
		}
	}

	for _, a := range out.PRFeedbackAssessments {
		if a.Disposition != "no_change" || len(a.Commits) != 0 || expected[a.ID] != a.Fingerprint || validateFeedbackAssessment(a) != nil {
			return false, nil
		}
		delete(expected, a.ID)
	}
	if len(expected) != 0 {
		return false, nil
	}
	rev, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil {
		return true, r.stop(dir, j, "blocked", err.Error())
	}
	if !rev.Clean || rev.Head != b.Head || rev.Tree != b.Tree {
		return false, nil
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return true, r.stop(dir, j, "blocked", err.Error())
	}
	if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return true, r.stop(dir, j, "blocked", err.Error())
	}
	checks, err := r.readPublishedCI(ctx, dir, j)
	if err == nil {
		err = validateCIResult(checks, j.Publication)
	}
	if err != nil {
		return true, r.stop(dir, j, "blocked", err.Error())
	}
	if checks.Status != "passed" {
		return true, r.stop(dir, j, "blocked", "CI changed during unchanged-feedback assessment; existing review cannot be reused")
	}
	j.CI = checks
	final := out
	final.Status = "implemented" // Finalise the no-change receipts, without asserting a new review occurred.
	if !accepted {
		if _, err := r.acceptPRFeedback(ctx, dir, j, final); err != nil {
			return true, r.stop(dir, j, "blocked", err.Error())
		}
	}
	if err := r.replyPRFeedback(ctx, dir, j); err != nil {
		return true, r.stop(dir, j, "blocked", err.Error())
	}
	status := PRFeedbackStatus(*j)
	if status.Pending > 0 || status.OutstandingReplies > 0 {
		return true, r.stop(dir, j, "blocked", "unchanged feedback still has outstanding assessments or replies")
	}
	h = j.PRFeedback
	h.Active = false
	h.CompletedHead = j.Publication.HeadSHA
	h.ReviewedCandidate = nil
	j.Feedback = ""
	j.Outcome.Status = "reviewed" // Existing reviewed boundary remains current; no new report is created.
	if j.RebaseMonitoring != nil {
		j.Outcome.Status = "implemented" // A CI-only operation does not create a review.
	}
	if r.Output != nil {
		fmt.Fprintln(r.Output, "Feedback required no changes; reusing checks and reviews for the unchanged candidate.")
	}
	return true, r.transition(dir, j, "ready")
}
