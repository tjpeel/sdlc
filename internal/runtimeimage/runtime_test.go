package runtimeimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

var oldImage = "sha256:" + strings.Repeat("a", 64)
var newImage = "sha256:" + strings.Repeat("b", 64)

type fakeDocker struct {
	endpoint, engine, osType, current string
	buildFailed, probeFailed, active  bool
	builds                            int
	context                           string
	removed                           []string
}

func (docker *fakeDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "context":
		return []byte(fmt.Sprintf("%q", docker.endpoint)), nil
	case "info":
		return []byte(fmt.Sprintf(`{"id":%q,"os":%q}`, docker.engine, docker.osType)), nil
	case "ps":
		if docker.active {
			return []byte("retained-container"), nil
		}
		return nil, nil
	case "run":
		if docker.probeFailed {
			return nil, errors.New("tool startup failed")
		}
		return []byte("codex test-version\nclaude test-version\ngh test-version"), nil
	case "image":
		if args[1] == "rm" {
			docker.removed = append(docker.removed, args[2])
			return nil, nil
		}
		name := args[len(args)-1]
		if strings.HasPrefix(name, "sdlc:build-") {
			return []byte(newImage), nil
		}
		if docker.current == "" {
			return nil, errors.New("image missing")
		}
		return []byte(docker.current), nil
	}
	return nil, fmt.Errorf("unexpected Docker command: %v", args)
}

func (docker *fakeDocker) Run(_ context.Context, args ...string) error {
	switch args[0] {
	case "build":
		docker.builds++
		docker.context = args[len(args)-1]
		if docker.buildFailed {
			return errors.New("build failed")
		}
		return nil
	case "image":
		if args[1] == "tag" && args[3] == Image {
			docker.current = args[2]
			return nil
		}
	}
	return fmt.Errorf("unexpected Docker command: %v", args)
}

func fixture(t *testing.T) (Manager, *fakeDocker, string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	root := filepath.Join(t.TempDir(), "source with spaces")
	for _, name := range []string{"Dockerfile", ".dockerignore", "entrypoint.py", "bin/sdlc-job"} {
		path := filepath.Join(root, "runtime", name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	docker := &fakeDocker{endpoint: "unix:///tmp/fake-docker.sock", engine: "test-engine", osType: "linux"}
	manager := Manager{Directory: t.TempDir(), Docker: docker}
	return manager, docker, root
}

func TestBuildAndStatusUseOneImageAndSavedSource(t *testing.T) {
	manager, docker, root := fixture(t)
	state, err := manager.Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(root)
	if state.ImageID != newImage || docker.current != newImage || docker.context != filepath.Join(resolved, "runtime") {
		t.Fatal("build did not select shared runtime context and verified image")
	}
	if state.Engine != "test-engine" || state.Tools == "" {
		t.Fatal("runtime record has no engine or tool evidence")
	}
	if _, err := manager.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(context.Background(), ""); err != nil {
		t.Fatal("saved source did not permit a repeat build", err)
	}
	if docker.builds != 2 || docker.current != newImage {
		t.Fatal("repeat build did not use the same shared image")
	}
}

func TestFailedBuildOrProbePreservesPriorRuntime(t *testing.T) {
	for _, stage := range []string{"build", "probe"} {
		t.Run(stage, func(t *testing.T) {
			manager, docker, root := fixture(t)
			docker.current = oldImage
			before := State{Version: 1, Source: root, Engine: "test-engine", ImageID: oldImage}
			if err := manager.save(before); err != nil {
				t.Fatal(err)
			}
			docker.buildFailed = stage == "build"
			docker.probeFailed = stage == "probe"
			if _, err := manager.Build(context.Background(), ""); err == nil {
				t.Fatal("failed candidate was selected")
			}
			after, err := manager.read()
			if err != nil || after.ImageID != oldImage || docker.current != oldImage {
				t.Fatal("failed candidate damaged the prior runtime", err)
			}
		})
	}
}

func TestPreflightBlocksUnsupportedEnginesAndDependentContainers(t *testing.T) {
	for _, cause := range []string{"remote", "windows-containers", "dependent-container", "lock"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root := fixture(t)
			switch cause {
			case "remote":
				docker.endpoint = "ssh://example.invalid"
			case "windows-containers":
				docker.osType = "windows"
			case "dependent-container":
				docker.current, docker.active = oldImage, true
			case "lock":
				lock, err := filelock.Acquire(filepath.Join(manager.Directory, "runtime-build.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			if _, err := manager.Build(context.Background(), root); err == nil || docker.builds != 0 {
				t.Fatal("failed preflight started a build", err)
			}
		})
	}
}

func TestStatusRejectsMissingOrReplacedRuntime(t *testing.T) {
	manager, docker, root := fixture(t)
	if _, err := manager.Status(context.Background()); err == nil {
		t.Fatal("unconfigured runtime reported ready")
	}
	if _, err := manager.Build(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, replaced := range []string{"", oldImage} {
		docker.current = replaced
		if _, err := manager.Status(context.Background()); err == nil {
			t.Fatal("missing or replaced image reported ready")
		}
	}
	docker.current, docker.engine = newImage, "different-engine"
	if _, err := manager.Status(context.Background()); err == nil {
		t.Fatal("unregistered engine reported ready")
	}
}

func TestFailedStateSaveRestoresPreviousSharedTag(t *testing.T) {
	manager, docker, root := fixture(t)
	docker.current = oldImage
	if err := os.Mkdir(filepath.Join(manager.Directory, "runtime.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(context.Background(), root); err == nil {
		t.Fatal("unrecorded runtime reported ready")
	}
	if docker.current != oldImage {
		t.Fatal("failed state save left the new shared image selected")
	}
}
