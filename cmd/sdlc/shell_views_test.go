package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	var status struct {
		Root  string           `json:"root"`
		Steps []onboardingStep `json:"steps"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := onboardCommand(context.Background(), []string{"status"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Project onboarding: ") {
		t.Fatal(out.String())
	}
	position := strings.Index(out.String(), "== Walkthrough ==")
	for i, step := range status.Steps {
		want := fmt.Sprintf("\n%d. %s\n", i+1, step.Name)
		next := strings.Index(out.String(), want)
		if next <= position || !strings.Contains(out.String(), step.Purpose) || !strings.Contains(out.String(), step.Instruction) {
			t.Fatalf("missing onboarding step %d: %q", i+1, out.String())
		}
		position = next
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestOnboardRejectsPartialProjectConfig(t *testing.T) {
	root := runGitFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".sdlc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sdlc", "project.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Steps []onboardingStep `json:"steps"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Steps[0].State, "invalid") {
		t.Fatalf("partial project configuration reported as %q", status.Steps[0].State)
	}
}

func TestOnboardDistinguishesProblemsSetupAndUnverifiedChecks(t *testing.T) {
	for _, test := range []struct{ name, config, project, checks string }{
		{"missing", "", "setup", "review"},
		{"empty check", `{"version":1,"checks":[[]],"input_files":[]}`, "problem", "review"},
		{"valid", `{"version":1,"checks":[["go","test","./..."]],"input_files":[]}`, "configured", "unverified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := runGitFixture(t)
			path := filepath.Join(root, ".sdlc/project.json")
			if test.config == "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(test.config), 0600); err != nil {
				t.Fatal(err)
			}
			marker := forbidConnectedRunCommands(t, root)
			before := dashboardTree(t, root)
			var out bytes.Buffer
			if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
				t.Fatal(err)
			}
			var status struct {
				Root  string
				Steps []onboardingStep
			}
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if len(status.Steps) != 8 || status.Steps[0].Category != test.project || status.Steps[1].Category != test.checks {
				t.Fatalf("wrong local evidence: %s", out.String())
			}
			for _, i := range []int{3, 4, 5} {
				if status.Steps[i].Category != "unverified" || status.Steps[i].State != "unchecked" {
					t.Fatalf("connected readiness claimed: %+v", status.Steps[i])
				}
			}
			if status.Steps[7].Category != "optional" || !strings.Contains(status.Steps[7].Reason, "external controller terminal") {
				t.Fatal("missing bridge made optional terminal look required")
			}
			if strings.Contains(out.String(), "\x1b") {
				t.Fatal("JSON contains ANSI styling")
			}
			out.Reset()
			if err := onboardCommand(context.Background(), []string{"status"}, &out); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Index(text, "Next:") > strings.Index(text, "== Walkthrough ==") {
				t.Fatal("useful action appears after walkthrough")
			}
			if test.project != "configured" && !strings.Contains(strings.Split(text, "== Walkthrough ==")[0], "project (step 1)") {
				t.Fatal("observed project problem missing from summary")
			}
			assertDashboardReadOnly(t, root, marker, before)
		})
	}
}

func TestOnboardWorkNeedsNumberedTicketsAndReportsReferenceErrors(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken=%t", broken), func(t *testing.T) {
			root := runGitFixture(t)
			tickets := filepath.Join(root, ".sdlc/work/TASK-1/tickets")
			if err := os.RemoveAll(tickets); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(tickets, 0700); err != nil {
				t.Fatal(err)
			}
			if broken {
				if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(tickets, "01-broken.md")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(tickets, "README.md"), []byte("reference notes"), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
				t.Fatal(err)
			}
			var status struct{ Steps []onboardingStep }
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			work := status.Steps[6]
			if broken {
				if work.Category != "problem" || !strings.Contains(work.Reason, "TASK-1:") {
					t.Fatalf("reference error hidden: %+v", work)
				}
			} else if work.Category != "problem" || !strings.Contains(work.Reason, "no numbered Markdown ticket files") {
				t.Fatalf("reference treated as tickets: %+v", work)
			}
		})
	}
}

func TestOnboardReportsInvalidAndUnreadableLocalEvidence(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%t", linked), func(t *testing.T) {
			root := runGitFixture(t)
			dir := os.Getenv("SDLC_STATE_DIR")
			if err := os.MkdirAll(filepath.Join(dir, "terminal"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{filepath.Join(dir, "runtime.json"), filepath.Join(dir, "terminal/ready.json")} {
				if linked {
					if err := os.Symlink(filepath.Join(root, "README.md"), path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(`{"version":99}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
				t.Fatal(err)
			}
			var status struct{ Steps []onboardingStep }
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			for _, i := range []int{2, 7} {
				if status.Steps[i].Category != "problem" || status.Steps[i].Reason == "" {
					t.Fatalf("invalid local evidence treated as missing: %+v", status.Steps[i])
				}
			}
		})
	}
}

func TestOnboardRecordedRuntimeIsUnverifiedAndBadIdentityIsAProblem(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid=%t", valid), func(t *testing.T) {
			runGitFixture(t)
			dir := os.Getenv("SDLC_STATE_DIR")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			id, category := "not-an-image", "problem"
			if valid {
				id, category = "sha256:"+strings.Repeat("a", 64), "unverified"
			}
			data := fmt.Sprintf(`{"version":1,"image_id":%q}`, id)
			if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := onboardCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
				t.Fatal(err)
			}
			var status struct{ Steps []onboardingStep }
			if err := json.Unmarshal(out.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			runtime := status.Steps[2]
			if runtime.Category != category || !strings.Contains(runtime.Instruction, "runtime status --offline") {
				t.Fatalf("runtime record gave wrong readiness/action: %+v", runtime)
			}
		})
	}
}

func TestVersionDetailsOnlyFlagsConfirmedIdentityDifferences(t *testing.T) {
	v := versionView{}
	v.Running.Version, v.Running.Revision = "1.2", "abc"
	v.Installed.Version, v.Installed.Revision = "unknown", "unknown"
	v.Source.Version, v.Source.Revision = "not recorded", "unknown"
	var out bytes.Buffer
	if err := writeVersionDetails(&out, v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "[problem]") || !strings.Contains(out.String(), "[unverified]") {
		t.Fatalf("unknown identity treated as mismatch: %s", out.String())
	}
	v.Installed.Version, v.Installed.Revision = "1.1", "abc"
	out.Reset()
	if err := writeVersionDetails(&out, v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[problem] Running/PATH version differs: 1.2 / 1.1") || strings.Contains(out.String(), "revision differs") {
		t.Fatalf("confirmed mismatch lost: %s", out.String())
	}
}

func TestVersionDetailsReportsUnavailableStateDirectory(t *testing.T) {
	runGitFixture(t)
	t.Setenv("SDLC_STATE_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if _, err := shellStateDirectory(); err == nil {
		t.Fatal("fixture did not make the state directory unavailable")
	}
	var out bytes.Buffer
	if err := versionDetailsCommand(context.Background(), []string{"--details", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var status versionView
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RuntimeCategory != "problem" || !strings.Contains(status.RuntimeStatus, "unavailable") {
		t.Fatalf("state directory error shown as missing evidence: %s", out.String())
	}
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
