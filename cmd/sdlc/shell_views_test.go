package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/buildinfo"
)

func TestOnboardStatusDoesNotInitializeProject(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	before := dashboardTree(t, root)
	var out bytes.Buffer
	if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unchecked") || !strings.Contains(out.String(), "terminal setup") {
		t.Fatal(out.String())
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestProjectCatalogueRemovalKeepsWorkAndHistory(t *testing.T) {
	root := runGitFixture(t)
	before := dashboardTree(t, root)
	var out bytes.Buffer
	if err := projectCommand(context.Background(), []string{"add", root, "--name", "demo", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	entries, err := projectList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "demo" || !entries[0].Current {
		t.Fatalf("%+v", entries)
	}
	if err := projectCommand(context.Background(), []string{"remove", "demo", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	after := dashboardTree(t, root)
	if len(before) != len(after) {
		t.Fatal("project removal changed work")
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatalf("changed %s", path)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "shell-projects.json"))
	if err != nil || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("catalogue %s %v", data, err)
	}
}

func TestVersionDetailsNeverExecutesPATHCandidate(t *testing.T) {
	root := runGitFixture(t)
	bin := t.TempDir()
	marker := filepath.Join(bin, "executed")
	if err := os.WriteFile(filepath.Join(bin, "sdlc"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out bytes.Buffer
	if err := versionDetailsCommand(context.Background(), []string{"--details", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var result versionView
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Running.Version != buildinfo.Version || result.Installed.Version != "unknown" || result.BuiltArtifact != "not recorded" || result.Source.Path != "" {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PATH binary was executed")
	}
	if err := versionDetailsCommand(context.Background(), []string{"--source", root}, &out); err == nil {
		t.Fatal("non-SDLC source accepted")
	}
}

func TestInspectTicketBoundsAndLinkRejection(t *testing.T) {
	root := runGitFixture(t)
	var out bytes.Buffer
	args := []string{"--reference", "TASK-1", "--ticket", "01-selected.md", "--json"}
	if err := inspectCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".sdlc/work/TASK-1/tickets/01-selected.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 256*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := inspectCommand(context.Background(), args, &out); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized ticket accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README.md"), path); err != nil {
		t.Fatal(err)
	}
	if err := inspectCommand(context.Background(), args, &out); err == nil {
		t.Fatal("symlink accepted")
	}
}
