package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func cleanupFeatureFixture(t *testing.T, registered bool) (*seriesDriver, workseries.Result) {
	t.Helper()
	driver, result := featureAdoptionFixture(t, "waiting_for_human")
	var err error
	result.Directory, err = workrun.RunDirectory(driver.plan.Root, driver.plan.Reference, "01-selected.md", result.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	result.State = "waiting_for_human"
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	series, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{"01-selected.md": result}}
	if err := workseries.Save(series, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if registered {
		j, err := workrun.Load(result.Directory)
		if err != nil {
			t.Fatal(err)
		}
		if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(result.Directory, j); err != nil {
			t.Fatal(err)
		}
	}
	return driver, result
}

func TestDashboardRemoveExplainsHiddenSavedRunWithoutDeletingIt(t *testing.T) {
	driver, result := cleanupFeatureFixture(t, false)
	before := dashboardTree(t, filepath.Join(driver.plan.Root, ".sdlc", "work"))
	var out bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"remove", "--run", result.RunID[:3]}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"already absent", "can still block runtime updates", "sdlc storage purge --run " + result.RunID, "sdlc work archive --reference"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	assertDashboardReadOnly(t, filepath.Join(driver.plan.Root, ".sdlc", "work"), "", before)
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), driver.plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: driver.settings.ImageID}); err == nil || !strings.Contains(err.Error(), result.RunID) || !strings.Contains(err.Error(), "feature TASK-1") {
		t.Fatal("hiding retained work changed runtime protection", err)
	}
}

func TestStoragePurgePreviewAndConfirmationRetireBothUpdateBlockers(t *testing.T) {
	driver, result := cleanupFeatureFixture(t, false)
	work := filepath.Join(driver.plan.Root, ".sdlc", "work")
	before := dashboardTree(t, work)
	var preview bytes.Buffer
	if err := storageCommand(context.Background(), []string{"purge", "--all"}, &preview); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.String(), "Nothing removed") || !strings.Contains(preview.String(), "Feature checkpoint abandoned") || !strings.Contains(preview.String(), result.RunID) {
		t.Fatal(preview.String())
	}
	assertDashboardReadOnly(t, work, "", before)
	var confirmed bytes.Buffer
	if err := storageCommand(context.Background(), []string{"purge", "--all", "--yes"}, &confirmed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{result.Directory, filepath.Join(work, "TASK-1", "series")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("saved checkpoint remained at %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(work, "TASK-1", "tickets", "01-selected.md")); err != nil || string(data) != "# Selected ticket\n" {
		t.Fatalf("purge changed ticket input: %q, %v", data, err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), driver.plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: driver.settings.ImageID}); err != nil {
		t.Fatal("purged checkpoints still block runtime update", err)
	}
}

func TestWorkArchivePreservesReferenceAndAllowsRuntimeReplacement(t *testing.T) {
	driver, result := cleanupFeatureFixture(t, true)
	originalJournal, err := os.ReadFile(filepath.Join(result.Directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(driver.plan.Root, ".sdlc", "work")
	before := dashboardTree(t, filepath.Join(work, "TASK-1"))
	var preview bytes.Buffer
	if err := workCommand(context.Background(), []string{"archive", "--reference", "TASK-1", "--dry-run"}, &preview); err != nil {
		t.Fatal(err)
	}
	assertDashboardReadOnly(t, filepath.Join(work, "TASK-1"), "", before)
	var out bytes.Buffer
	if err := workCommand(context.Background(), []string{"archive", "--reference", "TASK-1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ".archive") || !strings.Contains(out.String(), "reference name is free") {
		t.Fatal(out.String())
	}
	archives, err := os.ReadDir(filepath.Join(work, ".archive"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archive missing: %v, %v", archives, err)
	}
	archived := filepath.Join(work, ".archive", archives[0].Name())
	if data, err := os.ReadFile(filepath.Join(archived, "runs", "01-selected", result.RunID, "journal.json")); err != nil || !bytes.Equal(data, originalJournal) {
		t.Fatal("archive changed or lost saved journal", err)
	}
	views, err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).List(time.Now())
	if err != nil || len(views) != 0 {
		t.Fatalf("archive left unavailable registrations: %v, %v", views, err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), driver.plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: driver.settings.ImageID}); err != nil {
		t.Fatal("archive still blocks runtime replacement", err)
	}
	refs, err := project.References(context.Background(), driver.plan.Root)
	if err != nil || len(refs.References) != 0 {
		t.Fatalf("archive appears among active references: %v, %v", refs, err)
	}
	if err := os.MkdirAll(filepath.Join(work, "TASK-1", "tickets"), 0700); err != nil {
		t.Fatal("reference could not be reused", err)
	}
}

func TestDashboardRemoveAllPreviewsThenHidesStoppedEntriesOnly(t *testing.T) {
	root, _, journals := dashboardFixture(t)
	stopped := journals[1]
	directory := filepath.Dir(stopped.Workspace)
	if err := os.WriteFile(filepath.Join(directory, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, directory)
	var out bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"remove", "--all"}, &out); err != nil {
		t.Fatal(err)
	}
	registry := runstatus.New(filepath.Join(root, "installation"))
	if views, err := registry.List(time.Now()); err != nil || len(views) != 2 {
		t.Fatalf("preview changed registrations: %v, %v", views, err)
	}
	if err := dashboardCommand(context.Background(), []string{"remove", "--all", "--yes"}, &out); err != nil {
		t.Fatal(err)
	}
	views, err := registry.List(time.Now())
	if err != nil || len(views) != 1 || views[0].ID != journals[0].ID {
		t.Fatalf("did not keep active registration: %v, %v", views, err)
	}
	assertDashboardReadOnly(t, directory, "", before)
}

func TestSavedWorkCommandsRejectInvalidArgumentsWithoutCreatingStorage(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("SDLC_STATE_DIR", filepath.Join(root, "state"))
	for _, args := range [][]string{nil, {"--all", "--run", "abc"}, {"--all", "--yes", "--dry-run"}, {"--run", "ab"}, {"--all", "extra"}, {"--all", "--scope", "installation"}} {
		if err := storagePurgeCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid purge arguments", args)
		}
	}
	for _, args := range [][]string{nil, {"--reference", "TASK-1", "extra"}, {"--all"}} {
		if err := workArchiveCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid archive arguments", args)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("argument validation created runtime state", err)
	}
}
