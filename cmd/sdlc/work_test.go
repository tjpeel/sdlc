package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkRejectsArgumentsWithoutCreatingState(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("SDLC_STATE_DIR", filepath.Join(directory, "installation"))
	for _, args := range [][]string{nil, {"--reference="}, {"example"}, {"--reference", "example", "extra"}, {"--provider", "claude"}, {"--reference"}} {
		var output bytes.Buffer
		if err := workCommand(context.Background(), args, &output); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
		if output.Len() != 0 {
			t.Fatal("invalid arguments printed a ticket listing")
		}
	}
	for _, path := range []string{".sdlc", "installation"} {
		if _, err := os.Stat(filepath.Join(directory, path)); !os.IsNotExist(err) {
			t.Fatalf("invalid command created %s", path)
		}
	}
	var help bytes.Buffer
	if err := workCommand(context.Background(), []string{"--help"}, &help); err != nil || !strings.Contains(help.String(), workUsage) {
		t.Fatal("missing work help", err)
	}
}

func TestWorkListsTicketsWithoutReadingBodiesOrRunningChecks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	command := exec.Command("git", "init", "--initial-branch=main", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create test repository: %v: %s", err, output)
	}
	work := filepath.Join(root, ".sdlc", "work", "Example stream", "tickets")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"10-consumer.md", "02-api.md", "100-integration.md"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("Not a structured ticket. Dependencies and specification are absent.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/.sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Malformed project settings must not prevent filename-only discovery.
	if err := os.WriteFile(filepath.Join(root, ".sdlc", "project.json"), []byte("not JSON\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	state := filepath.Join(root, "installation")
	t.Setenv("SDLC_STATE_DIR", state)
	var output bytes.Buffer
	if err := workCommand(context.Background(), []string{"--reference", "Example stream"}, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"Work reference: \"Example stream\"", "Ticket order (3):", "1. \".sdlc/work/Example stream/tickets/02-api.md\"", "2. \".sdlc/work/Example stream/tickets/10-consumer.md\"", "3. \".sdlc/work/Example stream/tickets/100-integration.md\"", "content and dependencies have not been checked", "Select one ticket with sdlc run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("work accessed installation state")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "info", "sdlc-init.lock")); !os.IsNotExist(err) {
		t.Fatal("work initialized project state")
	}
}
