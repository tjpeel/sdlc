package workrun

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"time"
)

// CIHeldReview records the original hold and the exact publication under review.
type CIHeldReview struct {
	Publication        Publication `json:"publication"`
	HoldReason         string      `json:"hold_reason"`
	StartedAt          time.Time   `json:"started_at"`
	Pending            bool        `json:"pending"`
	BoundaryFailed     bool        `json:"boundary_failed,omitempty"`
	Invalidated        bool        `json:"invalidated,omitempty"`
	PriorOutcome       *Outcome    `json:"prior_outcome,omitempty"`
	InterruptedOutcome *Outcome    `json:"interrupted_outcome,omitempty"`
}

func validateCIHeldReview(j *Journal) error {
	for i := range j.CIHeldReviewHistory {
		previous := j.CIHeldReviewHistory[i]
		if previous.Pending {
			return fmt.Errorf("historical CI-held review cannot be pending")
		}
		copy := *j
		copy.CIHeldReviewHistory = nil
		copy.CIHeldReview = &previous
		if err := validateCIHeldReview(&copy); err != nil {
			return err
		}
	}
	c := j.CIHeldReview
	if c == nil {
		return nil
	}
	if c.StartedAt.IsZero() || c.HoldReason == "" || c.Publication.Number < 1 || c.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, c.Publication.Number) || !objectID.MatchString(c.Publication.BaseSHA) || !objectID.MatchString(c.Publication.HeadSHA) {
		return fmt.Errorf("invalid CI-held review checkpoint")
	}
	if c.Pending && c.Invalidated {
		return fmt.Errorf("invalidated CI-held review cannot remain pending")
	}
	if c.Pending && c.Publication != j.Publication {
		return fmt.Errorf("CI-held review publication changed; retained review cannot continue")
	}
	return nil
}

func ciHeldReviewPending(j *Journal) bool {
	return j.CIHeldReview != nil && j.CIHeldReview.Pending && j.CIHeldReview.Publication == j.Publication
}

func ciHeldReviewOutcomeHasWork(o Outcome) bool {
	return len(o.Findings) > 0 || len(o.Questions) > 0 || len(o.Limitations) > 0 || len(o.RepositoryRequests) > 0 || len(o.VerificationRequests) > 0 || len(o.DiagnosticRequests) > 0 || len(o.PRFeedbackAssessments) > 0 || o.ReviewCoverage != nil && len(o.ReviewCoverage.Gaps) > 0
}

// ciHeldFeedbackSettled permits a fresh review after the existing feedback
// cycle has been assessed and replied to, without claiming passing CI.
func ciHeldFeedbackSettled(j Journal) bool {
	h := j.PRFeedback
	if h == nil || !h.Active || h.FetchPending || h.LocalPending != nil || pendingFeedback(&j) != nil {
		return false
	}
	if err := validatePRFeedbackJournal(&j); err != nil {
		return false
	}
	status := PRFeedbackStatus(j)
	return status.Pending == 0 && status.Unresolved == 0 && status.OutstandingReplies == 0
}

func ciHeldReviewOutcomeHasUnfinishedWork(j Journal) bool {
	out := j.Outcome
	if ciHeldFeedbackSettled(j) {
		latest := map[string]PRFeedbackEntry{}
		for _, entry := range j.PRFeedback.Entries {
			latest[entry.Comment.ID] = entry
		}
		for _, assessment := range out.PRFeedbackAssessments {
			entry, ok := latest[assessment.ID]
			if !ok || entry.Assessment == nil || entry.Provisional || !reflect.DeepEqual(*entry.Assessment, assessment) {
				return true
			}
		}
		out.PRFeedbackAssessments = nil
	}
	return ciHeldReviewOutcomeHasWork(out)
}

func ValidateCIHeldReviewStart(j Journal) error {
	if ciHeldReviewOutcomeHasUnfinishedWork(j) {
		return fmt.Errorf("CI-held review cannot overwrite unfinished saved outcome work")
	}
	if completedReviewAwaitingCI(&j) || j.State != "blocked" || j.ResumeState != "ci" || j.StopReason == "" || j.CIHeldReview != nil && j.CIHeldReview.Pending || j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || j.ReviewInterruption != nil || j.ReviewContinuation != nil || j.PendingRole != "" || len(j.Outcome.Questions) != 0 || pendingCIFeedback(&j) != nil || j.PRFeedback != nil && j.PRFeedback.Active && !ciHeldFeedbackSettled(j) || j.PushRepair != nil && j.PushRepair.Pending != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay {
		return fmt.Errorf("CI-held review requires a stopped CI hold with no pending recovery or human feedback")
	}
	feedback := PRFeedbackStatus(j)
	if feedback.Pending > 0 || feedback.Unresolved > 0 || feedback.OutstandingReplies > 0 {
		return fmt.Errorf("CI-held review cannot bypass pending human PR feedback")
	}
	c := &CIHeldReview{Publication: j.Publication, HoldReason: j.StopReason, StartedAt: time.Now().UTC(), Pending: true}
	j.CIHeldReview = c
	return validateCIHeldReview(&j)
}

func (r Runner) verifyCIHeldReviewBoundary(ctx context.Context, j *Journal) error {
	if !ciHeldReviewPending(j) {
		return fmt.Errorf("CI-held review no longer matches the publication")
	}
	p, ok := r.Publisher.(interface {
		ValidateFeedbackBoundary(context.Context, Plan, Publication) error
	})
	if !ok {
		return fmt.Errorf("publisher cannot verify the CI-held PR revision")
	}
	if err := p.ValidateFeedbackBoundary(ctx, j.Plan, j.Publication); err != nil {
		j.CIHeldReview.BoundaryFailed = true
		return fmt.Errorf("CI-held review PR boundary: %w", err)
	}
	j.CIHeldReview.BoundaryFailed = false
	return nil
}

func (r Runner) beginCIHeldReview(ctx context.Context, dir string, j *Journal) error {
	if err := ValidateCIHeldReviewStart(*j); err != nil {
		return err
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return err
	}
	if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return err
	}
	previous := j.CIHeldReview
	priorOutcome := j.Outcome
	j.CIHeldReview = &CIHeldReview{Publication: j.Publication, HoldReason: j.StopReason, StartedAt: time.Now().UTC(), Pending: true, PriorOutcome: &priorOutcome}
	if err := r.verifyCIHeldReviewBoundary(ctx, j); err != nil {
		j.CIHeldReview = previous
		return err
	}
	if previous != nil {
		j.CIHeldReviewHistory = append(j.CIHeldReviewHistory, *previous)
	}
	return r.transition(dir, j, "awaiting_reviewer")
}

// ValidateDiscardCIHeldReview allows explicit recovery only after a boundary
// error and never discards known unfinished review findings or questions.
func ValidateDiscardCIHeldReview(j Journal) error {
	feedback := PRFeedbackStatus(j)
	if j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || pendingCIFeedback(&j) != nil || j.PushRepair != nil && j.PushRepair.Pending != nil || j.PRFeedback != nil && j.PRFeedback.Active && !ciHeldFeedbackSettled(j) || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay || feedback.Pending > 0 || feedback.Unresolved > 0 || feedback.OutstandingReplies > 0 {
		return fmt.Errorf("CI-held review cannot discard pending recovery or feedback")
	}
	if ciHeldReviewOutcomeHasUnfinishedWork(j) {
		return fmt.Errorf("CI-held review cannot discard unfinished saved outcome work")
	}
	c := j.CIHeldReview
	if c == nil || !c.Pending || !c.BoundaryFailed || j.State != "blocked" || (j.ResumeState != "reviewing" && j.ResumeState != "awaiting_reviewer") || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingRole != "" || len(j.Outcome.Questions) != 0 || j.ReviewAssessment != nil && j.ReviewAssessment.Pending {
		return fmt.Errorf("discard requires a CI-held review stopped at a PR boundary without unfinished review work")
	}
	if c.InterruptedOutcome != nil && (c.InterruptedOutcome.Status != "reviewed" || len(c.InterruptedOutcome.Findings) != 0 || len(c.InterruptedOutcome.Questions) != 0 || len(c.InterruptedOutcome.Limitations) != 0 || len(c.InterruptedOutcome.RepositoryRequests) != 0 || len(c.InterruptedOutcome.VerificationRequests) != 0 || len(c.InterruptedOutcome.DiagnosticRequests) != 0) {
		return fmt.Errorf("known unfinished review findings or questions cannot be discarded")
	}
	return validateCIHeldReview(&j)
}

func (r Runner) discardCIHeldReview(dir string, j *Journal) error {
	if err := ValidateDiscardCIHeldReview(*j); err != nil {
		return err
	}
	j.CIHeldReview.Pending = false
	j.CIHeldReview.Invalidated = true
	j.State, j.ResumeState, j.StopReason = "blocked", "ci", j.CIHeldReview.HoldReason
	if err := r.save(dir, j); err != nil {
		return err
	}
	if r.Output != nil {
		fmt.Fprintln(r.Output, "Provisional review invalidated; the CI hold remains open. Reconcile the saved PR revision before reviewing again.")
	}
	return nil
}

func ciHeldReviewFindingsAwaitingCI(j *Journal) bool {
	c := j.CIHeldReview
	if c == nil || c.Pending || c.Invalidated || c.Publication != j.Publication || j.Outcome.Status != "reviewed" || len(j.Outcome.Findings) == 0 || len(j.ReviewReports) == 0 {
		return false
	}
	report := j.ReviewReports[len(j.ReviewReports)-1]
	return report.Publication == j.Publication && report.Attempt == j.Attempt && len(report.Findings) > 0 && reflect.DeepEqual(report.Findings, j.Outcome.Findings)
}
