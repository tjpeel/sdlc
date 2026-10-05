package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestShellNormalisesLocatorsWithoutReinterpretingValues(t *testing.T) {
	tests := []struct {
		args, want []string
		bad        bool
	}{
		{[]string{"run", "--ticket", "@Example stream/01-first.md"}, []string{"run", "--ticket", "01-first.md", "--reference", "Example stream"}, false},
		{[]string{"run", "--ticket=@Example/01-first.md", "--reference=Example"}, []string{"run", "--ticket=01-first.md", "--reference=Example"}, false},
		{[]string{"run", "--input", "--ticket", "--reference", "Example", "--ticket", "01-first.md"}, []string{"run", "--input", "--ticket", "--reference", "Example", "--ticket", "01-first.md"}, false},
		{[]string{"run", "--reference", "Other", "--ticket", "@Example/01-first.md"}, nil, true},
		{[]string{"run", "--ticket", "@Example/nested/01-first.md"}, nil, true},
	}
	for _, test := range tests {
		got, err := normaliseShellArgs(test.args)
		if (err != nil) != test.bad || (!test.bad && !reflect.DeepEqual(got, test.want)) {
			t.Fatalf("%v -> %v (%v)", test.args, got, err)
		}
	}
	args := []string{"run", "--input", "--json", "--input", "--dry-run", "--reference", "Example", "--ticket", "01-first.md", "--json", "--dry-run"}
	key := planKey("/example", args)
	for _, literal := range []string{"--json", "--dry-run"} {
		if !strings.Contains(key, literal) {
			t.Fatalf("literal input lost: %s", key)
		}
	}
	want := []string{"--input", "--json", "--reference", "Example", "--ticket", "01-first.md"}
	got := controllerArgs([]string{"--input", "--json", "--reference", "Example", "--ticket", "01-first.md", "--terminal=background", "--launch-id", "example", "--json"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("controller args: %v", got)
	}
}

func TestStructuredPlansAndShellPreviewAreOffline(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	before := dashboardTree(t, root)
	args := runArgs("--repo", "example/project", "--dry-run", "--json")
	var output bytes.Buffer
	if err := runCommand(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version int
		Mode    string
		Offline bool
		Plan    workrun.Plan
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || !result.Offline || result.Mode != "ticket" || result.Plan.Reference != "TASK-1" {
		t.Fatalf("%s", output.String())
	}
	adapter := &shellAdapter{plans: map[string]string{}}
	if _, err := adapter.read(context.Background(), root, append([]string{"run"}, runArgs("--repo", "example/project")...)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.launch(context.Background(), root, append([]string{"run"}, args...)); err == nil || !strings.Contains(err.Error(), "preview only") {
		t.Fatalf("dry-run launch: %v", err)
	}
	for _, argset := range [][]string{{"run", "--reference", "TASK-1", "--all", "--dry-run"}, {"run", "--reference", "TASK-1", "--ticket", "01-selected.md", "--dry-run"}} {
		if _, err := adapter.launch(context.Background(), root, argset); err == nil {
			t.Fatal("explicit dry run dispatched")
		}
	}
	if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatal("plan created installation state", err)
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestShellLaunchRequiresCurrentReview(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	adapter := &shellAdapter{plans: map[string]string{}}
	args := append([]string{"run"}, runArgs("--repo", "example/project")...)
	if _, err := adapter.launch(context.Background(), root, args); err == nil || !strings.Contains(err.Error(), "not reviewed") {
		t.Fatalf("unreviewed: %v", err)
	}
	if _, err := adapter.read(context.Background(), root, args); err != nil {
		t.Fatal(err)
	}
	// A settings change affects the reviewed plan without reading ticket bodies.
	if err := os.WriteFile(filepath.Join(root, ".sdlc/project.json"), []byte(`{"version":1,"checks":[["go","vet","./..."]],"input_files":["README.md"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.launch(context.Background(), root, args); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview connected", err)
	}
	if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatal("stale preview dispatched", err)
	}
}

func TestDashboardProjectScopeUsesCurrentCheckout(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repoRoot := journals[0].Plan.Root
	if data, err := exec.Command("git", "init", "--initial-branch=main", repoRoot).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, data)
	}
	t.Chdir(repoRoot)
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--scope", "project", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), journals[0].ID) || strings.Contains(output.String(), journals[1].ID) {
		t.Fatalf("scope output: %s", output.String())
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestShellCompletionDoesNotReadTicketBodies(t *testing.T) {
	root := runGitFixture(t)
	if err := os.WriteFile(filepath.Join(root, ".sdlc/work/TASK-1/tickets/01-selected.md"), []byte("\x00\xff private example"), 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, root)
	suggestions, err := shellCompletion(context.Background(), root, "/run --ticket @TASK-1/01")
	if err != nil || len(suggestions) != 1 || !strings.Contains(suggestions[0].Insert, "@TASK-1/01-selected.md") {
		t.Fatalf("%+v %v", suggestions, err)
	}
	if strings.Contains(suggestions[0].Description, "private example") {
		t.Fatal("body in completion")
	}
	if after := dashboardTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("completion wrote checkout")
	}
}

func TestShellRejectsRedirectedIOWithoutState(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("SDLC_STATE_DIR", filepath.Join(directory, "state"))
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.Create(filepath.Join(directory, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := shellCommand(context.Background(), nil, input, output); err == nil || !strings.Contains(err.Error(), "requires terminal") {
		t.Fatalf("nonTTY: %v", err)
	}
	if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatal("shell created state")
	}
	adapter := &shellAdapter{executable: "must-not-execute"}
	for _, args := range [][]string{{"launch", "execute", "--id", "example"}, {"run", "--reference", "Example", "--ticket", "01-first.md"}, {"unknown"}} {
		if command, err := adapter.execute(context.Background(), directory, args); err == nil || command != nil {
			t.Fatalf("unsupported native command accepted: %v", args)
		}
	}
}
