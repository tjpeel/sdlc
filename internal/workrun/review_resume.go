package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

func reviewSnapshotPath(dir, relative string) (string, error) {
	if !safeRelative(relative) {
		return "", fmt.Errorf("invalid review snapshot path")
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	expected := filepath.Join(root, relative)
	actual, err := filepath.EvalSymlinks(filepath.Join(dir, relative))
	if err != nil || actual != expected {
		return "", fmt.Errorf("independent review snapshot is missing or symlinked")
	}
	return actual, nil
}

func (r Runner) prepareIndependentReview(ctx context.Context, dir string, j *Journal) (string, error) {
	c := j.ReviewContinuation
	if c == nil {
		return r.ReviewWorkspace(ctx, *j, dir)
	}
	if err := validateReviewContinuation(j); err != nil {
		return "", err
	}
	workspace, err := reviewSnapshotPath(dir, c.Workspace)
	if err != nil {
		return "", err
	}
	revision, err := r.Repository.Inspect(ctx, workspace)
	if err != nil || !revision.Clean || revision.Head != c.Publication.HeadSHA || revision.Tree != c.Tree {
		return "", fmt.Errorf("independent review snapshot changed before continuation")
	}
	if err := verifyInputs(workspace, j.Plan.Inputs); err != nil {
		return "", err
	}
	return workspace, nil
}

func reviewInspectionAdvanced(previous []string, coverage *ReviewCoverage) bool {
	if len(coverage.CompletedGaps) == 0 {
		return false
	}
	prior := map[string]bool{}
	for _, gap := range previous {
		prior[gap] = true
	}
	completed := map[string]bool{}
	for _, gap := range coverage.CompletedGaps {
		if !prior[gap] {
			return false
		}
		completed[gap] = true
	}
	remaining := map[string]bool{}
	for _, gap := range coverage.Gaps {
		remaining[gap] = true
	}
	for gap := range prior {
		if !completed[gap] && !remaining[gap] {
			return false
		}
	}
	for gap := range completed {
		if remaining[gap] {
			return false
		}
	}
	return true
}

func (r Runner) independentReview(ctx context.Context, dir string, j *Journal, workspace string) (SessionResult, error) {
	for {
		resume := ""
		if c := j.ReviewContinuation; c != nil {
			var err error
			workspace, err = r.prepareIndependentReview(ctx, dir, j)
			if err != nil {
				return SessionResult{}, err
			}
			resume = c.SessionID
			if activePRCheckpoint(j, "review") {
				if _, err := r.checkpointBoundary(ctx, j); err != nil {
					return SessionResult{}, err
				}
			} else if err := r.reviewSourceCI(ctx, j); err != nil {
				return SessionResult{}, err
			}
		}
		result, err := r.session(ctx, dir, j, "review", workspace, resume)
		if err != nil {
			// A later native failure cannot erase already known unfinished work.
			if j.ReviewContinuation != nil && result.ReviewInterruption == nil {
				raw, _ := json.Marshal(j.Outcome)
				result.ReviewInterruption, _ = unresolvedReviewHandoff(raw)
			}
			return result, err
		}
		if resume != "" && result.SessionID != resume {
			return result, fmt.Errorf("independent reviewer changed its native session")
		}
		out := result.Outcome
		if err := r.reconcileReviewInspection(ctx, workspace, &out); err != nil {
			return result, err
		}
		result.Outcome = out

		if out.Status == "reviewed" && out.ReviewCoverage != nil && out.ReviewCoverage.Scope != "full" {
			out.Status = "review_incomplete"
		}
		if out.Status == "reviewed" && out.ReviewCoverage != nil && (out.ReviewCoverage.BaseSHA != j.Publication.BaseSHA || out.ReviewCoverage.HeadSHA != j.Publication.HeadSHA) {
			return result, fmt.Errorf("review completion changed its publication boundary")
		}
		if out.Status != "review_incomplete" {
			if out.Status == "reviewed" && out.ReviewCoverage == nil {
				return result, fmt.Errorf("completed review lacks its coverage boundary")
			}
			// Final judgement may withdraw suspicions; interim findings remain in reports.
			if out.Status == "reviewed" || out.Status == "checks_requested" {
				j.ReviewContinuation = nil
			}
			return result, nil
		}
		if err := validateOutcome(out, "review"); err != nil {
			return result, err
		}
		if c := j.ReviewContinuation; c != nil {
			out.Findings = retainReviewFindings(c.Findings, out.Findings)
		}
		j.Outcome = out
		if err := recordReviewReport(j); err != nil {
			return result, err
		}
		c := j.ReviewContinuation
		stalled := c != nil && !reviewInspectionAdvanced(c.Gaps, out.ReviewCoverage)
		if c == nil {
			relative, err := filepath.Rel(dir, workspace)
			if err != nil || !sessionIdentifier.MatchString(result.SessionID) {
				return result, fmt.Errorf("review continuation lacks its original session")
			}
			resolved, err := reviewSnapshotPath(dir, relative)
			if err != nil {
				return result, err
			}
			revision, err := r.Repository.Inspect(ctx, resolved)
			if err != nil || !revision.Clean || revision.Head != j.Publication.HeadSHA {
				return result, fmt.Errorf("review continuation lacks its original snapshot")
			}
			c = &ReviewContinuation{Publication: j.Publication, Model: selectedReviewModel(j), Speed: j.Plan.Roles.ReviewSpeed, Tree: revision.Tree, SessionID: result.SessionID, Workspace: relative, StartAttempt: j.Attempt}
			j.ReviewContinuation = c
		}
		c.LastAttempt = j.Attempt
		if !stalled {
			c.Gaps = append([]string(nil), out.ReviewCoverage.Gaps...)
		}
		c.Findings = retainReviewFindings(c.Findings, out.Findings)
		if err := validateReviewContinuation(j); err != nil {
			return result, err
		}
		if err := r.save(dir, j); err != nil {
			return result, err
		}
		if stalled {
			out.Status = "blocked"
			out.Summary = "Independent review did not advance its named unfinished inspection; original session and gaps retained."
			result.Outcome = out
			return result, nil
		}
	}
}
