package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func configureTestHeadroom(t *testing.T, directory string) {
	t.Helper()
	if _, err := headroom.Save(directory, "http://127.0.0.1:8787"); err != nil {
		t.Fatal(err)
	}
}

func assertOnlyHeadroomSettings(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(os.Getenv("SDLC_STATE_DIR"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "headroom.json" {
		t.Fatal("command created installation state beyond configured proxy", entries, err)
	}
}

func TestHeadroomSavedOutsideRunAndRequiredByNewPlans(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	state := os.Getenv("SDLC_STATE_DIR")
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{runArgs("--repo", "example/project", "--dry-run"), {"--reference", "TASK-1", "--all", "--dry-run"}} {
		err := runCommand(context.Background(), args, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "set --proxy-url") {
			t.Fatal("new plan did not require saved proxy", err)
		}
	}
	var output bytes.Buffer
	if err := runtimeCommand(context.Background(), []string{"headroom", "set", "--proxy-url", "http://127.0.0.1:8787/"}, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "http://host.docker.internal:8787") {
		t.Fatal(output.String())
	}
	first, err := headroom.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{runArgs("--repo", "example/project", "--dry-run", "--json"), {"--reference", "TASK-1", "--all", "--dry-run", "--json"}} {
		output.Reset()
		if err := runCommand(context.Background(), args, &output); err != nil || !strings.Contains(output.String(), `"proxy_url":"http://127.0.0.1:8787"`) {
			t.Fatal(output.String(), err)
		}
	}
	if _, err := headroom.Save(state, "http://localhost:9797"); err != nil {
		t.Fatal(err)
	}
	frozen, err := selectRunHeadroom(state, runOptions{frozenHeadroom: &first})
	if err != nil || frozen != first {
		t.Fatal("saved run redirected by settings change", frozen, err)
	}
	if _, err := selectRunHeadroom(state, runOptions{supplied: map[string]bool{"headroom": true}, headroomMode: "off"}); err == nil {
		t.Fatal("new run bypassed required proxy")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configuration or preview contacted provider/Docker")
	}
	if _, err := os.Stat(filepath.Join(state, "runtime-build.lock")); !os.IsNotExist(err) {
		t.Fatal("configuration acquired runtime lease")
	}
}

func TestSavedFeatureRetainsExternalProxyAfterDefaultChanges(t *testing.T) {
	driver, run := featureAdoptionFixture(t, "ready")
	directory, journal, err := driver.loadRun("01-selected.md", run)
	if err != nil {
		t.Fatal(err)
	}
	original := headroom.Config{Mode: "external", ProxyURL: "http://localhost:8787"}
	driver.settings.Headroom = original
	journal.Plan.Headroom = original
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
	if _, err := headroom.Save(os.Getenv("SDLC_STATE_DIR"), "http://localhost:9797"); err != nil {
		t.Fatal(err)
	}
	data, err := offlinePlan(context.Background(), []string{"--reference", "TASK-1", "--all"}, driver.plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Headroom headroom.Config `json:"headroom"`
	}
	if err := json.Unmarshal(data, &preview); err != nil || preview.Headroom != original {
		t.Fatal("saved feature proxy changed", preview, err)
	}
}
