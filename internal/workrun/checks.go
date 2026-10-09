package workrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

// CheckCommandRunner streams a check's output without invoking a host shell.
type CheckCommandRunner interface {
	Run(context.Context, io.Writer, ...string) error
}

type localCheckRunner struct{}

func (localCheckRunner) Run(ctx context.Context, output io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout, command.Stderr = output, output
	return command.Run()
}

// DockerChecker gives checks a disposable copy. DockerTests explicitly enables
// a dedicated privileged daemon for trusted integration checks. Privileged
// containers are not a security boundary against hostile code.
type DockerChecker struct {
	Runtime        runtimeimage.Manager
	Runner         CheckCommandRunner
	InputDirectory string
	ImageID        string
	DockerTests    bool
	DaemonImage    string
	// Old journals have no daemon pin. Preserve their original default even
	// when the installation now has different dependency pins.
	UseDefaultDaemonImage bool
}

// ErrCheckFailed identifies a completed check with a nonzero exit status.
// Startup, transport, cancellation and cleanup errors are operational failures.
var ErrCheckFailed = errors.New("repository check failed")

const checkDaemonImage = runtimepins.DefaultDaemonImage
const checkSocket = "/run/sdlc/docker.sock"

// Explicit empties also override Docker CLI proxy injection from host config.
var checkSuppressedEnvironment = []string{"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "ftp_proxy", "no_proxy", "all_proxy", "DOCKER_AUTH_CONFIG", "GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK"}

func checkContainerEnvironment(args []string) []string {
	for _, key := range checkSuppressedEnvironment {
		args = append(args, "--env", key+"=")
	}
	return args
}

// Docker needs explicit empty overrides to suppress host proxy injection, but
// the check itself needs these keys absent: some libraries treat empty proxies
// as configured URLs. The pinned Debian image supplies GNU env.
func checkProcessArguments(command []string) []string {
	args := make([]string, 0, len(checkSuppressedEnvironment)+1+len(command))
	for _, key := range checkSuppressedEnvironment {
		args = append(args, "--unset="+key)
	}
	args = append(args, "--")
	return append(args, command...)
}

// Build the test copy from HEAD, excluding ignored outputs and local Git state.
// Raw blobs bypass export-ignore/export-subst; symlinks and submodules are rejected.
// All committed paths and optional input paths are checked before writing.
const copyCheckWorkspace = `import os, pathlib, shutil, subprocess
source_root = '/source'
git = ['git', '--no-replace-objects', '-c', 'safe.directory='+source_root, '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false', '-C', source_root]
env = {'PATH':os.environ['PATH'], 'HOME':'/tmp', 'GIT_CONFIG_NOSYSTEM':'1', 'GIT_CONFIG_GLOBAL':'/dev/null'}
entries = subprocess.check_output(git+['ls-tree', '-r', '-z', 'HEAD'], env=env)
for entry in entries.split(b'\0'):
 if not entry: continue
 metadata, raw_path = entry.split(b'\t', 1)
 mode, kind, oid = metadata.split(b' ')
 path = pathlib.PurePosixPath(os.fsdecode(raw_path))
 if path.is_absolute() or '..' in path.parts or '.git' in path.parts or mode not in (b'100644', b'100755') or kind != b'blob': raise ValueError('unsafe committed tree entry')
 destination = pathlib.Path('/workspace')/path
 destination.parent.mkdir(parents=True, exist_ok=True)
 data = subprocess.check_output(git+['cat-file', 'blob', oid.decode('ascii')], env=env)
 destination.write_bytes(data)
 destination.chmod(0o755 if mode == b'100755' else 0o644)
if os.path.isdir('/inputs'):
 for root, dirs, files in os.walk('/inputs', followlinks=False):
  for name in dirs+files:
   source = pathlib.Path(root)/name
   relative = source.relative_to('/inputs')
   if '.git' in relative.parts or source.is_symlink(): raise ValueError('unsafe check input')
   destination = pathlib.Path('/workspace')/relative
   if any(p.is_symlink() for p in [destination, *destination.parents]): raise ValueError('input traverses symlink')
   if source.is_dir(): destination.mkdir(parents=True, exist_ok=True)
   elif source.is_file(): destination.parent.mkdir(parents=True, exist_ok=True); shutil.copy2(source,destination)
   else: raise ValueError('unsupported check input')
for root, dirs, files in os.walk('/workspace', followlinks=False):
 os.chown(root, 1000, 1000)
 for name in dirs+files: os.chown(os.path.join(root,name), 1000, 1000, follow_symlinks=False)
`

func (checker DockerChecker) Check(ctx context.Context, workspace string, commands [][]string, output io.Writer) (result error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	daemonImage := checker.DaemonImage
	if daemonImage != "" {
		if err := runtimepins.ValidateDaemonImage(daemonImage); err != nil {
			return err
		}
	}
	if len(commands) == 0 {
		return errors.New("repository checks are required")
	}
	for _, command := range commands {
		if len(command) == 0 || command[0] == "" {
			return errors.New("repository check has no executable")
		}
		for _, arg := range command {
			if strings.ContainsRune(arg, 0) {
				return errors.New("repository check argument contains NUL")
			}
		}
	}
	if checker.Runtime.Docker == nil {
		return errors.New("repository checks require a configured Docker runtime")
	}
	state, err := checker.Runtime.Status(ctx)
	if err != nil {
		return fmt.Errorf("repository check runtime: %w", err)
	}
	if checker.ImageID != "" && checker.ImageID != state.ImageID {
		return errors.New("repository check runtime differs from the job's pinned image")
	}
	if daemonImage == "" {
		daemonImage = checkDaemonImage
		if !checker.UseDefaultDaemonImage && state.DependencyPins != nil {
			daemonImage = state.DependencyPins.DaemonImage
			if err := runtimepins.ValidateDaemonImage(daemonImage); err != nil {
				return err
			}
		}
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return errors.New("repository check workspace is not a directory")
	}
	// Docker's mount syntax cannot represent commas in a bind source.
	if strings.Contains(workspace, ",") {
		return errors.New("repository check workspace path contains a comma")
	}
	inputs := checker.InputDirectory
	if inputs != "" {
		inputs, err = filepath.Abs(inputs)
		if err != nil {
			return err
		}
		if err = filepath.Walk(inputs, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if strings.Contains(path, ",") || info.Mode()&os.ModeSymlink != 0 || info.Name() == ".git" || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("unsafe repository check input")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	var token [12]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	name := "sdlc-check-" + hex.EncodeToString(token[:])
	volumes := []string{name + "-workspace"}
	if checker.DockerTests {
		volumes = append(volumes, name+"-socket", name+"-data")
	}
	var cleanups [][]string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var cleanupErrors error
		for i := len(cleanups) - 1; i >= 0; i-- {
			if _, err := checker.Runtime.Docker.Output(cleanup, cleanups[i]...); err != nil {
				cleanupErrors = errors.Join(cleanupErrors, fmt.Errorf("repository check cleanup failed (%s): %w", cleanups[i][0], err))
			}
		}
		if cleanupErrors != nil {
			// Do not classify leaked resources as a repairable code failure.
			if errors.Is(result, ErrCheckFailed) {
				result = fmt.Errorf("%s", result)
			}
			result = errors.Join(result, cleanupErrors)
		}
	}()
	call := func(args ...string) error { _, err := checker.Runtime.Docker.Output(ctx, args...); return err }
	for _, volume := range volumes {
		if err = call("volume", "create", "--label", "io.sdlc.managed=true", volume); err != nil {
			return err
		}
		cleanups = append(cleanups, []string{"volume", "rm", volume})
	}
	if checker.DockerTests {
		if err = call("network", "create", "--label", "io.sdlc.managed=true", name); err != nil {
			return err
		}
		cleanups = append(cleanups, []string{"network", "rm", name})
	}
	// Register names before starting containers: a cancelled client may have
	// successfully started a container before returning an error.
	cleanups = append(cleanups, []string{"rm", "--force", name + "-copy"})
	copyArgs := checkContainerEnvironment([]string{"run", "--name", name + "-copy", "--pull", "never", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--mount", "type=bind,src=" + workspace + ",dst=/source,readonly", "--mount", "type=volume,src=" + volumes[0] + ",dst=/workspace"})
	copyArgs = append(copyArgs, "--entrypoint", "python3", state.ImageID, "-c", copyCheckWorkspace)
	if inputs != "" {
		// Insert the input bind before the image/entrypoint arguments.
		position := len(copyArgs) - 5
		copyArgs = append(copyArgs[:position], append([]string{"--mount", "type=bind,src=" + inputs + ",dst=/inputs,readonly"}, copyArgs[position:]...)...)
	}
	if err = call(copyArgs...); err != nil {
		return fmt.Errorf("copy repository check workspace: %w", err)
	}
	if checker.DockerTests {
		cleanups = append(cleanups, []string{"rm", "--force", name + "-daemon"})
		daemonPull := "never"
		if daemonImage == checkDaemonImage {
			// Legacy jobs retain their original tag and pull policy. New jobs
			// prepare an exact public digest before entering the controller.
			daemonPull = "missing"
		}
		daemonArgs := checkContainerEnvironment([]string{"run", "--detach", "--name", name + "-daemon", "--pull", daemonPull, "--privileged", "--network", name,
			"--mount", "type=volume,src=" + volumes[0] + ",dst=/workspace", "--mount", "type=volume,src=" + volumes[1] + ",dst=/run/sdlc", "--mount", "type=volume,src=" + volumes[2] + ",dst=/var/lib/docker",
			"--env", "DOCKER_TLS_CERTDIR="})
		// VFS cannot snapshot build layers containing Unix sockets, including
		// sockets left by .NET build services. Pin the classic OverlayFS backend
		// explicitly: Docker 29 otherwise enables the containerd image store.
		daemonArgs = append(daemonArgs, "--entrypoint", "dockerd-entrypoint.sh", daemonImage, "dockerd", "--host=unix://"+checkSocket, "--tls=false", "--group=root", "--feature=containerd-snapshotter=false", "--storage-driver=overlay2")
		if err = call(daemonArgs...); err != nil {
			return fmt.Errorf("start repository check daemon: %w", err)
		}
		ready, cancelReady := context.WithTimeout(ctx, time.Minute)
		defer cancelReady()
		for {
			_, err = checker.Runtime.Docker.Output(ready, "exec", name+"-daemon", "docker", "--host", "unix://"+checkSocket, "info")
			if err == nil {
				break
			}
			select {
			case <-ready.Done():
				return fmt.Errorf("repository check daemon did not become ready: %w", ready.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		if err = call("exec", name+"-daemon", "chown", "0:1000", checkSocket); err != nil {
			return err
		}
		if err = call("exec", name+"-daemon", "chmod", "0660", checkSocket); err != nil {
			return err
		}
	}
	runner := checker.Runner
	if runner == nil {
		runner = localCheckRunner{}
	}
	if output == nil {
		output = io.Discard
	}
	approvedPaths := diagnosticSourcePaths(ctx, workspace)
	for i, command := range commands {
		worker := fmt.Sprintf("%s-worker-%d", name, i)
		cleanups = append(cleanups, []string{"rm", "--force", worker})
		if err = json.NewEncoder(output).Encode(command); err != nil {
			return err
		}
		workerNetwork := "bridge"
		if checker.DockerTests {
			workerNetwork = "container:" + name + "-daemon"
		}
		args := []string{"run", "--name", worker, "--pull", "never", "--network", workerNetwork, "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "1024", "--memory", "4g", "--cpus", "4", "--workdir", "/workspace", "--tmpfs", "/tmp:rw,nosuid,nodev,size=2g,mode=1777", "--tmpfs", "/tmp/check-home:rw,nosuid,nodev,size=2g,uid=1000,gid=1000,mode=0700", "--mount", "type=volume,src=" + volumes[0] + ",dst=/workspace", "--env", "HOME=/tmp/check-home", "--env", "CODEX_HOME=/tmp/check-home/.codex", "--env", "CLAUDE_CONFIG_DIR=/tmp/check-home/.claude", "--env", "DOCKER_CONFIG=/tmp/check-home/.docker", "--env", "DOTNET_CLI_HOME=/tmp/check-home", "--env", "DOTNET_CLI_TELEMETRY_OPTOUT=1"}
		if checker.DockerTests {
			args = append(args, "--mount", "type=volume,src="+volumes[1]+",dst=/run/sdlc", "--env", "DOCKER_HOST=unix://"+checkSocket, "--env", "TESTCONTAINERS_HOST_OVERRIDE=localhost", "--env", "TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE="+checkSocket)
		}
		args = checkContainerEnvironment(args)
		args = append(args, "--entrypoint", "/usr/bin/env", state.ImageID)
		args = append(args, checkProcessArguments(command)...)
		diagnostic := &formatterDiagnostic{approved: approvedPaths}
		commandOutput := io.MultiWriter(output, diagnostic)
		if err = runner.Run(ctx, commandOutput, args...); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			metadata, inspectErr := checker.Runtime.Docker.Output(ctx, "inspect", "--format", "{{json .State}}", worker)
			if inspectErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("repository check %d transport failed: %w", i+1, errors.Join(err, inspectErr))
			}
			var status struct {
				Status    string
				Running   bool
				ExitCode  int
				Error     string
				OOMKilled bool
			}
			if json.Unmarshal(metadata, &status) != nil || status.Status != "exited" || status.Running || status.ExitCode == 0 || status.Error != "" || status.OOMKilled {
				return fmt.Errorf("repository check %d did not complete successfully: %w", i+1, err)
			}
			diagnostic.finish()
			// An unavailable Docker daemon is a check setup failure. Do not
			// spend provider repair turns on it or enable privileges implicitly.
			// A compiler/formatter failure followed by a Docker cleanup error
			// still needs the original source repair.
			if diagnostic.dockerUnavailable && diagnostic.compiler == nil && !diagnostic.styleIssues {
				if !checker.DockerTests {
					return fmt.Errorf("repository check %d exited with status %d: Docker daemon unavailable; start a new run with --docker-tests; resume preserves the recorded Docker setting. Private check log retained", i+1, status.ExitCode)
				}
				return fmt.Errorf("repository check %d exited with status %d: Docker daemon unavailable; inspect the private check log and resolve the Docker endpoint or isolated test-daemon setup before resuming", i+1, status.ExitCode)
			}
			return &CheckFailure{Command: i + 1, ExitCode: status.ExitCode, formatter: diagnostic.styleIssues, paths: diagnostic.paths, compiler: diagnostic.compiler}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
