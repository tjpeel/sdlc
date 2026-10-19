package workrun

import (
	"context"
	"errors"
	"fmt"
)

// The last completed report is reusable only for its exact publication and
// native attempt. Partial reports and unfinished reviewer work never qualify.
func completedReviewAwaitingCI(j *Journal) bool {
	if j.Outcome.Status != "reviewed" || len(j.Outcome.Findings) != 0 || len(j.Outcome.Questions) != 0 || len(j.ReviewReports) == 0 || j.ReviewContinuation != nil || j.ReviewInterruption != nil {
		return false
	}
	report := j.ReviewReports[len(j.ReviewReports)-1]
	c := report.Coverage
	return report.Attempt == j.Attempt && report.Publication == j.Publication && len(report.Findings) == 0 && c != nil && c.Scope == "full" && len(c.Gaps) == 0 && c.BaseSHA == j.Publication.BaseSHA && c.HeadSHA == j.Publication.HeadSHA
}

// readPublishedCI is shared by ordinary polling and both review boundaries.
// Manual workflow evidence must not disappear merely because review finished.
func (r Runner) readPublishedCI(ctx context.Context, dir string, j *Journal) (CIResult, error) {
	checks, err := r.Publisher.Checks(ctx, j.Plan, j.Publication)
	if err == nil {
		err = validateCIResult(checks, j.Publication)
	}
	if err == nil {
		checks, err = r.ensureCIWorkflow(ctx, dir, j, checks)
	}
	return checks, err
}

func (runner Runner) finishCompletedReview(ctx context.Context, directory string, journal *Journal) (bool, error) {
	// checkCI retains the existing grace period, configured polling interval,
	// cancellation, failure-repair policy and manual workflow reconciliation.
	if err := runner.checkCI(ctx, directory, journal); err != nil {
		if errors.Is(err, ErrStopped) {
			return false, runner.stop(directory, journal, "blocked", "final CI recheck after completed independent review: "+journal.StopReason)
		}
		return false, err
	}
	if journal.CI.Status != "passed" {
		return false, nil
	}
	if feedbackStatus := PRFeedbackStatus(*journal); feedbackStatus.Pending > 0 || feedbackStatus.OutstandingReplies > 0 {
		return false, runner.stop(directory, journal, "blocked", "human PR feedback assessments or replies remain outstanding")
	}
	if journal.PRFeedback != nil && journal.PRFeedback.Active {
		journal.PRFeedback.Active = false
		journal.PRFeedback.CompletedHead = journal.Publication.HeadSHA
	}
	if err := runner.transition(directory, journal, "ready"); err != nil {
		return false, err
	}
	if accepted, err := runner.autoAcceptRecommendations(ctx, directory, journal); err != nil {
		return false, runner.stop(directory, journal, "blocked", err.Error())
	} else if accepted {
		return false, nil
	}
	if notice := ReviewSkipNotice(*journal); notice != "" {
		fmt.Fprintln(runner.Output, notice)
	}
	if notice := ReviewRecommendationNotice(*journal); notice != "" {
		fmt.Fprintln(runner.Output, notice)
	}
	if notice := ReviewWarningNotice(*journal); notice != "" {
		fmt.Fprintln(runner.Output, notice)
	}
	fmt.Fprintln(runner.Output, "Ready for human review:", journal.Publication.URL)
	if status := PRFeedbackStatus(*journal); status.Summary != "" {
		fmt.Fprintln(runner.Output, status.Summary)
	}
	return true, nil
}
