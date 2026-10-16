package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestAdoptSelectionAndOfflinePreview(t *testing.T) {
	root := runGitFixture(t)
	t.Chdir(root)
	if data, err := exec.Command("git", "remote", "add", "origin", "https://github.com/example/project.git").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", data, err)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", state)
	configureTestHeadroom(t, state)
	var out bytes.Buffer
	if err := adoptCommand(context.Background(), []string{"--pr", "https://github.com/example/project/pull/17", "--dry-run", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"remote_verified":false`) || !strings.Contains(out.String(), `"reference":"pr-17"`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".sdlc/work/pr-17")); !os.IsNotExist(err) {
		t.Fatal("offline adoption wrote work state")
	}
	for _, extra := range [][]string{{"--reference", "TASK-1"}, {"--base", "main"}, {"--branch", "other"}, {"--resume", "abc"}, {"--all"}, {"--repo", "other/project"}, {"--terminal", "background"}} {
		if _, err := parseRunOptions(append([]string{"--pr", "https://github.com/example/project/pull/17"}, extra...)); err == nil {
			t.Fatalf("accepted incompatible options: %v", extra)
		}
	}
	out.Reset()
	if err := adoptCommand(context.Background(), []string{"--pr", "https://github.com/other/project/pull/17", "--dry-run"}, &out); err == nil {
		t.Fatal("accepted PR from another project")
	}
}

func TestGeneratedAdoptionTicketIsPrivateAndNeverOverwritten(t *testing.T) {
	root := runGitFixture(t)
	const pr = "https://github.com/example/project/pull/17"
	if err := prepareAdoptionTicket(context.Background(), root, "pr-17", 17, pr); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".sdlc/work/pr-17/tickets/01-pr-comments.md")
	original, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(original), pr) {
		t.Fatal(err)
	}
	if err := prepareAdoptionTicket(context.Background(), root, "pr-17", 17, pr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user's notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAdoptionTicket(context.Background(), root, "pr-17", 17, pr); err == nil {
		t.Fatal("overwrote existing ticket")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "user's notes" {
		t.Fatal("modified user ticket")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README.md"), path); err != nil {
		t.Fatal(err)
	}
	if err := prepareAdoptionTicket(context.Background(), root, "pr-17", 17, pr); err == nil {
		t.Fatal("followed ticket symlink")
	}
}

func TestAdoptPreviewRetainsOptionalTicketWithoutConnectedCommands(t *testing.T) {
	root := runGitFixture(t)
	if data, err := exec.Command("git", "remote", "add", "origin", "https://github.com/example/project.git").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", data, err)
	}
	marker := forbidConnectedRunCommands(t, root)
	var output bytes.Buffer
	err := adoptCommand(context.Background(), []string{"--pr", "https://github.com/example/project/pull/17", "--reference", "TASK-1", "--ticket", "01-selected.md", "--provider", "claude", "--reasoning", "medium", "--dry-run", "--json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"reference":"TASK-1"`) || !strings.Contains(output.String(), `"provider":"claude"`) {
		t.Fatal(output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview invoked connected command")
	}
}

func TestAdoptionInitialFetchCanResumeThroughPublicCommand(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.Plan.SourceSHA = j.Publication.HeadSHA
	j.Plan.StartingSHA = j.Publication.HeadSHA
	j.Plan.BaseSHA = j.Publication.BaseSHA
	j.Plan.Base = "main"
	j.Plan.Branch = "feedback-fix"
	j.Plan.Adoption = &workrun.PRAdoption{URL: j.Publication.URL, Number: j.Publication.Number, Branch: j.Plan.Branch, Base: j.Plan.Base, HeadSHA: j.Publication.HeadSHA, BaseSHA: j.Publication.BaseSHA}
	j.State = "blocked"
	j.ResumeState = "adopting"
	j.SessionID = ""
	j.Attempt = 0
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"adoption"`) || !strings.Contains(output.String(), j.Publication.URL) {
		t.Fatal("resume lost PR boundary", output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline resume invoked connected command")
	}
}
