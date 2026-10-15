package runusage

import "fmt"

// ReviewEffortSelection is controller policy evidence, not a native observation.
type ReviewEffortSelection struct {
	Policy              string   `json:"policy"`
	Effort              string   `json:"effort"`
	Reasons             []string `json:"reasons"`
	Files               int      `json:"files"`
	ChangedLines        int      `json:"changed_lines"`
	StatisticsAvailable bool     `json:"statistics_available"`
}

func (s ReviewEffortSelection) Validate() error {
	if s.Policy != "pr-v1" || (s.Effort != "low" && s.Effort != "medium" && s.Effort != "high") || s.Files < 0 || s.Files > 2000 || s.ChangedLines < 0 || len(s.Reasons) != 1 {
		return fmt.Errorf("invalid automatic review effort evidence")
	}
	// Reasons are controller categories, never private paths or model-authored text.
	switch s.Reasons[0] {
	case "diff exceeds bounded assessment capacity", "security, data integrity or concurrency signals in changed source", "large substantive change: at least 25 files or 1000 changed lines", "public contract, API or persistence schema changes", "small documentation-only change", "ordinary or uncertain change; medium is the baseline", "diff assessment unavailable; medium is the baseline":
		return nil
	}
	return fmt.Errorf("invalid automatic review effort reason")
}
