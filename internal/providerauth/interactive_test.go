package providerauth

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/instructions"
)

type sessionDocker struct {
	*fakeDocker
	attached func(context.Context, []string)
}

func (docker sessionDocker) Interactive(ctx context.Context, args ...string) error {
	if docker.attached != nil {
		docker.attached(ctx, args)
	}
	return docker.fakeDocker.Interactive(ctx, args...)
}

func readyInteractive(t *testing.T, provider string) (Manager, *fakeDocker) {
	t.Helper()
	manager, docker := fixture(t)
	if err := manager.Login(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	docker.calls = nil
	return manager, docker
}

func mounts(t *testing.T, args []string) []map[string]string {
	t.Helper()
	var result []map[string]string
	for i, arg := range args {
		if arg != "--mount" {
			continue
		}
		fields, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
		if err != nil {
			t.Fatal(err)
		}
		mount := map[string]string{}
		for _, field := range fields {
			key, value, _ := strings.Cut(field, "=")
			mount[key] = value
		}
		result = append(result, mount)
	}
	return result
}

func TestInteractiveUsesOnlySelectedCacheAndPrivateInstructionSnapshot(t *testing.T) {
	for _, provider := range Providers {
		t.Run(provider, func(t *testing.T) {
			manager, docker := readyInteractive(t, provider)
			instructionManager := instructions.Manager{Directory: manager.Runtime.Directory}
			source := filepath.Join(t.TempDir(), "additional.md")
			custom := "Run the relevant checks.\n"
			if err := os.WriteFile(source, []byte(custom), 0600); err != nil {
				t.Fatal(err)
			}
			if err := instructionManager.Set(source); err != nil {
				t.Fatal(err)
			}
			var snapshot string
			attached := false
			manager.Docker = sessionDocker{docker, func(ctx context.Context, args []string) {
				attached = true
				if _, finite := ctx.Deadline(); finite {
					t.Fatal("interactive work received a login timeout")
				}
				for _, required := range []string{testImage, "--rm", "never", "--read-only", "ALL", "no-new-privileges", "1000:1000", "--init", "--interactive", "--tty", "bridge", "none", "/workspace", "HTTP_PROXY="} {
					if !has(args, required) {
						t.Fatalf("session missing isolation setting %s", required)
					}
				}
				for _, forbidden := range []string{"--privileged", "--cap-add", "--publish", "--volume", "--pid", "--device"} {
					if has(args, forbidden) {
						t.Fatalf("session has unsafe setting %s", forbidden)
					}
				}
				if args[len(args)-3] != provider || args[len(args)-2] != "interactive" {
					t.Fatal("wrong provider or helper action")
				}
				wantedMode, _ := InteractiveMode(provider, "")
				if args[len(args)-1] != wantedMode {
					t.Fatal("session did not receive full-access default")
				}
				if strings.Contains(strings.Join(args, "\n"), custom) {
					t.Fatal("private instructions were placed in Docker arguments")
				}
				binds := 0
				for _, mount := range mounts(t, args) {
					switch mount["type"] {
					case "volume":
						if !strings.HasSuffix(mount["src"], "-"+provider) || mount["dst"] != "/provider-auth" {
							t.Fatal("another provider cache was exposed")
						}
					case "bind":
						binds++
						if _, readOnly := mount["readonly"]; !readOnly {
							t.Fatal("instructions are writable")
						}
						if provider == "claude" && (mount["dst"] == "/session-settings.json" || mount["dst"] == "/provider-auth/settings.json") {
							content, err := os.ReadFile(mount["src"])
							if err != nil || string(content) != "{\"skipDangerousModePermissionPrompt\":true}\n" {
								t.Fatal("Claude session settings differ from full-access configuration")
							}
							continue
						}
						if mount["dst"] != "/session-instructions.md" && !(provider == "claude" && mount["dst"] == "/provider-auth/CLAUDE.md") {
							t.Fatal("unexpected host bind mount")
						}
						snapshot = mount["src"]
					}
				}
				wantedBinds := 1
				if provider == "claude" {
					wantedBinds = 4
					for _, directory := range []string{"rules", "commands", "output-styles", "workflows", "agent-memory", "plans", "tasks", "file-history", "paste-cache", "debug", "plugins", "projects"} {
						present := false
						for i, arg := range args {
							if arg == "--tmpfs" && strings.HasPrefix(args[i+1], "/provider-auth/"+directory+":") {
								present = true
								break
							}
						}
						if !present {
							t.Fatalf("cached Claude work data or customization exposed: %s", directory)
						}
					}
				}
				if binds != wantedBinds || snapshot == "" {
					t.Fatal("missing instructions snapshot")
				}
				before, err := os.ReadFile(snapshot)
				if err != nil || !strings.Contains(string(before), custom) || !strings.Contains(string(before), "until a human has answered") {
					t.Fatal("incorrect shared instructions")
				}
				file, err := os.Stat(snapshot)
				if err != nil || file.Mode().Perm() != 0444 {
					t.Fatal("snapshot is not readable by the container's non-root user")
				}
				info, err := os.Stat(filepath.Dir(snapshot))
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatal("snapshot parent is not private")
				}
				if err := instructionManager.Reset(); err != nil {
					t.Fatal(err)
				}
				after, _ := os.ReadFile(snapshot)
				if string(after) != string(before) {
					t.Fatal("settings change modified active session instructions")
				}
				lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
				if err == nil {
					lock.Close()
					t.Fatal("active session did not hold the runtime lock")
				}
			}}
			if err := manager.Interactive(context.Background(), provider, ""); err != nil {
				t.Fatal(err)
			}
			if !attached {
				t.Fatal("native session was not attached")
			}
			if _, err := os.Stat(filepath.Dir(snapshot)); !os.IsNotExist(err) {
				t.Fatal("session instruction snapshot survived exit")
			}
			if len(docker.volumes) != 1 {
				t.Fatal("session created another cache")
			}
		})
	}
}

func TestNativeModeOverridesReachOnlySelectedProvider(t *testing.T) {
	for provider, modes := range map[string][]string{
		"codex":  {"never", "on-request"},
		"claude": {"default", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"},
	} {
		for _, mode := range modes {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				manager, docker := readyInteractive(t, provider)
				attached := false
				manager.Docker = sessionDocker{docker, func(_ context.Context, args []string) {
					attached = true
					if args[len(args)-3] != provider || args[len(args)-2] != "interactive" || args[len(args)-1] != mode {
						t.Fatal("native mode override changed or reached another provider")
					}
					if provider == "claude" {
						wanted := "{}\n"
						if mode == "bypassPermissions" {
							wanted = "{\"skipDangerousModePermissionPrompt\":true}\n"
						}
						for _, mount := range mounts(t, args) {
							if mount["dst"] == "/session-settings.json" {
								content, err := os.ReadFile(mount["src"])
								if err != nil || string(content) != wanted {
									t.Fatal("mode received unexpected Claude settings")
								}
							}
						}
					}
				}}
				if err := manager.Interactive(context.Background(), provider, mode); err != nil {
					t.Fatal(err)
				}
				if !attached {
					t.Fatal("mode override did not launch the selected client")
				}
			})
		}
	}
}

func TestInvalidNativeModesStopBeforeDockerAndStateAccess(t *testing.T) {
	for _, input := range [][2]string{{"codex", "bypassPermissions"}, {"codex", "untrusted"}, {"claude", "never"}, {"claude", "--dangerously-skip-permissions"}} {
		manager, docker := fixture(t)
		if err := manager.Interactive(context.Background(), input[0], input[1]); err == nil {
			t.Fatal("invalid native mode accepted")
		}
		if len(docker.calls) != 0 {
			t.Fatal("invalid native mode reached Docker")
		}
		if _, err := os.Stat(filepath.Join(manager.Runtime.Directory, "auth-installation.json")); !os.IsNotExist(err) {
			t.Fatal("invalid native mode created authentication state")
		}
	}
}

func TestInteractiveRejectsUnavailableLoginWithoutCreatingStorage(t *testing.T) {
	for _, setup := range []string{"identity", "volume", "missing", "invalid"} {
		t.Run(setup, func(t *testing.T) {
			manager, docker := fixture(t)
			if setup != "identity" {
				id, err := manager.identity(true)
				if err != nil {
					t.Fatal(err)
				}
				if setup != "volume" {
					if _, err := manager.volume(context.Background(), id, "codex", true); err != nil {
						t.Fatal(err)
					}
					docker.result = setup
				}
			}
			volumes := len(docker.volumes)
			docker.calls = nil
			if err := manager.Interactive(context.Background(), "codex", ""); err == nil || !strings.Contains(err.Error(), "auth login --provider codex") {
				t.Fatal("missing login did not give a useful error")
			}
			if len(docker.volumes) != volumes {
				t.Fatal("interactive created provider storage")
			}
			for _, args := range docker.calls {
				if args[0] == "run" && args[len(args)-1] != "status" {
					t.Fatal("interactive work started before login")
				}
			}
		})
	}
}

func TestInteractiveRejectsUnsafePreflight(t *testing.T) {
	for _, setup := range []string{"terminal", "provider", "image", "lock", "volume", "instructions"} {
		t.Run(setup, func(t *testing.T) {
			manager, docker := readyInteractive(t, "codex")
			provider := "codex"
			switch setup {
			case "terminal":
				manager.Terminal = func() bool { return false }
			case "provider":
				provider = "other"
			case "image":
				docker.staleImage = true
			case "lock":
				lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "volume":
				for _, volume := range docker.volumes {
					volume["Driver"] = "untrusted"
				}
			case "instructions":
				if err := os.WriteFile(filepath.Join(manager.Runtime.Directory, "instructions.md"), []byte("\x1bunsafe-terminal-control"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := manager.Interactive(ctx, provider, ""); err == nil {
				t.Fatal("unsafe preflight accepted")
			}
			for _, args := range docker.calls {
				if args[0] == "run" && args[len(args)-2] == "interactive" {
					t.Fatal("unsafe preflight started work")
				}
			}
		})
	}
}

func TestInterruptedInteractiveCleansContainerAndKeepsOnlyCache(t *testing.T) {
	manager, docker := readyInteractive(t, "codex")
	ctx, cancel := context.WithCancel(context.Background())
	var snapshot string
	manager.Docker = sessionDocker{docker, func(_ context.Context, args []string) {
		for _, mount := range mounts(t, args) {
			if mount["dst"] == "/session-instructions.md" {
				snapshot = mount["src"]
			}
		}
		cancel()
		docker.cancelled = true
	}}
	if err := manager.Interactive(ctx, "codex", ""); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("interrupted session leaked or ignored failure")
	}
	if docker.leftover || len(docker.volumes) != 1 {
		t.Fatal("interrupted session left its container or deleted the cache")
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatal("interrupted session retained its instruction snapshot")
	}
}

func TestInteractiveReportsCleanupFailureWithoutDiagnostics(t *testing.T) {
	manager, docker := readyInteractive(t, "claude")
	manager.Docker = sessionDocker{docker, func(context.Context, []string) {
		docker.cleanupFailed = true
	}}
	if err := manager.Interactive(context.Background(), "claude", ""); err == nil || !strings.Contains(err.Error(), "cleanup failed") || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("unsafe cleanup diagnostics or missing cleanup failure")
	}
}
