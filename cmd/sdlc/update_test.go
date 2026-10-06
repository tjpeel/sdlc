package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/install"
)

func updateSourceFixture(t *testing.T) string {
	t.Helper()
	root := runGitFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "cmd", "sdlc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/tjpeel/sdlc\n\ngo 1.25.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestUpdateDryRunUsesLegacySavedSourceWithoutBuildingOrWriting(t *testing.T) {
	root := updateSourceFixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stateDir := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "runtime.json"), []byte(`{"version":1,"source":`+strconvJSON(root)+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bin, "connected-call")
	for _, name := range []string{"go", "docker", "gh", "codex", "claude"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	before := dashboardTree(t, stateDir)
	var output bytes.Buffer
	if err := updateCommand(context.Background(), []string{"--bin-dir", bin, "--dry-run"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), root) || !strings.Contains(output.String(), "source-pins") {
		t.Fatal(output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline preview executed a build or connected command")
	}
	if _, err := os.Stat(filepath.Join(bin, ".sdlc-install.lock")); !os.IsNotExist(err) {
		t.Fatal("preview created an install lock")
	}
	after := dashboardTree(t, stateDir)
	if strconvJSON(before) != strconvJSON(after) {
		t.Fatal("preview changed private installation state")
	}
}

func strconvJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestUpdateRejectsDirtyPullAndConflictingPolicies(t *testing.T) {
	root := updateSourceFixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := updateCommand(context.Background(), []string{"--source", root, "--bin-dir", bin, "--dry-run", "--pull"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "clean source checkout") {
		t.Fatalf("dirty source accepted for pull: %v", err)
	}
	if err := updateCommand(context.Background(), []string{"--cli-only", "--dependencies"}, io.Discard, io.Discard); err == nil {
		t.Fatal("conflicting update policies accepted")
	}
}

func TestUpdateLocatorCannotExecuteUnmanagedPATHCommand(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "executed")
	if err := os.WriteFile(filepath.Join(bin, "sdlc"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := installedBinDirectory(); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unmanaged PATH executable accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("update executed an unmanaged PATH command")
	}
}

func TestUpdateReceiptKeepsSourceVisibleFromAnotherProject(t *testing.T) {
	root := updateSourceFixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(root, "cmd", "sdlc", "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){ fmt.Println(\"sdlc 0.1.0-test (unknown; test)\") }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := install.Build(context.Background(), root, bin, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	t.Chdir(other)
	source, err := updateSource(context.Background(), os.Getenv("SDLC_STATE_DIR"), "")
	if err != nil || source != root {
		t.Fatalf("saved source: %q %v", source, err)
	}
	var output bytes.Buffer
	if err := versionDetailsCommand(context.Background(), []string{"--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var version versionView
	if err := json.Unmarshal(output.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if version.Source.Path != root || version.Installed.Version != "0.1.0-test" || version.BuiltArtifact != "verified installation receipt" {
		t.Fatalf("version view from other project: %+v", version)
	}
}
