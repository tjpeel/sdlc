package workrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRunDirectoryRejectsAmbiguousAndSymlinkedRuns(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const first = "abc100000000000000000001"
	const second = "abc200000000000000000002"
	directory, err := RunDirectory(root, "TASK-1", "01-work.md", first, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := RunDirectory(root, "TASK-1", "01-work.md", second, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRunDirectory(root, "TASK-1", "01-work.md", "abc"); err == nil || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if got, err := ResolveRunDirectory(root, "TASK-1", "01-work.md", "abc1"); err != nil || got != directory {
		t.Fatalf("unique prefix: %q %v", got, err)
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), directory); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRunDirectory(root, "TASK-1", "01-work.md", "abc"); err == nil {
		t.Fatal("symlinked run accepted")
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(directory)
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRunDirectory(root, "TASK-1", "01-work.md", "abc"); err == nil {
		t.Fatal("symlinked ticket namespace accepted")
	}
	if _, err := RunDirectory(root, "TASK-1", "01-work.md", "abc", true); err == nil {
		t.Fatal("canonical directory accepted prefix")
	}
}
