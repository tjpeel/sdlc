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

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestRuntimeMigrationCommandRetainsStoppedCandidate(t *testing.T) {
	j, _ := attentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	if err := os.Mkdir(j.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Workspace, "candidate"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	j.State, j.ResumeState, j.PendingRole = "blocked", "implementing", ""
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.SessionID = "00000000-0000-4000-8000-000000000001"
	j.Outcome.Questions = nil
	j.Outcome.Status = "blocked"
	j.StopReason = "Restore .NET SDK in the formatter worker"
	if err := os.WriteFile(filepath.Join(j.Workspace, "global.json"), []byte(`{"sdk":{"version":"10.0.401"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "precommit-format-1-test.log"), []byte("A compatible .NET SDK was not found.\nRequested SDK version: 10.0.401\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	target := runtimeimage.State{Version: 1, Name: "updated", Engine: "test-engine", ImageID: "sha256:" + strings.Repeat("b", 64), Inventory: statusInventory()}
	state, _ := json.Marshal(target)
	if err := os.WriteFile(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.updated.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(j.Plan.Root, "fake-bin", "docker")
	script := "#!/bin/sh\ncase \"$1\" in info) echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; image) echo '" + target.ImageID + "';; ps) :;; *) exit 1;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	var output bytes.Buffer
	if err := runtimeMigrationCommand(context.Background(), []string{"--run", j.ID, "--to", "updated", "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	before, err := workrun.Load(directory)
	if err != nil || before.ImageID != j.ImageID {
		t.Fatal("dry run changed checkpoint")
	}
	if err := runtimeMigrationCommand(context.Background(), []string{"--run", j.ID, "--to", "updated"}, &output); err != nil {
		t.Fatal(err)
	}
	after, err := workrun.Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != j.ID || after.ImageID != target.ImageID || after.SessionID != "" || after.Attempt != j.Attempt || len(after.RuntimeMigrations) != 1 {
		t.Fatal("migration lost run identity or lineage")
	}
	if data, err := os.ReadFile(filepath.Join(j.Workspace, "candidate")); err != nil || string(data) != "uncommitted" {
		t.Fatal("candidate changed")
	}
	if _, err := os.Stat(filepath.Join(directory, "runtime-migration-1.before.json")); err != nil {
		t.Fatal("previous checkpoint not retained")
	}
}

func TestRuntimeListAndUseShowPinnedRunsAndPreserveSelectionOnFailure(t *testing.T) {
	j, _ := attentionFixture(t)
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(filepath.Dir(j.Workspace), j); err != nil {
		t.Fatal(err)
	}
	target := runtimeimage.State{Version: 1, Name: "updated", Engine: "test-engine", ImageID: "sha256:" + strings.Repeat("b", 64), Inventory: statusInventory()}
	data, _ := json.Marshal(target)
	if err := os.WriteFile(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.updated.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in info) echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; image) echo '" + target.ImageID + "';; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(j.Plan.Root, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	var output bytes.Buffer
	if err := runtimeSelectionCommand(context.Background(), []string{"list"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "10.0.401") || !strings.Contains(output.String(), j.ID) || !strings.Contains(output.String(), "no current runtime record") {
		t.Fatal("runtime inventory or pinned run missing")
	}
	if err := runtimeSelectionCommand(context.Background(), []string{"use", "updated"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, err := runtimeimage.ReadDefaultName(os.Getenv("SDLC_STATE_DIR")); err != nil || got != "updated" {
		t.Fatal("default was not selected")
	}
	if err := runtimeSelectionCommand(context.Background(), []string{"use", "missing"}, &output); err == nil {
		t.Fatal("missing runtime selected")
	}
	if got, err := runtimeimage.ReadDefaultName(os.Getenv("SDLC_STATE_DIR")); err != nil || got != "updated" {
		t.Fatal("failed selection changed default")
	}
}

func TestNewRunDryRunUsesSelectedDefaultAndAllowsLocalOverride(t *testing.T) {
	root := runGitFixture(t)
	forbidConnectedRunCommands(t, root)
	dir := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runtimeimage.WriteDefaultName(dir, "updated"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args     []string
		expected string
	}{
		{[]string{"--repo", "example/project", "--dry-run", "--json"}, "updated"},
		{[]string{"--repo", "example/project", "--dry-run", "--json", "--runtime", "local"}, ""},
		{[]string{"--repo", "example/project", "--dry-run", "--json", "--runtime", "different"}, "different"},
	} {
		var output bytes.Buffer
		if err := runCommand(context.Background(), runArgs(tc.args...), &output); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Plan workrun.Plan `json:"plan"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Plan.RuntimeName != tc.expected {
			t.Fatalf("runtime %q, expected %q", result.Plan.RuntimeName, tc.expected)
		}
	}
}

func TestMigratedRunProtectsPreviousRuntimeFromReplacement(t *testing.T) {
	j, _ := attentionFixture(t)
	old := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = "sha256:" + strings.Repeat("b", 64)
	j.State, j.ResumeState, j.PendingRole = "blocked", "implementing", ""
	j.RuntimeMigrations = []workrun.RuntimeMigration{{At: time.Now().UTC(), PreviousImageID: old, PreviousSessionID: "original", SessionDirectory: "native-implementation-migrated-1"}}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(filepath.Dir(j.Workspace), j); err != nil {
		t.Fatal(err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: old}); err == nil || !strings.Contains(err.Error(), j.ID) {
		t.Fatal("prior runtime is not protected")
	}
}

func TestFeaturePreviewUsesSelectedDefaultRuntime(t *testing.T) {
	root := runGitFixture(t)
	forbidConnectedRunCommands(t, root)
	dir := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := runtimeimage.WriteDefaultName(dir, "updated"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"--reference", "TASK-1", "--all", "--repo", "example/project", "--dry-run", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		RuntimeName string `json:"runtime_name"`
	}
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.RuntimeName != "updated" {
		t.Fatalf("feature runtime: %q", plan.RuntimeName)
	}
}

func TestRuntimeListFindsForgottenUnfinishedRun(t *testing.T) {
	j, _ := attentionFixture(t)
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runs", j.ID+".json")); err != nil {
		t.Fatal(err)
	}
	state := runtimeimage.State{Version: 1, Engine: "test-engine", ImageID: j.ImageID, Inventory: statusInventory()}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in info) echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; image) echo '" + j.ImageID + "';; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(j.Plan.Root, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	var output bytes.Buffer
	if err := runtimeSelectionCommand(context.Background(), []string{"list"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "unfinished run "+j.ID) {
		t.Fatal("forgotten unfinished run missing from listing")
	}
}

func TestSavedRunPreviewDoesNotReadChangedDefaultRuntime(t *testing.T) {
	j, _ := attentionFixture(t)
	j.State, j.ResumeState, j.PendingRole = "blocked", "implementing", ""
	j.Outcome.Questions = nil
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime-default.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	args := []string{"--reference", j.Plan.Reference, "--ticket", filepath.Base(j.Plan.Ticket), "--resume", j.ID, "--dry-run", "--json"}
	if err := runCommand(context.Background(), args, &output); err != nil {
		t.Fatalf("saved run consulted changed default: %v", err)
	}
}
