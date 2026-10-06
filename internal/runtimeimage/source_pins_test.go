package runtimeimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func TestSourcePinsReplacePrivateOverridesOnlyAfterSuccessfulBuild(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			manager, docker, root, pins, original := pinsFixture(t)
			before := recordPrevious(t, manager, docker, root, &pins)
			docker.buildFailed = failed
			state, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{SourcePins: true})
			if failed {
				if err == nil {
					t.Fatal("failed build succeeded")
				}
				assertPrevious(t, manager, docker, before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.DependencyPins != nil || string(docker.buildRecipe) != string(original) || !strings.Contains(state.BuildRecipe, "ARG CODEX_VERSION=0.0.1") {
				t.Fatal("source pins were overridden")
			}
		})
	}
}

func TestSourcePinsRejectExplicitOverrides(t *testing.T) {
	manager, docker, root, pins, _ := pinsFixture(t)
	if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{SourcePins: true, Pins: &pins}); err == nil {
		t.Fatal("conflicting policy accepted")
	}
	if len(docker.calls) != 0 {
		t.Fatal("conflicting policy reached Docker")
	}
}

func TestPreviousValidationHoldsWriterLockBeforeDocker(t *testing.T) {
	manager, docker, root, pins, _ := pinsFixture(t)
	before := recordPrevious(t, manager, docker, root, &pins)
	called := false
	sentinel := errors.New("saved work must finish")
	_, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{ValidatePrevious: func(_ context.Context, previous State) error {
		called = true
		if previous.ImageID != oldImage {
			t.Fatal("wrong previous state")
		}
		lock, err := filelock.Acquire(filepath.Join(manager.Directory, "runtime-build.lock"))
		if err == nil {
			lock.Close()
			t.Fatal("writer lock not held")
		}
		if len(docker.calls) != 0 {
			t.Fatal("Docker touched before validation")
		}
		return sentinel
	}})
	if !called || !errors.Is(err, sentinel) {
		t.Fatal("validation ignored", err)
	}
	assertPrevious(t, manager, docker, before)
}

func TestPreviousValidationRejectsCorruptRecordedRuntimeBeforeDocker(t *testing.T) {
	manager, docker, root := fixture(t)
	if err := os.WriteFile(filepath.Join(manager.Directory, "runtime.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{SourcePins: true, ValidatePrevious: func(context.Context, State) error { called = true; return nil }})
	if err == nil || called || len(docker.calls) != 0 {
		t.Fatal("corrupt recorded state bypassed saved-work protection", err)
	}
}

func TestPreviousValidationAllowsMissingRuntimeBootstrap(t *testing.T) {
	manager, _, root := fixture(t)
	called := false
	if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{ValidatePrevious: func(context.Context, State) error { called = true; return nil }}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("previous validation ran without a previous runtime")
	}
}

func TestPreviousValidationProtectsExistingTagWhenRecordIsMissing(t *testing.T) {
	manager, docker, root := fixture(t)
	docker.current = oldImage
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("pending human work uses inspected image")
	called := false
	_, err = manager.BuildWithOptions(context.Background(), root, BuildOptions{ValidatePrevious: func(_ context.Context, previous State) error {
		called = true
		if previous.ImageID != oldImage || previous.Engine != docker.engine || previous.Source != root || previous.Version != 0 {
			t.Fatal("missing record did not use inspected identity")
		}
		lock, err := filelock.Acquire(filepath.Join(manager.Directory, "runtime-build.lock"))
		if err == nil {
			lock.Close()
			t.Fatal("writer lock not held")
		}
		return sentinel
	}})
	if !called || !errors.Is(err, sentinel) || docker.builds != 0 || len(docker.removed) != 0 || docker.current != oldImage {
		t.Fatal("missing record allowed replacement", err)
	}
	for _, call := range docker.calls {
		if call[0] == "pull" || call[0] == "run" {
			t.Fatal("saved-work validation ran after image mutation", call)
		}
	}
}
