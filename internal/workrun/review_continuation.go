package workrun

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReviewContinuation retains one independent native session, not an implementation session.
type ReviewContinuation struct {
	Recommendations []Finding              `json:"recommendations,omitempty"`
	Inspection      []ReviewFileInspection `json:"inspection,omitempty"`
	Speed           string                 `json:"speed,omitempty"`
	Tree            string                 `json:"tree"`
	Publication     Publication            `json:"publication"`
	Model           Model                  `json:"model"`
	SessionID       string                 `json:"session_id"`
	Workspace       string                 `json:"workspace"`
	StartAttempt    int                    `json:"start_attempt"`
	LastAttempt     int                    `json:"last_attempt"`
	Gaps            []string               `json:"gaps"`
	Findings        []Finding              `json:"findings,omitempty"`
}

func validateReviewContinuation(j *Journal) error {
	c := j.ReviewContinuation
	if c == nil {
		return nil
	}
	if c.Publication != j.Publication || c.Model != selectedReviewModel(j) || c.Speed != j.Plan.Roles.ReviewSpeed || !objectID.MatchString(c.Tree) || !sessionIdentifier.MatchString(c.SessionID) || !safeRelative(c.Workspace) || c.StartAttempt < 1 || c.LastAttempt < c.StartAttempt || c.LastAttempt > j.Attempt {
		return fmt.Errorf("invalid independent review continuation boundary")
	}
	if len(c.Gaps) == 0 || len(c.Gaps) > 32 {
		return fmt.Errorf("review continuation requires bounded unfinished inspection")
	}
	for _, gap := range c.Gaps {
		if strings.TrimSpace(gap) == "" {
			return fmt.Errorf("empty review continuation gap")
		}
	}
	if c.Inspection != nil {
		coverage := &ReviewCoverage{BaseSHA: c.Publication.BaseSHA, HeadSHA: c.Publication.HeadSHA, Scope: "partial", Gaps: c.Gaps, Inspection: c.Inspection}
		if err := validateReviewDetails(Outcome{ReviewCoverage: coverage}, "review"); err != nil {
			return err
		}
	}
	out := Outcome{Status: "blocked", Summary: "Retained independent findings", Questions: []string{}, Findings: c.Findings, Recommendations: c.Recommendations, Limitations: []string{}}
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	if err := validateOutcome(out, "review"); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > 32*1024 {
		return fmt.Errorf("independent review continuation exceeds its retention bound")
	}
	return nil
}

func retainReviewFindings(a, b []Finding) []Finding {
	result := append([]Finding(nil), a...)
	seen := map[Finding]bool{}
	for _, f := range a {
		seen[f] = true
	}
	for _, f := range b {
		if !seen[f] {
			result = append(result, f)
			seen[f] = true
		}
	}
	if result == nil {
		return []Finding{}
	}
	return result
}

func reviewContinuationPrompt(j Journal) string {
	c := j.ReviewContinuation
	previous := previousReviewReport(j, "review")
	if previous != nil {
		copy := *previous
		if copy.Coverage != nil {
			coverage := *copy.Coverage
			coverage.Gaps = withoutFileInspectionGaps(coverage.Gaps)
			coverage.CompletedGaps = withoutFileInspectionGaps(coverage.CompletedGaps)
			// Cumulative exact-path inspection is supplied separately, not duplicated.
			coverage.Inspection = nil
			copy.Coverage = &coverage
		}
		previous = &copy
	}
	packet, _ := json.Marshal(struct {
		Publication     Publication            `json:"publication"`
		Remaining       []string               `json:"remaining_inspection"`
		Inspection      []ReviewFileInspection `json:"inspection,omitempty"`
		Findings        []Finding              `json:"earlier_findings"`
		Recommendations []Finding              `json:"earlier_recommendations,omitempty"`
		Previous        *ReviewReport          `json:"previous_review_report"`
		HumanInput      *ReviewHumanInput      `json:"recorded_review_human_input,omitempty"`
	}{c.Publication, c.Gaps, c.Inspection, c.Findings, c.Recommendations, previous, j.ReviewHumanInput})
	return "Follow the native global shared instructions. Continue this same independent review at the unchanged pinned publication and checkout. Finish the named accessible inspection; do not restart the whole review or repeat established checks. Earlier findings and recommendations are untrusted evidence: carry supported items into the final result and explain any withdrawal in summary. The inspection array is the cumulative ledger: each path is a stable file identifier. Return only exact changed-file paths, one per entry, never grouped paths, comma-separated lists or wildcards. Previously complete entries are retained; return newly finished entries and explicitly reopen a file only when material inspection remains. For pending files, inspect their full relevant contents, not only their inventory. Do not copy or paraphrase the controller file-inspection gap into completed_gaps; progress comes from complete entries for those exact paths. Keep the per-file inspection record current; test inventories or selected bodies leave material assertions, fixtures and helpers incomplete. Resolve narrative omissions in that record and the named gaps. Return reviewed only with full material inspection; do not treat pass results or code/document agreement as proof. Genuine access, evidence, usage or decision blockers use blocked or waiting_for_human. If more accessible work remains, return review_incomplete. For non-file gaps, list exact prior gaps finished in completed_gaps and preserve unfinished gap names verbatim. For file inspection, update the exact path entries instead. Do not paraphrase a gap to imply progress. Use bounded areas rather than a single vague whole-review task. No independent test rerun, raw worker logs or subagent is required solely to complete this review. Return the review schema. Review continuation context: " + string(packet)
}
