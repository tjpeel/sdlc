package project

import "testing"

func TestCIWorkflowConfigurationIsExplicitAndBounded(t *testing.T) {
	for _, name := range []string{"check-pull-request.yml", "checks.yaml"} {
		cfg, err := ParseConfig([]byte(`{"version":1,"checks":[],"input_files":[],"non_default_ci_workflow":"` + name + `"}`))
		if err != nil || cfg.NonDefaultCIWorkflow != name {
			t.Fatal(cfg, err)
		}
	}
	for _, name := range []string{"../other.yml", "/tmp/check.yml", "https://host/check.yml", "check.sh"} {
		if ValidateCIWorkflow(name) == nil {
			t.Fatal("unsafe workflow accepted", name)
		}
	}
}
