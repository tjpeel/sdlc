package project

import "fmt"

const SonarRepairAllNew = "all-new-issues-and-duplication"

// SonarPolicy is local repository policy, independent of quality-gate thresholds
// and independent-review recommendation acceptance. Empty keeps legacy advisory
// behaviour. PR analysis supplies the new-code scope; repository backlog is not
// implicitly added to a selected ticket.
type SonarPolicy struct {
	RepairPolicy string `json:"repair_policy,omitempty"`
}

func (p SonarPolicy) RepairsEnabled() bool { return p.RepairPolicy == SonarRepairAllNew }
func (p SonarPolicy) Validate() error {
	if p.RepairPolicy != "" && p.RepairPolicy != "advisory" && p.RepairPolicy != SonarRepairAllNew {
		return fmt.Errorf("sonar.repair_policy must be advisory or all-new-issues-and-duplication")
	}
	return nil
}
