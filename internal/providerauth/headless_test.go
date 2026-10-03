package providerauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

type streamDocker struct {
	*fakeDocker
	stream func(context.Context, io.Reader, io.Writer, io.Writer, []string) error
}

func (docker streamDocker) Stream(ctx context.Context, in io.Reader, out, diagnostics io.Writer, args ...string) error {
	return docker.stream(ctx, in, out, diagnostics, args)
}
func headlessFixture(t *testing.T) HeadlessRequest {
	t.Helper()
	directory := t.TempDir()
	request := HeadlessRequest{Provider: "codex", Model: "example-model", Effort: "high", Workspace: filepath.Join(directory, "workspace"), SessionDirectory: filepath.Join(directory, "session"), PromptFile: filepath.Join(directory, "prompt"), SchemaFile: filepath.Join(directory, "schema")}
	for _, path := range []string{request.Workspace, request.SessionDirectory} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{request.PromptFile, request.SchemaFile} {
		if err := os.WriteFile(path, []byte("{}"), 0444); err != nil {
			t.Fatal(err)
		}
	}
	return request
}
func TestHeadlessSelectedAuthenticationAndMountModes(t *testing.T) {
	for _, provider := range Providers {
		for _, readonly := range []bool{false, true} {
			t.Run(provider+map[bool]string{true: "review", false: "implementation"}[readonly], func(t *testing.T) {
				manager, docker := readyInteractive(t, provider)
				request := headlessFixture(t)
				request.Provider = provider
				request.ReadOnly = readonly
				if err := os.Mkdir(filepath.Join(request.Workspace, ".codex"), 0700); err != nil {
					t.Fatal(err)
				}
				var out, diagnostics bytes.Buffer
				manager.Docker = streamDocker{docker, func(ctx context.Context, in io.Reader, stdout, stderr io.Writer, args []string) error {
					if in != nil || has(args, "--tty") || !has(args, testImage) || !has(args, "--interactive") {
						t.Fatal("wrong stream or runtime settings")
					}
					for _, required := range []string{"--init", "bridge", "1000:1000", "--read-only", "no-new-privileges", "HTTP_PROXY="} {
						if !has(args, required) {
							t.Fatalf("missing isolation %s", required)
						}
					}
					lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
					if err == nil {
						lock.Close()
						t.Fatal("runtime lease released during model execution")
					}
					counts := map[string]int{}
					for _, mount := range mounts(t, args) {
						counts[mount["dst"]]++
						_, ro := mount["readonly"]
						switch mount["dst"] {
						case "/workspace":
							if ro != readonly {
								t.Fatal("workspace has wrong review mode")
							}
						case "/session":
							if ro {
								t.Fatal("session is readonly")
							}
						case "/prompt.txt", "/schema.json":
							if !ro {
								t.Fatal("input writable")
							}
						case "/provider-auth":
							if !strings.HasSuffix(mount["src"], "-"+provider) {
								t.Fatal("wrong account cache")
							}
						default:
							t.Fatal("unexpected mount")
						}
					}
					for _, destination := range []string{"/workspace", "/session", "/prompt.txt", "/schema.json", "/provider-auth"} {
						if counts[destination] != 1 {
							t.Fatal("missing or duplicate mount")
						}
					}
					io.WriteString(stdout, "{\"type\":\"native-event\"}\n")
					io.WriteString(stderr, "private native diagnostic")
					return nil
				}}
				if err := manager.Headless(context.Background(), request, &out, &diagnostics); err != nil {
					t.Fatal(err)
				}
				if out.String() != "{\"type\":\"native-event\"}\n" || diagnostics.String() != "private native diagnostic" {
					t.Fatal("native streams changed")
				}
				if len(docker.volumes) != 1 {
					t.Fatal("transport changed account caches")
				}
			})
		}
	}
}
func TestHeadlessUnavailableAuthNeverStartsModel(t *testing.T) {
	for _, state := range []string{"identity", "missing", "invalid"} {
		t.Run(state, func(t *testing.T) {
			manager, docker := fixture(t)
			if state != "identity" {
				id, err := manager.identity(true)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := manager.volume(context.Background(), id, "codex", true); err != nil {
					t.Fatal(err)
				}
				docker.result = state
			}
			manager.Docker = streamDocker{docker, func(context.Context, io.Reader, io.Writer, io.Writer, []string) error {
				t.Fatal("model started without auth")
				return nil
			}}
			if err := manager.Headless(context.Background(), headlessFixture(t), nil, nil); err == nil {
				t.Fatal("unavailable login accepted")
			}
		})
	}
}
func TestHeadlessRejectsUnsafeRequestsBeforeDocker(t *testing.T) {
	for _, kind := range []string{"provider", "relative", "resume", "nested", "extension"} {
		t.Run(kind, func(t *testing.T) {
			manager, docker := fixture(t)
			request := headlessFixture(t)
			switch kind {
			case "provider":
				request.Provider = "other"
			case "relative":
				request.PromptFile = "prompt"
			case "resume":
				request.ResumeID = "--last"
			case "nested":
				request.SessionDirectory = request.Workspace
			case "extension":
				if err := os.Symlink(request.SessionDirectory, filepath.Join(request.Workspace, ".codex")); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Headless(context.Background(), request, nil, nil); err == nil {
				t.Fatal("unsafe request accepted")
			}
			if len(docker.calls) != 0 {
				t.Fatal("unsafe request reached Docker")
			}
		})
	}
}
func TestHeadlessCancellationCleansWithoutLeakingDiagnostics(t *testing.T) {
	manager, docker := readyInteractive(t, "codex")
	ctx, cancel := context.WithCancel(context.Background())
	manager.Docker = streamDocker{docker, func(context.Context, io.Reader, io.Writer, io.Writer, []string) error {
		docker.leftover = true
		cancel()
		return errors.New("fake-secret")
	}}
	err := manager.Headless(ctx, headlessFixture(t), nil, nil)
	if !errors.Is(err, context.Canceled) || docker.leftover || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("cancellation failed cleanup or disclosed diagnostics")
	}
}
func TestHeadlessCleanupFailureStopsRun(t *testing.T) {
	manager, docker := readyInteractive(t, "codex")
	manager.Docker = streamDocker{docker, func(context.Context, io.Reader, io.Writer, io.Writer, []string) error {
		docker.cleanupFailed = true
		return nil
	}}
	err := manager.Headless(context.Background(), headlessFixture(t), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("cleanup failure missing or disclosed")
	}
}

func TestLocalDockerStreamStopsContainerBeforeClientExits(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "docker")
	script := `#!/bin/sh
if [ "$1" = stop ]; then
  if [ "$2" != --time ] || [ "$3" != 8 ] || [ "$4" != sdlc-test-headless ]; then exit 9; fi
  touch "$SDLC_TEST_STOPPED"
  exit 0
fi
if [ "$1" = run ]; then
  touch "$SDLC_TEST_STARTED"
  while [ ! -f "$SDLC_TEST_STOPPED" ]; do sleep 0.01; done
  printf '{"type":"native-event"}\n'
  printf 'private native stderr\n' >&2
  exit 0
fi
exit 9
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(directory, "started")
	stopped := filepath.Join(directory, "stopped")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SDLC_TEST_STARTED", started)
	t.Setenv("SDLC_TEST_STOPPED", stopped)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var stdout, stderr bytes.Buffer
	err := (LocalDocker{}).Stream(ctx, nil, &stdout, &stderr, "run", "--name", "sdlc-test-headless")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("missing cancellation: %v", err)
	}
	if _, err := os.Stat(stopped); err != nil {
		t.Fatal("Docker stop was not invoked")
	}
	if !strings.Contains(stdout.String(), "native-event") || !strings.Contains(stderr.String(), "private native stderr") {
		t.Fatal("native streams lost during orderly stop")
	}
}
