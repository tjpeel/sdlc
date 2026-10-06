package runtimeimage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveBuildRecordsSuppliedSourceRevision(t *testing.T) {
	manager, docker, source := fixture(t)
	revision := strings.Repeat("c", 40)
	state, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SourceRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != revision || docker.builds != 1 {
		t.Fatalf("archive provenance missing: %+v", state)
	}
	saved, err := manager.Status(context.Background())
	if err != nil || saved.Revision != revision {
		t.Fatalf("archive revision not retained: %+v %v", saved, err)
	}
}

func TestInvalidArchiveRevisionStopsBeforeDockerOrStateWrites(t *testing.T) {
	for _, revision := range []string{"short", strings.Repeat("a", 39), strings.Repeat("z", 40), strings.Repeat("a", 40) + "-dirty"} {
		t.Run(revision, func(t *testing.T) {
			manager, docker, source := fixture(t)
			_, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SourceRevision: revision})
			if err == nil || !strings.Contains(err.Error(), "full Git commit ID") || len(docker.calls) != 0 {
				t.Fatalf("invalid archive revision reached Docker: %v, calls=%v", err, docker.calls)
			}
			if _, err := os.Stat(filepath.Join(manager.Directory, "runtime.json")); !os.IsNotExist(err) {
				t.Fatal("invalid archive revision wrote state", err)
			}
		})
	}
}

func TestCheckoutRevisionTakesPriorityOverArchiveProvenance(t *testing.T) {
	manager, _, source := fixture(t)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", source}, args...)...)
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("disposable checkout setup: %v\n%s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init")
	git("add", ".")
	git("-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Disposable source")
	revision := git("rev-parse", "HEAD")
	state, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SourceRevision: strings.Repeat("c", 40)})
	if err != nil || state.Revision != revision {
		t.Fatalf("checkout identity overridden: %+v %v", state, err)
	}
}

func TestArchiveRevisionDoesNotInheritAncestorCheckout(t *testing.T) {
	manager, _, source := fixture(t)
	ancestor := filepath.Dir(source)
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Disposable package manager"}} {
		command := exec.Command("git", append([]string{"-C", ancestor}, args...)...)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("ancestor checkout setup: %v\n%s", err, data)
		}
	}
	revision := strings.Repeat("c", 40)
	state, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SourceRevision: revision})
	if err != nil || state.Revision != revision {
		t.Fatalf("archive inherited package-manager identity: %+v %v", state, err)
	}
}
