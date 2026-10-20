package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type reviewChangedPathRepository interface {
	ReviewChangedPaths(context.Context, string, string, string) ([]string, error)
}

func (r DockerRepository) ReviewChangedPaths(ctx context.Context, workspace, base, head string) ([]string, error) {
	if !objectID.MatchString(base) || !objectID.MatchString(head) {
		return nil, fmt.Errorf("review file inventory requires exact revisions")
	}
	raw, err := r.run(ctx, workspace, "review_files:"+base+":"+head, "")
	if err != nil {
		return nil, err
	}
	var paths []string
	if json.Unmarshal(raw, &paths) != nil || paths == nil {
		return nil, fmt.Errorf("invalid changed-file review inventory")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if !safeRelative(p) || seen[p] {
			return nil, fmt.Errorf("invalid changed-file review path")
		}
		seen[p] = true
	}
	return paths, nil
}

// File paths are stable inspection identifiers. Narrative gap text is not a
// completion receipt, and grouped/wildcard labels never credit individual files.
const fileInspectionGap = "Complete the pending exact file paths in inspection."

func isFileInspectionGap(gap string) bool {
	return gap == fileInspectionGap || strings.HasPrefix(gap, "Finish material file inspection:")
}

func withoutFileInspectionGaps(gaps []string) []string {
	result := []string{}
	for _, gap := range gaps {
		if !isFileInspectionGap(gap) {
			result = append(result, gap)
		}
	}
	return result
}

func reviewInspectionHistory(j *Journal) [][]ReviewFileInspection {
	var history [][]ReviewFileInspection
	c := j.ReviewContinuation
	if c == nil {
		return history
	}
	if c.Inspection != nil {
		history = append(history, c.Inspection)
	}
	for _, report := range j.ReviewReports {
		if report.Publication == c.Publication && report.Attempt >= c.StartAttempt && report.Attempt <= c.LastAttempt && report.Coverage != nil && report.Coverage.BaseSHA == c.Publication.BaseSHA && report.Coverage.HeadSHA == c.Publication.HeadSHA {
			history = append(history, report.Coverage.Inspection)
		}
	}
	return history
}

func mergeReviewInspection(history ...[]ReviewFileInspection) map[string]ReviewFileInspection {
	merged := map[string]ReviewFileInspection{}
	for _, batch := range history {
		for _, item := range batch {
			merged[item.Path] = item
		}
	}
	return merged
}

// Old reports remain unchanged. Only exact paths in the verified changed-file
// manifest contribute to the current cumulative accounting.
func (r Runner) reconcileReviewInspection(ctx context.Context, workspace string, out *Outcome, history ...[]ReviewFileInspection) error {
	return r.reconcileInspectionPaths(ctx, workspace, out, nil, history...)
}

func (r Runner) reconcileInspectionPaths(ctx context.Context, workspace string, out *Outcome, required []string, history ...[]ReviewFileInspection) error {
	c := out.ReviewCoverage
	if c == nil || (out.Status != "reviewed" && out.Status != "review_incomplete") {
		return nil
	}
	if c.Inspection == nil {
		_, manifest := r.Repository.(reviewChangedPathRepository)
		for _, batch := range history {
			manifest = manifest || batch != nil
		}
		if !manifest {
			return nil
		}
		c.Inspection = []ReviewFileInspection{}
	}
	if err := validateReviewDetails(*out, "review"); err != nil {
		return err
	}
	batches := append(append([][]ReviewFileInspection(nil), history...), c.Inspection)
	merged := mergeReviewInspection(batches...)
	var paths []string
	manifestAvailable := false
	if required != nil {
		manifestAvailable = true
		paths = append([]string(nil), required...)
	} else if repo, ok := r.Repository.(reviewChangedPathRepository); ok {
		manifestAvailable = true
		var err error
		paths, err = repo.ReviewChangedPaths(ctx, workspace, c.BaseSHA, c.HeadSHA)
		if err != nil {
			return err
		}
	} else {
		for path := range merged {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	allowed := map[string]bool{}
	for _, path := range paths {
		allowed[path] = true
	}
	// An explicitly reopened supporting caller must survive even when it is
	// unchanged and outside both diff manifests. Verify exact tracked names;
	// grouped or invented paths never receive inspection credit.
	needsSupport := false
	for p := range merged {
		needsSupport = needsSupport || !allowed[p]
	}
	if needsSupport {
		if reader, ok := r.Repository.(reviewTrackedPathRepository); ok {
			tracked, err := reader.ReviewTrackedPaths(ctx, workspace, c.HeadSHA)
			if err != nil {
				return err
			}
			for _, p := range tracked {
				if _, present := merged[p]; present && !allowed[p] {
					paths = append(paths, p)
					allowed[p] = true
				}
			}
			sort.Strings(paths)
		} else if required != nil {
			for p, item := range merged {
				if !allowed[p] && !item.Complete {
					return fmt.Errorf("cannot verify reopened supporting inspection path")
				}
			}
		}
	}
	invalid := 0
	for _, item := range c.Inspection {
		if !allowed[item.Path] {
			invalid++
		}
	}
	c.Inspection = []ReviewFileInspection{}
	pending := false
	for _, path := range paths {
		item, ok := merged[path]
		if !ok {
			item = ReviewFileInspection{Path: path, Evidence: "No exact per-file inspection supplied. Inspect this file and return one exact path per entry; do not group paths or use wildcards."}
		}
		c.Inspection = append(c.Inspection, item)
		pending = pending || !item.Complete
	}
	if len(paths) == 0 && !manifestAvailable {
		pending = true
	}
	c.Gaps = withoutFileInspectionGaps(c.Gaps)
	c.CompletedGaps = withoutFileInspectionGaps(c.CompletedGaps)
	if invalid > 0 {
		out.Summary += fmt.Sprintf("\nInspection accounting: %d entries did not exactly match changed-file paths and received no credit. Use the pending exact paths in inspection; return one file per entry, with no grouped paths or wildcards.", invalid)
	}
	if pending {
		c.Gaps = append(c.Gaps, fileInspectionGap)
		c.Scope = "partial"
		out.Status = "review_incomplete"
	} else if len(c.Gaps) == 0 {
		if out.Status == "review_incomplete" {
			c.Gaps = []string{"Return the final independent review judgement for the recorded inspection."}
			c.Scope = "partial"
		} else {
			c.Scope = "full"
		}
	}
	return nil
}

// Refresh only derived inspection accounting after the saved snapshot and CI
// checks pass. The publication, session, inputs and historical reports stay fixed.
func (r Runner) restoreReviewInspection(ctx context.Context, workspace string, j *Journal) error {
	c := j.ReviewContinuation
	if c == nil {
		return nil
	}
	for _, report := range j.ReviewReports {
		if report.Publication == c.Publication && report.Attempt >= c.StartAttempt && report.Attempt <= c.LastAttempt {
			c.Recommendations = retainReviewFindings(c.Recommendations, report.Recommendations)
		}
	}
	history := reviewInspectionHistory(j)
	present := c.Inspection != nil
	for _, batch := range history {
		present = present || batch != nil
	}
	for _, gap := range c.Gaps {
		present = present || isFileInspectionGap(gap)
	}
	if !present {
		return nil
	}
	out := Outcome{Status: "reviewed", ReviewCoverage: &ReviewCoverage{BaseSHA: c.Publication.BaseSHA, HeadSHA: c.Publication.HeadSHA, Scope: "full", Gaps: []string{}, Inspection: []ReviewFileInspection{}}}
	if err := r.reconcilePlannedInspection(ctx, workspace, j, &out, history...); err != nil {
		return err
	}
	c.Inspection = out.ReviewCoverage.Inspection
	gaps := withoutFileInspectionGaps(c.Gaps)
	if out.Status == "review_incomplete" {
		gaps = append(gaps, fileInspectionGap)
	}
	if len(gaps) == 0 {
		gaps = append(gaps, "Return the final independent review judgement for the recorded inspection.")
	}
	c.Gaps = gaps
	return nil
}

func reviewFilesAdvanced(j *Journal, coverage *ReviewCoverage) bool {
	previous := mergeReviewInspection(reviewInspectionHistory(j)...)
	for _, item := range coverage.Inspection {
		if item.Complete && (!previous[item.Path].Complete || previous[item.Path].InheritedFromAttempt != 0 && item.InheritedFromAttempt == 0) {
			return true
		}
	}
	return false
}

type reviewTrackedPathRepository interface {
	ReviewTrackedPaths(context.Context, string, string) ([]string, error)
}

func (r DockerRepository) ReviewTrackedPaths(ctx context.Context, workspace, head string) ([]string, error) {
	if !objectID.MatchString(head) {
		return nil, fmt.Errorf("invalid supporting-path revision")
	}
	raw, err := r.run(ctx, workspace, "review_tracked:"+head, "")
	var paths []string
	if err != nil || json.Unmarshal(raw, &paths) != nil || paths == nil {
		return nil, fmt.Errorf("supporting file inventory unavailable")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if !safeRelative(p) || seen[p] {
			return nil, fmt.Errorf("invalid supporting path inventory")
		}
		seen[p] = true
	}
	return paths, nil
}
