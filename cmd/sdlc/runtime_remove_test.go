package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestRuntimeRemoveRequiresForceForStoppedWork(t *testing.T) {
	j, _ := attentionFixture(t)
	id := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = id
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	state := runtimeimage.State{Version: 1, Name: "updated", Engine: "test-engine", ImageID: id, Source: j.Plan.Root}
	data, _ := json.Marshal(state)
	path := filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.updated.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1 $2\" in 'info --format') echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; 'image inspect') echo '" + id + "';; 'image rm') exit 0;; 'ps --all') exit 0;; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(j.Plan.Root, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	var out bytes.Buffer
	if err := runtimeRemoveCommand(context.Background(), []string{"updated"}, &out); err == nil {
		t.Fatal("removed pinned runtime without force")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed removal lost runtime")
	}
	if err := runtimeRemoveCommand(context.Background(), []string{"updated", "--force"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(out.String(), "may no longer resume") {
		t.Fatal("forced removal did not warn and remove")
	}
}
func TestRuntimeRemoveRejectsDefaultEvenWithForce(t *testing.T) {
	attentionFixture(t)
	if err := runtimeRemoveCommand(context.Background(), []string{"--force", "local"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("default removal: %v", err)
	}
}

func TestRuntimeRemoveMissingTagStillProtectsSavedWork(t *testing.T) {
	j, _ := attentionFixture(t)
	id := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = id
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(runtimeimage.State{Version: 1, Name: "missing", Engine: "test-engine", ImageID: id, Source: j.Plan.Root})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.missing.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1 $2\" in 'info --format') echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; 'image inspect') exit 1;; 'image ls'|'ps --all') exit 0;; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(j.Plan.Root, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	var out bytes.Buffer
	if err := runtimeRemoveCommand(context.Background(), []string{"missing"}, &out); err == nil {
		t.Fatal("removed a runtime referenced by saved work")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := runtimeRemoveCommand(context.Background(), []string{"--force", "missing"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale record retained")
	}
	if !strings.Contains(out.String(), "may no longer resume") {
		t.Fatal("missing warning")
	}
}

func TestRuntimeRemoveProtectsReadyRegisteredRuns(t *testing.T) {
	j, _ := attentionFixture(t)
	id := "sha256:" + strings.Repeat("b", 64)
	j.ImageID, j.State = id, "ready"
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	state := runtimeimage.State{Version: 1, Name: "updated", Engine: "test-engine", ImageID: id, Source: j.Plan.Root}
	data, _ := json.Marshal(state)
	path := filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runtime.updated.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1 $2\" in 'info --format') echo '{\"id\":\"test-engine\",\"os\":\"linux\"}';; 'image inspect') echo '" + id + "';; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(j.Plan.Root, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	if err := runtimeRemoveCommand(context.Background(), []string{"updated"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "saved runs") {
		t.Fatalf("ready run did not protect runtime: %v", err)
	}
}
