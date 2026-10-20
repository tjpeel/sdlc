package workrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ReviewInterruption retains known unresolved structured review work, without
// asserting that the native review completed or approved the publication.
type ReviewInterruption struct {
	Recommendations    []Finding                    `json:"recommendations,omitempty"`
	Coverage           *ReviewCoverage              `json:"coverage,omitempty"`
	Publication        Publication                  `json:"publication"`
	Attempt            int                          `json:"attempt"`
	Provider           string                       `json:"provider"`
	Model              string                       `json:"model"`
	Findings           []Finding                    `json:"findings,omitempty"`
	Questions          []string                     `json:"questions,omitempty"`
	Limitations        []string                     `json:"limitations,omitempty"`
	RepositoryRequests []CompanionRepositoryRequest `json:"repository_requests,omitempty"`
}

func unresolvedReviewHandoff(raw []byte) (*ReviewInterruption, error) {
	var out Outcome
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || validateOutcome(out, "review") != nil {
		return nil, nil
	}
	if len(out.Findings)+len(out.Questions)+len(out.Limitations)+len(out.RepositoryRequests)+len(out.Recommendations)+reviewGapCount(out.ReviewCoverage) == 0 {
		return nil, nil
	}
	result := &ReviewInterruption{Recommendations: out.Recommendations, Coverage: out.ReviewCoverage, Findings: out.Findings, Questions: out.Questions, Limitations: out.Limitations, RepositoryRequests: out.RepositoryRequests}
	data, err := json.Marshal(result)
	if err != nil || len(data) > 64*1024 {
		return nil, fmt.Errorf("interrupted structured review exceeds its retention bound")
	}
	return result, nil
}

func validateReviewInterruption(j *Journal) error {
	c := j.ReviewInterruption
	if c == nil {
		return nil
	}
	if coverage := c.Coverage; coverage != nil && (coverage.BaseSHA != c.Publication.BaseSHA || coverage.HeadSHA != c.Publication.HeadSHA) {
		return fmt.Errorf("interrupted review coverage does not match its publication")
	}
	data, err := json.Marshal(c)
	if err != nil || len(data) > 64*1024 || c.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, c.Publication.Number) || c.Publication.Number < 1 || !objectID.MatchString(c.Publication.HeadSHA) || !objectID.MatchString(c.Publication.BaseSHA) || c.Attempt < 1 || c.Attempt > j.Attempt || c.Provider != j.Plan.Roles.Review.Provider || c.Model != j.Plan.Roles.Review.Name || len(c.Findings)+len(c.Questions)+len(c.Limitations)+len(c.RepositoryRequests)+len(c.Recommendations)+reviewGapCount(c.Coverage) == 0 {
		return fmt.Errorf("invalid interrupted review evidence")
	}
	out := Outcome{Recommendations: c.Recommendations, ReviewCoverage: c.Coverage, Status: "blocked", Summary: "Interrupted independent review", Findings: c.Findings, Questions: c.Questions, Limitations: c.Limitations}
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	if out.Questions == nil {
		out.Questions = []string{}
	}
	if out.Limitations == nil {
		out.Limitations = []string{}
	}
	if err := validateOutcome(out, "review"); err != nil {
		return err
	}
	return validateCompanionRepositoryRequests(c.RepositoryRequests, &j.Plan)
}

// ReviewWarningNotice preserves the reviewer's stated coverage at the human checkpoint.
func ReviewWarningNotice(j Journal) string {
	if j.Outcome.Status != "reviewed" && !reviewReuseCurrent(&j) {
		return ""
	}
	gaps := append([]string(nil), j.Outcome.Limitations...)
	reports := relevantReviewReports(j)
	if len(reports) == 0 {
		if j.Outcome.ReviewCoverage == nil {
			gaps = append(gaps, "Review coverage was not recorded by this older run.")
		}
		if c := j.Outcome.ReviewCoverage; c != nil && c.Scope != "full" {
			gaps = append(gaps, c.Scope+" review: "+strings.Join(c.Gaps, "; "))
		}
	}
	for _, report := range reports {
		gaps = append(gaps, report.Limitations...)
		if report.Coverage == nil {
			gaps = append(gaps, "Earlier review coverage was not recorded.")
		} else if report.Coverage.Scope != "full" {
			gaps = append(gaps, report.Coverage.Scope+" review at "+report.Publication.HeadSHA[:8]+": "+strings.Join(report.Coverage.Gaps, "; "))
		}
	}
	gaps = uniqueReviewText(gaps)
	if len(gaps) == 0 {
		return ""
	}
	return "Warning: independent review has limitations; human review is required:\n  " + strings.Join(gaps, "\n  ")
}
