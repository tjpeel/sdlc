package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestFeaturePreviewTracksExecutionSelections(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	args := []string{"--reference", "TASK-1", "--all", "--alternate-providers"}
	preview := func(args []string) []byte {
		t.Helper()
		data, err := offlinePlan(context.Background(), args, root)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	baseline := preview(args)
	var result struct {
		Roles       map[string]workrun.Roles
		Checks      [][]string
		CheckInputs []string `json:"check_inputs"`
		Inputs      []string
		Headroom    headroom.Config
	}
	if err := json.Unmarshal(baseline, &result); err != nil {
		t.Fatal(err)
	}
	if result.Roles["01-selected.md"] != workrun.DefaultModels().Codex || result.Roles["02-other.md"] != workrun.DefaultModels().Claude || !reflect.DeepEqual(result.CheckInputs, []string{"README.md"}) || len(result.Checks) == 0 {
		t.Fatalf("missing resolved selections: %s", baseline)
	}
	for _, mode := range []string{"passthrough", "optimize"} {
		changed := preview(append(append([]string{}, args...), "--headroom", mode))
		if planHash(changed) == planHash(baseline) {
			t.Fatalf("%s selector omitted from hash", mode)
		}
	}
	if planHash(preview(append(append([]string{}, args...), "--input", "README.md"))) == planHash(baseline) {
		t.Fatal("additional input omitted from hash")
	}
	state := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	defaults := workrun.DefaultModels()
	defaults.Codex.Implementation.Name = "example-new-default"
	data, _ := json.Marshal(defaults)
	if err := os.WriteFile(filepath.Join(state, "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	changedModels := preview(args)
	if planHash(changedModels) == planHash(baseline) {
		t.Fatal("model defaults omitted from hash")
	}
	if err := os.WriteFile(filepath.Join(root, ".sdlc/project.json"), []byte(`{"version":1,"checks":[["go","vet","./..."]],"input_files":["README.md"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	changedChecks := preview(args)
	if planHash(changedChecks) == planHash(changedModels) {
		t.Fatal("project checks omitted from hash")
	}
	if err := os.WriteFile(filepath.Join(root, ".sdlc/project.json"), []byte(`{"version":1,"checks":[["go","vet","./..."]],"input_files":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if planHash(preview(args)) == planHash(changedChecks) {
		t.Fatal("check input selection omitted from hash")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview contacted provider or Docker", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sdlc/work/TASK-1/series")); !os.IsNotExist(err) {
		t.Fatal("preview wrote checkpoint", err)
	}
}

func TestFeatureResumePreviewUsesFrozenSelections(t *testing.T) {
	driver, run := featureAdoptionFixture(t, "ready")
	directory, journal, err := driver.loadRun("01-selected.md", run)
	if err != nil {
		t.Fatal(err)
	}
	config := headroom.Config{Mode: "optimize", ImageID: "sha256:" + strings.Repeat("b", 64), PolicyVersion: headroom.PolicyVersion}
	driver.settings.Headroom = config
	journal.Plan.Headroom = config
	if err := workrun.Save(directory, &journal); err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	series, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{"01-selected.md": run}}
	if err := workseries.Save(series, &state); err != nil {
		t.Fatal(err)
	}
	args := []string{"--reference", "TASK-1", "--all"}
	baseline, err := offlinePlan(context.Background(), args, driver.plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Headroom    headroom.Config
		Roles       map[string]workrun.Roles
		Checks      [][]string
		InputHashes map[string]string `json:"input_hashes"`
	}
	if err := json.Unmarshal(baseline, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Headroom != config || !reflect.DeepEqual(preview.Roles, driver.settings.Roles) || !reflect.DeepEqual(preview.Checks, driver.settings.Config.Checks) || !reflect.DeepEqual(preview.InputHashes, driver.settings.InputHashes) {
		t.Fatalf("frozen selections lost: %s", baseline)
	}
	// New local defaults/config do not replace an existing feature's settings.
	if err := os.WriteFile(filepath.Join(driver.plan.Root, ".sdlc/project.json"), []byte(`{"version":1,"checks":[["go","vet","./..."]],"input_files":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaults := workrun.DefaultModels()
	defaults.Codex.Implementation.Name = "changed-default"
	data, _ := json.Marshal(defaults)
	stateDir := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	resumed, err := offlinePlan(context.Background(), args, driver.plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	if planHash(resumed) != planHash(baseline) {
		t.Fatal("resume preview replaced frozen selections")
	}
	for _, mode := range []string{"off", "passthrough"} {
		if _, err := offlinePlan(context.Background(), append(append([]string{}, args...), "--headroom", mode), driver.plan.Root); err == nil || !strings.Contains(err.Error(), "recorded --headroom") {
			t.Fatalf("resume mode %s accepted: %v", mode, err)
		}
	}
	if _, err := offlinePlan(context.Background(), append(append([]string{}, args...), "--headroom", "optimize"), driver.plan.Root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.loadRun("01-selected.md", run); err != nil {
		t.Fatal("persisted ticket mode rejected", err)
	}
	journal.Plan.Headroom = headroom.Config{}
	if err := workrun.Save(directory, &journal); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.loadRun("01-selected.md", run); err == nil {
		t.Fatal("ticket adopted with different Headroom mode")
	}
}

func TestResumeKeepsRecordedDockerTestsWhenProjectDefaultChanges(t *testing.T) {
	driver, run := featureAdoptionFixture(t, "blocked")
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	series, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{"01-selected.md": run}}
	if err := workseries.Save(series, &state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(driver.plan.Root, ".sdlc/project.json"), []byte(`{"version":1,"checks":[["go","test","./..."]],"input_files":["README.md"],"docker_tests":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--reference", "TASK-1", "--all"},
		runArgs("--resume", run.RunID),
	} {
		data, err := offlinePlan(context.Background(), args, driver.plan.Root)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			DockerTests bool         `json:"docker_tests"`
			Plan        workrun.Plan `json:"plan"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.DockerTests || result.Plan.DockerTests {
			t.Fatalf("resume enabled a privileged daemon from changed project defaults: %s", data)
		}
	}
}

func TestHeadroomControllerArgumentBoundary(t *testing.T) {
	args := []string{"--reference", "TASK-1", "--all", "--headroom", "--json", "--json", "--terminal", "background"}
	want := []string{"--reference", "TASK-1", "--all", "--headroom", "--json"}
	if got := controllerArgs(args); !reflect.DeepEqual(got, want) {
		t.Fatalf("Headroom value treated as adapter flag: %v", got)
	}
}
