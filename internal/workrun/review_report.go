package workrun

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func validateReviewDetails(out Outcome, role string) error {
	if role != "review" && (len(out.Recommendations) > 0 || out.ReviewCoverage != nil) {
		return fmt.Errorf("recommendations and review coverage require the review role")
	}
	if len(out.Recommendations) > 32 {
		return fmt.Errorf("too many review recommendations")
	}
	for _, item := range out.Recommendations {
		if !safeRelative(item.Path) || item.Line < 1 || strings.TrimSpace(item.Scenario) == "" || strings.TrimSpace(item.Recommendation) == "" || len(item.Priority) != 2 || item.Priority[0] != 'P' || item.Priority[1] < '0' || item.Priority[1] > '3' {
			return fmt.Errorf("review recommendation requires priority, file/line, consequence and recommendation")
		}
	}
	c := out.ReviewCoverage
	// Missing coverage remains readable for saved runs from older versions.
	if c == nil {
		return nil
	}
	if !objectID.MatchString(c.BaseSHA) || !objectID.MatchString(c.HeadSHA) || c.Gaps == nil || len(c.Gaps) > 32 {
		return fmt.Errorf("review coverage requires exact revisions and bounded gaps")
	}
	switch c.Scope {
	case "full":
		if len(c.Gaps) > 0 {
			return fmt.Errorf("full review coverage cannot contain uninspected areas")
		}
	case "partial", "delta":
		if len(c.Gaps) == 0 {
			return fmt.Errorf("partial or delta review must identify uninspected areas")
		}
	default:
		return fmt.Errorf("unknown review coverage scope")
	}
	for _, gap := range c.Gaps {
		if strings.TrimSpace(gap) == "" || !utf8.ValidString(gap) {
			return fmt.Errorf("invalid review coverage gap")
		}
	}
	return nil
}

func recordReviewReport(j *Journal) error {
	if err := validateReviewDetails(j.Outcome, "review"); err != nil {
		return err
	}
	c := j.Outcome.ReviewCoverage
	if c != nil && (c.BaseSHA != j.Publication.BaseSHA || c.HeadSHA != j.Publication.HeadSHA) {
		return fmt.Errorf("review coverage does not match the published PR boundary")
	}
	report := ReviewReport{Publication: j.Publication, Attempt: j.Attempt, Coverage: c, Limitations: j.Outcome.Limitations, Recommendations: j.Outcome.Recommendations}
	data, err := json.Marshal(report)
	if err != nil || len(data) > 32*1024 {
		return fmt.Errorf("review report exceeds its retention bound")
	}
	// Detach retained evidence from subsequent outcome updates.
	if err := json.Unmarshal(data, &report); err != nil {
		return err
	}
	j.ReviewReports = append(j.ReviewReports, report)
	return nil
}

func validateReviewReports(j *Journal) error {
	previous := 0
	for _, report := range j.ReviewReports {
		data, err := json.Marshal(report)
		if err != nil || len(data) > 32*1024 || report.Attempt <= previous || report.Attempt > j.Attempt || !objectID.MatchString(report.Publication.BaseSHA) || !objectID.MatchString(report.Publication.HeadSHA) || report.Publication.Number < 1 || report.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, report.Publication.Number) {
			return fmt.Errorf("invalid retained review report")
		}
		if c := report.Coverage; c != nil && (c.BaseSHA != report.Publication.BaseSHA || c.HeadSHA != report.Publication.HeadSHA) {
			return fmt.Errorf("retained review coverage boundary changed")
		}
		if err := validateReviewDetails(Outcome{ReviewCoverage: report.Coverage, Recommendations: report.Recommendations}, "review"); err != nil {
			return err
		}
		previous = report.Attempt
	}
	return nil
}

func previousReviewReport(j Journal, role string) *ReviewReport {
	if role != "review" || len(j.ReviewReports) == 0 {
		return nil
	}
	return &j.ReviewReports[len(j.ReviewReports)-1]
}

// A full review supersedes earlier coverage; a delta cannot erase prior gaps.
func relevantReviewReports(j Journal) []ReviewReport {
	reports := j.ReviewReports
	for i := len(reports) - 1; i >= 0; i-- {
		if reports[i].Coverage != nil && reports[i].Coverage.Scope == "full" {
			return reports[i:]
		}
	}
	return reports
}

func uniqueReviewText(items []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func ReviewRecommendationNotice(j Journal) string {
	if j.Outcome.Status != "reviewed" {
		return ""
	}
	items := []string{}
	reports := j.ReviewReports
	if len(reports) == 0 {
		reports = []ReviewReport{{Recommendations: j.Outcome.Recommendations}}
	}
	for _, report := range reports {
		for _, item := range report.Recommendations {
			boundary := ""
			if report.Publication.HeadSHA != "" {
				boundary = " at " + report.Publication.HeadSHA
			}
			items = append(items, fmt.Sprintf("%s %s:%d%s: %s Suggested: %s", item.Priority, item.Path, item.Line, boundary, item.Scenario, item.Recommendation))
		}
	}
	items = uniqueReviewText(items)
	if len(items) == 0 {
		return ""
	}
	return "Recommendations recorded for human consideration (may predate repairs; not automatic repairs):\n  " + strings.Join(items, "\n  ")
}

func reviewGapCount(c *ReviewCoverage) int {
	if c == nil {
		return 0
	}
	return len(c.Gaps)
}
