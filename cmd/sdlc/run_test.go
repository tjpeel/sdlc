package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func runArgs(extra ...string) []string {
	return append([]string{"--reference", "TASK-1", "--ticket", "01-selected.md"}, extra...)
}
func TestRunOptionsDefaultsRepeatableInputsAndDockerOptIn(t *testing.T) {
	options, err := parseRunOptions(runArgs("--input", "spec.md", "--input", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	if options.provider != "codex" || options.base != "main" || options.timeout != 2*time.Hour || options.dockerTests || !reflect.DeepEqual(options.inputs, selectedInputs{"spec.md", "design.md"}) {
		t.Fatalf("unexpected defaults: %+v", options)
	}
	options, err = parseRunOptions(runArgs("--provider", "claude", "--docker-tests", "--timeout", "1m"))
	if err != nil || !options.dockerTests || options.provider != "claude" || options.timeout != time.Minute {
		t.Fatalf("explicit options: %+v %v", options, err)
	}
}
func TestRunOptionsRejectInvalidAndResumeOverrides(t *testing.T) {
	cases := [][]string{nil, {"--reference", "TASK-1"}, runArgs("extra"), runArgs("--provider", "other"), runArgs("--input", ""), runArgs("--answer-file", "answer.txt"), runArgs("--timeout", "59s"), runArgs("--timeout", "25h"), runArgs("--timeout", "invalid")}
	for _, name := range []string{"provider", "model", "effort", "review-model", "review-effort", "base", "branch", "repo", "input"} {
		cases = append(cases, runArgs("--resume", "recorded", "--"+name, "override"))
	}
	cases = append(cases, runArgs("--resume", "recorded", "--docker-tests"))
	for _, args := range cases {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := parseRunOptions(runArgs("--resume", "recorded", "--answer-file", "answer.txt", "--timeout", "24h", "--dry-run")); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRunOptions([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help=%v", err)
	}
}
func TestRunHelpCreatesNoState(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	state := filepath.Join(directory, "state")
	t.Setenv("SDLC_STATE_DIR", state)
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), runUsage) {
		t.Fatalf("help=%s error=%v", output.String(), err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("help created installation state")
	}
}
func runGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Example User", "-c", "user.email=example@example.invalid"}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v: %s", err, output)
		}
	}
	git("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/.sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{"README.md": "Example repository\n", "spec.md": "Selected requirement\n", ".sdlc/project.json": `{"version":1,"checks":[["go","test","./..."]],"input_files":["README.md"]}`, ".sdlc/work/TASK-1/tickets/01-selected.md": "# Selected ticket\n", ".sdlc/work/TASK-1/tickets/02-other.md": "# Other ticket\n"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "README.md", "spec.md", ".sdlc/project.json")
	git("commit", "-m", "Create disposable fixture")
	t.Chdir(root)
	t.Setenv("SDLC_STATE_DIR", filepath.Join(root, "private-state"))
	return root
}
func forbidConnectedRunCommands(t *testing.T, root string) string {
	t.Helper()
	directory := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "called")
	for _, name := range []string{"docker", "gh", "codex", "claude"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nprintf called > \"$(dirname \"$0\")/called\"\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}
func TestRunDryRunIsOfflineAndPreservesProviderRoles(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := runGitFixture(t)
			marker := forbidConnectedRunCommands(t, root)
			t.Setenv("CI", "true")
			var output bytes.Buffer
			if err := runCommand(context.Background(), runArgs("--provider", provider, "--repo", "example/project", "--branch", "work/TASK-1", "--input", "spec.md", "--input", "spec.md", "--input", "README.md", "--dry-run"), &output); err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(output.String(), "\n", 2)
			if len(parts) != 2 || !strings.Contains(parts[0], "Offline plan") {
				t.Fatalf("missing offline notice: %s", output.String())
			}
			var result struct {
				State string       `json:"state"`
				Plan  workrun.Plan `json:"plan"`
			}
			if err := json.Unmarshal([]byte(parts[1]), &result); err != nil {
				t.Fatal(err)
			}
			want := workrun.DefaultModels().Codex
			if provider == "claude" {
				want = workrun.DefaultModels().Claude
			}
			if result.State != "prepared" || result.Plan.Roles != want || result.Plan.Ticket != ".sdlc/work/TASK-1/tickets/01-selected.md" || result.Plan.Branch != "work/TASK-1" || result.Plan.DockerTests {
				t.Fatalf("selection=%+v", result)
			}
			wantInputs := []workrun.Input{{Path: result.Plan.Ticket}, {Path: "spec.md"}, {Path: "README.md"}}
			wantChecks := []workrun.Input{{Path: "README.md"}}
			if !reflect.DeepEqual(result.Plan.Inputs, wantInputs) || !reflect.DeepEqual(result.Plan.CheckInputs, wantChecks) {
				t.Fatalf("selected provider/check inputs omitted or misclassified: %+v", result.Plan)
			}
			for _, body := range []string{"Selected requirement", "Example repository", "Selected ticket", "Other ticket"} {
				if strings.Contains(output.String(), body) {
					t.Fatalf("offline plan disclosed input body %q", body)
				}
			}
			for _, path := range []string{marker, filepath.Join(root, "private-state")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("offline plan touched %s", path)
				}
			}
		})
	}
}
func TestRunDryRunRejectsMissingOrUnsafeExplicitInputs(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	if err := os.Symlink(filepath.Join(root, "spec.md"), filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"missing.md", "../spec.md", filepath.Join(root, "spec.md"), "linked.md", ".git/config"} {
		var output bytes.Buffer
		if err := runCommand(context.Background(), runArgs("--repo", "example/project", "--input", path, "--dry-run"), &output); err == nil {
			t.Fatalf("accepted explicit input %q", path)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid input contacted connected command")
	}
}
