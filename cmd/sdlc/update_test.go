package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func nativeUpdateDestination(t *testing.T, bin string) {
	t.Helper()
	// Give each legacy-install scenario its own native command. An actual
	// Homebrew installation on the host must not select that update route.
	executable, _ := bundledSourceFixture(t)
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sdlc"), data, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDryRunUsesLegacySavedSourceWithoutBuildingOrWriting(t *testing.T) {
	root := updateSourceFixture(t)
	bin := t.TempDir()
	nativeUpdateDestination(t, bin)
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
	nativeUpdateDestination(t, bin)
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

func TestHomebrewUpdatePreviewUsesSavedCheckoutAndNeverCallsBrew(t *testing.T) {
	executable, _ := bundledSourceFixture(t)
	root := updateSourceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "install_homebrew.py"), []byte("# offline fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if data, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, data)
		}
	}
	run("add", ".")
	run("-c", "commit.gpgsign=false", "commit", "-m", "Prepare offline source")
	stateDir := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema_version":1,"formula":"local/sdlc/sdlc","source":` + strconvJSON(root) + `}`)
	if err := os.WriteFile(filepath.Join(stateDir, "homebrew.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Dir(executable)
	marker := filepath.Join(bin, "external-call")
	for _, name := range []string{"brew", "python3", "docker"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(t.TempDir())
	before := dashboardTree(t, stateDir)
	var out bytes.Buffer
	if err := updateCommand(context.Background(), []string{"--dry-run"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "brew upgrade local/sdlc/sdlc") || !strings.Contains(out.String(), root) || !strings.Contains(out.String(), "source-pins") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline preview called an external builder or provider")
	}
	if strconvJSON(before) != strconvJSON(dashboardTree(t, stateDir)) {
		t.Fatal("preview changed installation state")
	}
	if err := updateCommand(context.Background(), []string{"--dry-run", "--bin-dir", bin}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "Homebrew owns") {
		t.Fatalf("Homebrew destination override accepted: %v", err)
	}
	// Metadata changes without content edits are common after a clone or backup.
	// Homebrew packages HEAD and must accept an otherwise unchanged checkout.
	if err := os.Chtimes(filepath.Join(root, "go.mod"), time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := updateCommand(context.Background(), []string{"--dry-run", "--pull"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("unchanged source rejected after metadata changed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/tjpeel/sdlc\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := updateCommand(context.Background(), []string{"--dry-run"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "uncommitted content") {
		t.Fatalf("uncommitted Homebrew source accepted: %v", err)
	}
}
