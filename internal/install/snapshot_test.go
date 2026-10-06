package install

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
)

func snapshotRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "sdlc"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"go.mod":           "module github.com/tjpeel/sdlc\n\ngo 1.25.0\n",
		"cmd/sdlc/main.go": "package main\nfunc main() {}\n",
		"README.md":        "initial\n",
		".gitignore":       "ignored\n",
		".gitattributes":   "README.md filter=example diff=example\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	snapshotRun(t, root, "init", "--initial-branch=main")
	snapshotRun(t, root, "config", "user.name", "Disposable Test")
	snapshotRun(t, root, "config", "user.email", "test@example.invalid")
	snapshotRun(t, root, "add", ".")
	snapshotRun(t, root, "-c", "commit.gpgsign=false", "commit", "-m", "Create disposable source")
	return root
}

func snapshotRun(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.SplitN(entry, "=", 2)[0], "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestCommittedSourceAcceptsMetadataOnlyChanges(t *testing.T) {
	root := snapshotRepo(t)
	path := filepath.Join(root, "README.md")
	stamp := time.Unix(1000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	identity, err := project.InspectIdentity(context.Background(), root)
	if err != nil || !identity.Dirty {
		t.Fatalf("fixture must exercise conservative dirty metadata: %+v, %v", identity, err)
	}
	canonical, _ := filepath.EvalSymlinks(root)
	indexPath := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateCommittedSource(context.Background(), root)
	if err != nil || got != canonical {
		t.Fatalf("unchanged committed bytes rejected: %q, %v", got, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection changed the Git index: %v", err)
	}
}

func TestCommittedSourceRejectsChangedBytesWithPreservedMetadata(t *testing.T) {
	root := snapshotRepo(t)
	path := filepath.Join(root, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// An index flag can hide worktree changes from Git's ordinary status checks.
	snapshotRun(t, root, "update-index", "--assume-unchanged", "README.md")
	if err := os.WriteFile(path, []byte("changed\n"), info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCommittedSource(context.Background(), root); err == nil || !strings.Contains(err.Error(), "uncommitted content") {
		t.Fatalf("changed source bytes accepted: %v", err)
	}
}

func TestCommittedSourceRejectsStagedUntrackedAndSymlinkChanges(t *testing.T) {
	for _, change := range []string{"staged", "untracked", "symlink", "mode"} {
		t.Run(change, func(t *testing.T) {
			root := snapshotRepo(t)
			path := filepath.Join(root, "README.md")
			switch change {
			case "staged":
				if err := os.WriteFile(path, []byte("changed\n"), 0644); err != nil {
					t.Fatal(err)
				}
				snapshotRun(t, root, "add", "README.md")
			case "untracked":
				if err := os.WriteFile(filepath.Join(root, "untracked"), []byte("untracked\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("go.mod", path); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ValidateCommittedSource(context.Background(), root); err == nil {
				t.Fatalf("%s change accepted", change)
			}
		})
	}
}

func TestCommittedSourceDoesNotExecuteSourceHelpers(t *testing.T) {
	root := snapshotRepo(t)
	marker := filepath.Join(t.TempDir(), "called")
	hook := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf called > '"+marker+"'\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{"core.fsmonitor", "filter.example.clean", "filter.example.smudge", "diff.example.textconv"} {
		snapshotRun(t, root, "config", config, hook)
	}
	// Repository-discovery environment must not redirect inspection elsewhere.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "missing-index"))
	if err := os.WriteFile(filepath.Join(root, "ignored"), []byte("ignored\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCommittedSource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("source inspection executed a Git helper")
	}
}
