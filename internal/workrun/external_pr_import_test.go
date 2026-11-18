package workrun

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalPRImportChecksMergeParentsAndResumes(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateGit(t, root, "config", "user.name", "Test")
	aggregateGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "add", "file")
	aggregateGit(t, root, "commit", "-m", "base")
	parent := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD^{tree}"))
	aggregateGit(t, root, "checkout", "-b", "incoming")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("incoming\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "protected"), []byte("incoming\n"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "add", "protected")
	aggregateGit(t, root, "commit", "-am", "incoming")
	incoming := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	aggregateGit(t, root, "checkout", "main")
	aggregateGit(t, root, "merge", "--no-ff", "-m", "merge", "incoming")
	merged := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	aggregateGit(t, root, "update-ref", "refs/sdlc/snapshot", merged)
	bundle := filepath.Join(t.TempDir(), "snapshot.bundle")
	aggregateGit(t, root, "bundle", "create", bundle, "refs/sdlc/snapshot")
	aggregateGit(t, root, "reset", "--hard", parent)
	helper := strings.ReplaceAll(externalPRImportHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
	helper = strings.ReplaceAll(helper, "safe.directory=/workspace", "safe.directory="+root)
	helper = strings.ReplaceAll(helper, "/newbase.bundle", bundle)
	run := func(second string) error {
		return exec.Command("python3", "-c", helper, parent, tree, parent, second, merged, "main").Run()
	}
	if err := run(strings.Repeat("a", 40)); err == nil {
		t.Fatal("accepted wrong merge parent")
	}
	if got := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD")); got != parent {
		t.Fatal("invalid import changed head")
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/protected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "protected"), []byte("private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(incoming); err == nil {
		t.Fatal("overwrote ignored local file")
	}
	if data, err := os.ReadFile(filepath.Join(root, "protected")); err != nil || string(data) != "private\n" {
		t.Fatal("ignored local file changed")
	}
	if err := os.Remove(filepath.Join(root, "protected")); err != nil {
		t.Fatal(err)
	}
	if err := run(incoming); err != nil {
		t.Fatal(err)
	}
	// Recovery after checkout completed but before the branch ref was updated.
	aggregateGit(t, root, "checkout", "--detach", merged)
	aggregateGit(t, root, "update-ref", "refs/heads/main", parent, merged)
	if err := run(incoming); err != nil {
		t.Fatal("repeat import failed:", err)
	}
	if got := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD")); got != merged {
		t.Fatal("merge was not adopted")
	}
	if got := strings.TrimSpace(aggregateGit(t, root, "branch", "--show-current")); got != "main" {
		t.Fatal("import did not preserve the working branch")
	}
}
