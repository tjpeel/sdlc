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

func TestRunRejectsIncompleteHistoryBeforeConnectedPreparation(t *testing.T) {
	root := runGitFixture(t)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Example User", "-c", "user.email=example@example.invalid"}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v: %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	parent := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("second source commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "Second source commit")
	head := git("rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(root, ".git", "objects", parent[:2], parent[2:])); err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, root)
	var output bytes.Buffer
	err := runCommand(context.Background(), runArgs("--repo", "example/project"), &output)
	if err == nil || !strings.Contains(err.Error(), "incomplete history") {
		t.Fatalf("missing actionable history failure: %v", err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("incomplete history reached provider or Docker")
	}
	if _, err := os.Lstat(filepath.Join(root, ".sdlc", "work", "TASK-1", "runs")); !os.IsNotExist(err) {
		t.Fatal("incomplete history captured execution workspace")
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("preflight changed source HEAD")
	}
	if err := runCommand(context.Background(), runArgs("--repo", "example/project", "--dry-run", "--json"), &output); err != nil {
		t.Fatalf("offline plan unavailable: %v", err)
	}
}
