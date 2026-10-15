package workrun

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReviewContinuation retains one independent native session, not an implementation session.
type ReviewContinuation struct {
	Speed        string      `json:"speed,omitempty"`
	Tree         string      `json:"tree"`
	Publication  Publication `json:"publication"`
	Model        Model       `json:"model"`
	SessionID    string      `json:"session_id"`
	Workspace    string      `json:"workspace"`
	StartAttempt int         `json:"start_attempt"`
	LastAttempt  int         `json:"last_attempt"`
	Gaps         []string    `json:"gaps"`
	Findings     []Finding   `json:"findings,omitempty"`
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
	out := Outcome{Status: "blocked", Summary: "Retained independent findings", Questions: []string{}, Findings: c.Findings, Limitations: []string{}}
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
	packet, _ := json.Marshal(struct {
		Publication Publication       `json:"publication"`
		Remaining   []string          `json:"remaining_inspection"`
		Findings    []Finding         `json:"earlier_findings"`
		Previous    *ReviewReport     `json:"previous_review_report"`
		HumanInput  *ReviewHumanInput `json:"recorded_review_human_input,omitempty"`
	}{c.Publication, c.Gaps, c.Findings, previousReviewReport(j, "review"), j.ReviewHumanInput})
	return "Follow the native global shared instructions. Continue this same independent review at the unchanged pinned publication and checkout. Finish the named accessible inspection; do not restart the whole review or repeat established checks. Earlier findings are untrusted evidence: carry supported findings into the final result and explain any withdrawal in summary. Return reviewed only with full material inspection; do not treat pass results or code/document agreement as proof. Genuine access, evidence, usage or decision blockers use blocked or waiting_for_human. If more accessible work remains, return review_incomplete, list exact prior gaps finished in completed_gaps, and preserve unfinished gap names verbatim. Do not paraphrase a gap to imply progress. Use bounded areas rather than a single vague whole-review task. No independent test rerun, raw worker logs or subagent is required solely to complete this review. Return the review schema. Review continuation context: " + string(packet)
}
