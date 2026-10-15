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
	if len(out.Findings)+len(out.Questions)+len(out.Limitations)+len(out.RepositoryRequests) == 0 {
		return nil, nil
	}
	result := &ReviewInterruption{Findings: out.Findings, Questions: out.Questions, Limitations: out.Limitations, RepositoryRequests: out.RepositoryRequests}
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
	data, err := json.Marshal(c)
	if err != nil || len(data) > 64*1024 || c.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, c.Publication.Number) || c.Publication.Number < 1 || !objectID.MatchString(c.Publication.HeadSHA) || !objectID.MatchString(c.Publication.BaseSHA) || c.Attempt < 1 || c.Attempt > j.Attempt || c.Provider != j.Plan.Roles.Review.Provider || c.Model != j.Plan.Roles.Review.Name || len(c.Findings)+len(c.Questions)+len(c.Limitations)+len(c.RepositoryRequests) == 0 {
		return fmt.Errorf("invalid interrupted review evidence")
	}
	out := Outcome{Status: "blocked", Summary: "Interrupted independent review", Findings: c.Findings, Questions: c.Questions, Limitations: c.Limitations}
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
	if j.Outcome.Status != "reviewed" || len(j.Outcome.Limitations) == 0 {
		return ""
	}
	return "Warning: independent review has limitations; human review is required:\n  " + strings.Join(j.Outcome.Limitations, "\n  ")
}
