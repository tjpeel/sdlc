package project

import "testing"

func TestSonarRepairPolicyConfig(t *testing.T) {
	for _, policy := range []string{"", "advisory", SonarRepairAllNew, "unsupported"} {
		cfg, err := ParseConfig([]byte(`{"version":1,"checks":[],"input_files":[],"sonar":{"repair_policy":"` + policy + `"}}`))
		if policy == "unsupported" {
			if err == nil {
				t.Fatal("unknown policy accepted")
			}
			continue
		}
		if err != nil || cfg.Sonar.RepairsEnabled() != (policy == SonarRepairAllNew) {
			t.Fatal("invalid policy parsing", policy, err)
		}
	}
}
