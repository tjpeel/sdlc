package workrun

import (
	"context"
	"fmt"
	"time"
)

func (runner Runner) checkCI(ctx context.Context, directory string, journal *Journal) error {
	started := time.Now()
	defer func() { journal.Timings.CIWallMS += time.Since(started).Milliseconds() }()
	queryStarted := time.Now()

	checks, err := runner.readPublishedCI(ctx, directory, journal)
	journal.Timings.CIQueryMS += time.Since(queryStarted).Milliseconds()
	if err == nil {
		journal.CI = checks
	}
	if err != nil {
		return runner.stop(directory, journal, "blocked", err.Error())
	}
	if checks.Status != "missing" && !journal.MissingChecksSince.IsZero() {
		journal.MissingChecksSince = time.Time{}
		if err := runner.save(directory, journal); err != nil {
			return err
		}
	}
	if err := runner.save(directory, journal); err != nil {
		return err
	}
	wait := runner.PollInterval
	if checks.Status == "missing" {
		if journal.MissingChecksSince.IsZero() {
			journal.MissingChecksSince = time.Now().UTC()
			if err := runner.save(directory, journal); err != nil {
				return err
			}
			fmt.Fprintln(runner.Output, "Waiting for CI checks to appear on the published PR.")
		}
		remaining := runner.MissingCheckGrace - time.Since(journal.MissingChecksSince)
		if remaining <= 0 {
			return runner.stop(directory, journal, "blocked", "PR has no reported CI checks after the startup grace; configure CI, then resume this run")
		}
		if remaining < wait {
			wait = remaining
		}
	}
	switch checks.Status {
	case "pending", "missing":
		waitStarted := time.Now()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			journal.Timings.CIWaitMS += time.Since(waitStarted).Milliseconds()
			return runner.stop(directory, journal, "blocked", "CI wait cancelled; published PR retained")
		case <-timer.C:
		}
		journal.Timings.CIWaitMS += time.Since(waitStarted).Milliseconds()
		return nil
	case "failed":
		budget := journal.CIRepairBudget
		if budget == nil {
			return runner.stop(directory, journal, "blocked", legacyCIRepairBound)
		}
		if budget.LastGrantedHead == journal.Publication.HeadSHA {
			return runner.stop(directory, journal, "blocked", "CI repair requires a new published head; unchanged failed revision retained")
		}
		limit := runner.MaxRounds
		if limit <= 0 {
			limit = 12
		}
		if !runLimitsOverridden(journal) && budget.Used >= limit {
			return runner.stop(directory, journal, "blocked", ciRepairBound)
		}
		budget.Used++
		budget.LastGrantedHead = journal.Publication.HeadSHA
		journal.Rounds++
		journal.Feedback = ciRepairFeedback(checks, journal.Publication.HeadSHA)
		for _, dispatch := range journal.CIWorkflowDispatches {
			if dispatch.Publication == journal.Publication {
				journal.Feedback += "\nThis validation used the repository's existing manual workflow. Runner integration is local-only. Do not change repository workflows, dispatch inputs, tests, docs or product behavior to accommodate the runner or compensate for missing PR-event context. Repair independently actionable product defects only. Missing event context or unsupported manual validation is an operational block, not a source-code repair task."
			}
		}

		if err := runner.transition(directory, journal, "repairing"); err != nil {
			return err
		}
		return nil
	case "passed":
		if err := runner.transition(directory, journal, "awaiting_reviewer"); err != nil {
			return err
		}
	default:
		return runner.stop(directory, journal, "blocked", "CI returned an unknown outcome")
	}
	return nil
}
