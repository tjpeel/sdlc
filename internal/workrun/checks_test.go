package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

type checkDockerFake struct {
	calls            [][]string
	cleanupCancelled bool
	cleanupFail      bool
	replaced         bool
	workerState      string
	inspectFail      bool
	daemonMissing    bool
}

func (fake *checkDockerFake) Output(ctx context.Context, args ...string) ([]byte, error) {
	fake.calls = append(fake.calls, append([]string(nil), args...))
	if fake.daemonMissing && args[0] == "run" && strings.Contains(strings.Join(args, " "), "--privileged") {
		return nil, errors.New("pinned daemon is missing from the local image cache")
	}
	if args[0] == "rm" || ((args[0] == "volume" || args[0] == "network") && args[1] == "rm") {
		fake.cleanupCancelled = fake.cleanupCancelled || ctx.Err() != nil
		if fake.cleanupFail {
			return nil, errors.New("cleanup failure")
		}
	}
	switch args[0] {
	case "inspect":
		if fake.inspectFail {
			return nil, errors.New("inspect unavailable")
		}
		if fake.workerState != "" {
			return []byte(fake.workerState), nil
		}
		return []byte(`{"Status":"exited","Running":false,"ExitCode":1,"Error":""}`), nil
	case "context":
		return []byte(`"unix:///tmp/disposable-test.sock"`), nil
	case "info":
		return []byte(`{"id":"offline-test","os":"linux"}`), nil
	case "image":
		if fake.replaced {
			return []byte("sha256:" + strings.Repeat("b", 64)), nil
		}
		return []byte("sha256:" + strings.Repeat("a", 64)), nil
	}
	return nil, nil
}
func (*checkDockerFake) Run(context.Context, ...string) error { panic("unexpected runtime Run") }

type checkRunnerFake struct {
	calls  [][]string
	fail   bool
	cancel context.CancelFunc
}

func (fake *checkRunnerFake) Run(_ context.Context, output io.Writer, args ...string) error {
	fake.calls = append(fake.calls, append([]string(nil), args...))
	io.WriteString(output, "synthetic check output\n")
	if fake.cancel != nil {
		fake.cancel()
		return context.Canceled
	}
	if fake.fail {
		return errors.New("check failed")
	}
	return nil
}

func checkFixture(t *testing.T) (DockerChecker, *checkDockerFake, *checkRunnerFake, string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	directory := t.TempDir()
	state := runtimeimage.State{Version: 1, Engine: "offline-test", ImageID: "sha256:" + strings.Repeat("a", 64)}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(directory, "runtime.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	docker, runner := &checkDockerFake{}, &checkRunnerFake{}
	return DockerChecker{Runtime: runtimeimage.Manager{Directory: directory, Docker: docker}, Runner: runner, DockerTests: true}, docker, runner, t.TempDir()
}

func checkDependencyPins() runtimepins.Pins {
	pins := runtimepins.Pins{Arguments: map[string]string{}, Images: map[string]string{}, SigningImage: "1password/op:2.40.1@sha256:" + strings.Repeat("b", 64), DaemonImage: "docker:29.9.0-dind@sha256:" + strings.Repeat("c", 64)}
	for _, name := range []string{"CODEX_VERSION", "CLAUDE_VERSION", "GH_VERSION", "DOTNET_VERSION", "NPM_VERSION", "YARN_VERSION", "COMPOSE_VERSION", "BUILDX_VERSION"} {
		pins.Arguments[name] = "1.2.3"
	}
	pins.Arguments["SKILLS_REVISION"] = strings.Repeat("a", 40)
	pins.Arguments["AGENTS_REVISION"] = strings.Repeat("b", 40)
	pins.Images["node"] = "node:22.1.0-bookworm@sha256:" + strings.Repeat("a", 64)
	pins.Images["golang"] = "golang:1.24.0-bookworm@sha256:" + strings.Repeat("b", 64)
	pins.Images["docker"] = "docker:29.9.0-cli@sha256:" + strings.Repeat("c", 64)
	return pins
}

func TestDockerChecksUseRecordedOrInstalledDaemonImage(t *testing.T) {
	installed := checkDependencyPins()
	recorded := "docker:29.8.3-dind@sha256:" + strings.Repeat("d", 64)
	for _, choice := range []string{"explicit", "installed", "legacy", "recorded"} {
		t.Run(choice, func(t *testing.T) {
			checker, docker, _, workspace := checkFixture(t)
			want := recorded
			if choice != "explicit" {
				state := runtimeimage.State{Version: 1, Engine: "offline-test", ImageID: "sha256:" + strings.Repeat("a", 64), DependencyPins: &installed}
				data, _ := json.Marshal(state)
				if err := os.WriteFile(filepath.Join(checker.Runtime.Directory, "runtime.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch choice {
			case "explicit", "recorded":
				checker.DaemonImage = recorded
			case "installed":
				want = installed.DaemonImage
			case "legacy":
				checker.UseDefaultDaemonImage = true
				want = runtimepins.DefaultDaemonImage
			}
			if err := checker.Check(context.Background(), workspace, [][]string{{"go", "test", "./..."}}, io.Discard); err != nil {
				t.Fatal(err)
			}
			selected, pullPolicy := "", ""
			for _, call := range docker.calls {
				for index, arg := range call {
					if arg == "dockerd-entrypoint.sh" && index+1 < len(call) {
						selected = call[index+1]
						for flag, value := range call {
							if value == "--pull" && flag+1 < len(call) {
								pullPolicy = call[flag+1]
							}
						}
					}
				}
			}
			if selected != want {
				t.Fatalf("daemon selection=%q, want %q", selected, want)
			}
			wantPull := "never"
			if choice == "legacy" {
				wantPull = "missing"
			}
			if pullPolicy != wantPull {
				t.Fatalf("daemon pull policy=%q, want %q", pullPolicy, wantPull)
			}
		})
	}
}

func TestDockerChecksMissingPinnedDaemonDoesNotImplicitlyPull(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	checker.DaemonImage = "docker:29.9.0-dind@sha256:" + strings.Repeat("c", 64)
	docker.daemonMissing = true
	err := checker.Check(context.Background(), workspace, [][]string{{"go", "test", "./..."}}, io.Discard)
	if err == nil || errors.Is(err, ErrCheckFailed) || len(runner.calls) != 0 {
		t.Fatalf("missing cached daemon did not stop checks: %v, workers=%v", err, runner.calls)
	}
	daemons := 0
	for _, call := range docker.calls {
		joined := strings.Join(call, " ")
		if call[0] == "pull" || strings.Contains(joined, "--pull missing") {
			t.Fatal("missing pinned daemon triggered a credential-using pull")
		}
		if call[0] == "run" && strings.Contains(joined, "--privileged") {
			daemons++
			if !strings.Contains(joined, "--pull never") || !strings.Contains(joined, checker.DaemonImage) {
				t.Fatal("pinned daemon startup changed its image or pull policy")
			}
		}
	}
	if daemons != 1 {
		t.Fatal("missing pinned daemon was retried or replaced")
	}
}

func TestDockerChecksRejectUnapprovedDaemonBeforeDockerCalls(t *testing.T) {
	for _, image := range []string{runtimepins.DefaultDaemonImage, "docker:latest", "docker:29.9.0-dind", "docker:29.9.0-dind@sha256:short", "example.invalid/docker:29.9.0-dind@sha256:" + strings.Repeat("c", 64), "docker:29.9.0-cli@sha256:" + strings.Repeat("c", 64)} {
		t.Run(image, func(t *testing.T) {
			checker, docker, runner, workspace := checkFixture(t)
			checker.DaemonImage = image
			if checker.Check(context.Background(), workspace, [][]string{{"go", "test", "./..."}}, io.Discard) == nil {
				t.Fatal("unapproved daemon image accepted")
			}
			if len(docker.calls) != 0 || len(runner.calls) != 0 {
				t.Fatal("unapproved daemon image reached Docker")
			}
		})
	}
}

func TestDockerChecksUseDedicatedDaemonAndUnchangedArgv(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	commands := [][]string{{"dotnet", "test", "Example.slnx", "--filter", "Name=a; echo unsafe", "--configuration", "Release"}, {"docker", "compose", "up", "--abort-on-container-exit"}}
	var output bytes.Buffer
	if err := checker.Check(context.Background(), workspace, commands, &output); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatal("checks missing")
	}
	for i, call := range runner.calls {
		joined := strings.Join(call, " ")
		for _, expected := range []string{"--user 1000:1000", "--read-only", "--cap-drop ALL", "no-new-privileges", "--network container:sdlc-check-", "DOCKER_HOST=unix:///run/sdlc/docker.sock", "TESTCONTAINERS_HOST_OVERRIDE=localhost", "type=volume", "size=2g", "HTTP_PROXY=", "GH_TOKEN=", "SSH_AUTH_SOCK="} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("missing worker restriction: %s", expected)
			}
		}
		for _, forbidden := range []string{workspace, "type=bind", "/var/run/docker.sock", "--privileged"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("worker exposes %s", forbidden)
			}
		}
		for j, arg := range call {
			if arg == "--entrypoint" {
				if call[j+1] != commands[i][0] || !reflect.DeepEqual(call[j+3:], commands[i][1:]) {
					t.Fatal("argv was changed or interpreted")
				}
				break
			}
		}
	}
	var daemon, copyCall []string
	for _, call := range docker.calls {
		if call[0] == "run" && strings.Contains(strings.Join(call, " "), "--privileged") {
			daemon = call
		}
		if call[0] == "run" && strings.Contains(strings.Join(call, " "), "dst=/source,readonly") {
			copyCall = call
		}
	}
	if daemon == nil || copyCall == nil {
		t.Fatal("daemon or read-only source copy missing")
	}
	joined := strings.Join(daemon, " ")
	if !strings.Contains(joined, checkDaemonImage) || !strings.Contains(joined, "--host=unix://"+checkSocket) {
		t.Fatal("daemon version/socket not fixed")
	}
	if !strings.Contains(joined, "--storage-driver=overlay2") || !strings.Contains(joined, "--feature=containerd-snapshotter=false") || strings.Contains(joined, "--storage-driver=vfs") {
		t.Fatal("integration image builds require the explicit classic OverlayFS backend")
	}
	for _, arg := range daemon {
		if arg == "--publish" || arg == "-p" || strings.Contains(arg, "tcp://") || strings.Contains(arg, "type=bind") {
			t.Fatal("daemon exposes host resources")
		}
	}
	if !strings.Contains(output.String(), "synthetic check output") || !strings.Contains(output.String(), "Example.slnx") {
		t.Fatal("missing streamed output/argv evidence")
	}
	last := docker.calls[len(docker.calls)-1]
	if last[0] != "volume" || last[1] != "rm" {
		t.Fatal("volumes were not removed last")
	}
}

func TestDockerChecksStopAndCleanOnFailureAndCancellation(t *testing.T) {
	for _, cancelCheck := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancelCheck], func(t *testing.T) {
			checker, docker, runner, workspace := checkFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelCheck {
				runner.cancel = cancel
			} else {
				runner.fail = true
			}
			err := checker.Check(ctx, workspace, [][]string{{"dotnet", "test", "Example.slnx"}, {"dotnet", "build"}}, io.Discard)
			if err == nil {
				t.Fatal("failed check passed")
			}
			if cancelCheck && (!errors.Is(err, context.Canceled) || errors.Is(err, ErrCheckFailed)) {
				t.Fatal("cancellation was classified as code failure", err)
			}
			if !cancelCheck && !errors.Is(err, ErrCheckFailed) {
				t.Fatal("nonzero check exit not classified", err)
			}
			if len(runner.calls) != 1 {
				t.Fatal("failure ran later checks")
			}
			removed, volumes, networks := 0, 0, 0
			for _, call := range docker.calls {
				if call[0] == "rm" {
					removed++
				}
				if call[0] == "volume" && call[1] == "rm" {
					volumes++
				}
				if call[0] == "network" && call[1] == "rm" {
					networks++
				}
			}
			if removed != 3 || volumes != 3 || networks != 1 || docker.cleanupCancelled {
				t.Fatal("cleanup incomplete or used cancelled context")
			}
		})
	}
}

func TestDockerChecksOperationalFailuresDoNotRequestCodeRepair(t *testing.T) {
	for _, cause := range []string{"inspect", "running", "startup", "zero exit", "invalid state", "out of memory", "cleanup", "runtime pin"} {
		t.Run(cause, func(t *testing.T) {
			checker, docker, runner, workspace := checkFixture(t)
			runner.fail = true
			switch cause {
			case "inspect":
				docker.inspectFail = true
			case "running":
				docker.workerState = `{"Status":"running","Running":true,"ExitCode":1}`
			case "startup":
				docker.workerState = `{"Status":"exited","ExitCode":127,"Error":"container init failed"}`
			case "zero exit":
				docker.workerState = `{"Status":"exited","ExitCode":0}`
			case "invalid state":
				docker.workerState = `invalid`
			case "out of memory":
				docker.workerState = `{"Status":"exited","ExitCode":137,"OOMKilled":true}`
			case "cleanup":
				docker.cleanupFail = true
			case "runtime pin":
				checker.ImageID = "sha256:" + strings.Repeat("b", 64)
			}
			err := checker.Check(context.Background(), workspace, [][]string{{"dotnet", "test"}}, io.Discard)
			if err == nil || errors.Is(err, ErrCheckFailed) {
				t.Fatal("operational failure classified as code repair", err)
			}
			if cause == "runtime pin" && len(runner.calls) != 0 {
				t.Fatal("runtime pin mismatch launched worker")
			}
		})
	}
}

func TestDockerChecksRejectRuntimeReplacementAndCleanupFailure(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	docker.replaced = true
	if err := checker.Check(context.Background(), workspace, [][]string{{"dotnet", "test"}}, io.Discard); err == nil {
		t.Fatal("replaced runtime accepted")
	}
	for _, call := range docker.calls {
		if call[0] == "run" || call[0] == "volume" {
			t.Fatal("failed status created resources")
		}
	}
	docker.replaced, docker.cleanupFail = false, true
	if err := checker.Check(context.Background(), workspace, [][]string{{"dotnet", "test"}}, io.Discard); err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatal("cleanup failure ignored", err)
	}
	if len(runner.calls) != 1 {
		t.Fatal("check did not run")
	}
}

func TestDockerChecksInputsOnlyReachSnapshotInitializer(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	checker.InputDirectory = t.TempDir()
	if err := os.WriteFile(filepath.Join(checker.InputDirectory, ".env"), []byte("EXAMPLE_SETTING=disposable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checker.Check(context.Background(), workspace, [][]string{{"dotnet", "test", "Example.slnx"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range docker.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, checker.InputDirectory) {
			count++
			if !strings.Contains(joined, "dst=/inputs,readonly") || !strings.Contains(joined, "--network none") || !strings.Contains(joined, "git") || !strings.Contains(joined, "ls-tree") {
				t.Fatal("input snapshot initializer is unsafe")
			}
		}
	}
	if count != 1 || strings.Contains(strings.Join(runner.calls[0], " "), checker.InputDirectory) {
		t.Fatal("private input directory leaked to worker")
	}
	for _, forbidden := range []string{".git", "linked-input"} {
		t.Run(forbidden, func(t *testing.T) {
			checker, docker, _, workspace := checkFixture(t)
			checker.InputDirectory = t.TempDir()
			path := filepath.Join(checker.InputDirectory, forbidden)
			var err error
			if forbidden == ".git" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.Symlink(workspace, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if checker.Check(context.Background(), workspace, [][]string{{"dotnet", "test"}}, io.Discard) == nil {
				t.Fatal("unsafe input accepted")
			}
			for _, call := range docker.calls {
				if call[0] == "run" || call[0] == "volume" {
					t.Fatal("unsafe input created resources")
				}
			}
		})
	}
}

func TestDockerChecksRequireCommands(t *testing.T) {
	for _, commands := range [][][]string{nil, {{}}, {{""}}, {{"dotnet", "bad\x00arg"}}} {
		checker, docker, _, workspace := checkFixture(t)
		if checker.Check(context.Background(), workspace, commands, io.Discard) == nil {
			t.Fatal("invalid checks accepted")
		}
		if len(docker.calls) != 0 {
			t.Fatal("invalid checks contacted Docker")
		}
	}
}

func TestDockerChecksDefaultHasNoPrivilegedDaemonOrSocket(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	checker.DockerTests = false
	if err := checker.Check(context.Background(), workspace, [][]string{{"dotnet", "build", "Example.slnx"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	volumes := 0
	for _, call := range docker.calls {
		joined := strings.Join(call, " ")
		for _, forbidden := range []string{"--privileged", "-daemon", "-socket", "-data", "dockerd", "TESTCONTAINERS_", "DOCKER_HOST="} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("default checks enable Docker integration: %s", forbidden)
			}
		}
		if call[0] == "network" {
			t.Fatal("default checks create daemon network")
		}
		if call[0] == "volume" && call[1] == "create" {
			volumes++
		}
	}
	if volumes != 1 {
		t.Fatal("default checks did not use only isolated workspace storage")
	}
	worker := strings.Join(runner.calls[0], " ")
	if !strings.Contains(worker, "--network bridge") || !strings.Contains(worker, "--user 1000:1000") {
		t.Fatal("default worker is not isolated")
	}
	for _, forbidden := range []string{"--privileged", "/run/sdlc", "DOCKER_HOST=", "TESTCONTAINERS_"} {
		if strings.Contains(worker, forbidden) {
			t.Fatalf("default worker has Docker daemon access: %s", forbidden)
		}
	}
}

func TestCheckSnapshotCopiesRawCommittedBlobsDespiteArchiveAttributes(t *testing.T) {
	root, destination, inputs := t.TempDir(), t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.invalid", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.invalid"}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %s: %v", output, err)
		}
	}
	git("init")
	files := map[string]string{
		".gitattributes": "tests.cs export-ignore\nversion.txt export-subst\n",
		"tests.cs":       "// committed test must run\n",
		"version.txt":    "$Format:%H$\n",
		"run-check":      "#!/bin/sh\nexit 0\n",
		"test\tfile.cs":  "// raw path with a tab\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "Disposable test fixture")
	if err := os.WriteFile(filepath.Join(root, "tests.cs"), []byte("// ignored dirty replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("must not copy"), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/source", root, "/workspace", destination, "/inputs", inputs).Replace(copyCheckWorkspace)
	// The production initializer runs as root inside a container. This offline
	// test verifies copying; it does not change host fixture ownership.
	script = "import os\nos.chown = lambda *args, **kwargs: None\n" + script
	command := exec.Command("python3", "-c", script)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("snapshot helper: %s: %v", output, err)
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(actual) != expected {
			t.Fatalf("raw committed file %s altered or omitted: %q %v", name, actual, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked file copied")
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); !os.IsNotExist(err) {
		t.Fatal("Git state copied")
	}
	info, err := os.Stat(filepath.Join(destination, "run-check"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("executable mode lost")
	}
	if err := os.Symlink("version.txt", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	git("add", "linked")
	git("commit", "-m", "Disposable symlink fixture")
	command = exec.Command("python3", "-c", script)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "unsafe committed tree entry") {
		t.Fatal("committed symlink was accepted", string(output), err)
	}
}
