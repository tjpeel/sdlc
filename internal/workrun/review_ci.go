package workrun

import (
	"context"
	"errors"
	"fmt"
)

// The last completed report is reusable only for its exact publication and
// native attempt. Partial reports and unfinished reviewer work never qualify.
func completedReviewAwaitingCI(j *Journal) bool {
	if reviewReuseCurrent(j) {
		return true
	}
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
	if r.SonarPolicy != nil {
		j.Plan.Sonar = *r.SonarPolicy
	}
	if err := j.Plan.Sonar.Validate(); err != nil {
		return CIResult{}, err
	}
	checks, err := r.Publisher.Checks(ctx, j.Plan, j.Publication)
	if err == nil {
		err = validateCIResult(checks, j.Publication)
	}
	if err == nil && j.Plan.Sonar.RepairsEnabled() && checks.Status == "passed" && checks.SonarFindings == nil {
		checks.Status = "missing"
		checks.Details += "\nSonar policy is waiting for complete current-head PR analysis."
	}
	if err == nil {
		checks, err = r.ensureCIWorkflow(ctx, dir, j, checks)
	}
	return checks, err
}

func (runner Runner) finishCompletedReview(ctx context.Context, directory string, journal *Journal) (bool, error) {
	if reviewReuseCurrent(journal) {
		revision, err := runner.Repository.Inspect(ctx, journal.Workspace)
		if err != nil || !revision.Clean || revision.Head != journal.Evidence.Head || revision.Tree != journal.ReviewReuse.Tree {
			return false, runner.stop(directory, journal, "blocked", "review reuse candidate changed; refusing to reuse prior review")
		}
	}

	// checkCI retains the existing grace period, configured polling interval,
	// cancellation, failure-repair policy and manual workflow reconciliation.
	if err := runner.checkCI(ctx, directory, journal); err != nil {
		if errors.Is(err, ErrStopped) {
			return false, runner.stop(directory, journal, "blocked", "final CI recheck after completed independent review: "+journal.StopReason)
		}
		return false, err
	}
	if journal.CI.Status != "passed" || journal.State == "repairing" {
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
	if accepted, err := runner.acceptRecommendationsUnlessReused(ctx, directory, journal); err != nil {
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
