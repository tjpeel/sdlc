package providerauth

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

//go:embed container.py
var helper string

var Providers = []string{"codex", "claude"}

type Docker interface {
	Output(context.Context, ...string) ([]byte, error)
	Interactive(context.Context, ...string) error
}

type LocalDocker struct{}

func (LocalDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	// Keep Docker diagnostics private: proxy URLs and configuration can contain secrets.
	return exec.CommandContext(ctx, "docker", args...).Output()
}

func (LocalDocker) Interactive(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	// The container PTY merges provider stderr into stdout. Docker-client stderr
	// is separate and can contain host configuration; only show our fixed errors.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, io.Discard
	return command.Run()
}

type Manager struct {
	Runtime  runtimeimage.Manager
	Docker   Docker
	Terminal func() bool
}

func New(runtime runtimeimage.Manager) Manager {
	return Manager{Runtime: runtime, Docker: LocalDocker{}, Terminal: terminal}
}

func terminal() bool {
	for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func validProvider(provider string) bool { return provider == "codex" || provider == "claude" }

func randomID() (string, error) {
	var token [16]byte
	_, err := rand.Read(token[:])
	return hex.EncodeToString(token[:]), err
}

var installationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (manager Manager) identity(create bool) (string, error) {
	path := filepath.Join(manager.Runtime.Directory, "auth-installation.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !create {
		return "", nil
	}
	if err == nil {
		var record struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &record) != nil || !installationID.MatchString(record.ID) {
			return "", fmt.Errorf("authentication installation state is invalid")
		}
		return record.ID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot read authentication installation state")
	}
	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("cannot create authentication installation identity")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("cannot save authentication installation identity")
	}
	if err = json.NewEncoder(file).Encode(struct {
		ID string `json:"id"`
	}{id}); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("cannot save authentication installation identity")
	}
	return id, nil
}

func labels(id, provider string) map[string]string {
	return map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "provider-auth",
		"io.sdlc.installation": id, "io.sdlc.provider": provider}
}

func (manager Manager) volume(ctx context.Context, id, provider string, create bool) (string, error) {
	name := "sdlc-auth-" + id + "-" + provider
	output, err := manager.Docker.Output(ctx, "volume", "ls", "--filter", "name="+name, "--format", "{{.Name}}")
	if err != nil {
		return "", fmt.Errorf("cannot list authentication storage")
	}
	found := false
	for _, candidate := range strings.Fields(string(output)) {
		if candidate == name {
			found = true
		}
	}
	if !found {
		if !create {
			return "", nil
		}
		args := []string{"volume", "create", "--driver", "local"}
		for _, key := range []string{"io.sdlc.managed", "io.sdlc.kind", "io.sdlc.installation", "io.sdlc.provider"} {
			args = append(args, "--label", key+"="+labels(id, provider)[key])
		}
		if _, err := manager.Docker.Output(ctx, append(args, name)...); err != nil {
			return "", fmt.Errorf("cannot create authentication storage")
		}
	}
	output, err = manager.Docker.Output(ctx, "volume", "inspect", name)
	if err != nil {
		return "", fmt.Errorf("cannot inspect authentication storage")
	}
	var volumes []struct {
		Name, Driver string
		Options      map[string]string
		Labels       map[string]string
	}
	if json.Unmarshal(output, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != name ||
		volumes[0].Driver != "local" || len(volumes[0].Options) != 0 {
		return "", fmt.Errorf("authentication storage must be an SDLC-managed local volume without driver options")
	}
	for key, value := range labels(id, provider) {
		if volumes[0].Labels[key] != value {
			return "", fmt.Errorf("authentication storage ownership labels do not match this installation")
		}
	}
	return name, nil
}

func containerArgs(image, name, volume, provider, action string) []string {
	args := []string{"run", "--rm", "--name", name, "--pull", "never", "--log-driver", "none",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "128", "--memory", "1g", "--cpus", "2",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777",
		"--tmpfs", "/home/node/:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000"}
	// Docker's client can inject proxies from its host config. Empty overrides prevent that.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "ftp_proxy", "no_proxy", "all_proxy"} {
		args = append(args, "--env", key+"=")
	}
	mount := "type=volume,src=" + volume + ",dst=/provider-auth,volume-nocopy"
	if action == "init" {
		args = append(args, "--network", "none", "--user", "0:0", "--cap-add", "CHOWN", "--cap-add", "FOWNER")
	} else {
		args = append(args, "--user", "1000:1000")
		if action == "status" {
			args = append(args, "--network", "none")
			mount += ",readonly"
		}
		if action == "login" {
			args = append(args, "--interactive", "--tty", "--network", "bridge")
		}
	}
	return append(args, "--mount", mount, "--entrypoint", "/usr/bin/python3", image, "-c", helper, provider, action)
}

func (manager Manager) container(ctx context.Context, image, volume, provider, action string) (output []byte, err error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("cannot name authentication container")
	}
	name := "sdlc-auth-" + id
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// --rm normally removes it; listing also distinguishes that from a failed cleanup.
		remaining, cleanupErr := manager.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if cleanupErr == nil && len(strings.TrimSpace(string(remaining))) > 0 {
			_, cleanupErr = manager.Docker.Output(cleanup, "rm", "--force", name)
		}
		if cleanupErr != nil {
			err = fmt.Errorf("authentication container cleanup failed; check Docker before retrying")
		}
	}()
	args := containerArgs(image, name, volume, provider, action)
	if action == "login" {
		err = manager.Docker.Interactive(ctx, args...)
	} else {
		output, err = manager.Docker.Output(ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("authentication %s failed or was cancelled; check sdlc auth status before retrying", action)
	}
	return output, nil
}

func (manager Manager) begin(ctx context.Context) (runtimeimage.State, *os.File, error) {
	if err := os.MkdirAll(manager.Runtime.Directory, 0700); err != nil {
		return runtimeimage.State{}, nil, fmt.Errorf("cannot open SDLC state directory")
	}
	lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
	if err != nil {
		return runtimeimage.State{}, nil, fmt.Errorf("another SDLC operation is running, or its lock is unavailable")
	}
	state, err := manager.Runtime.Status(ctx)
	if err != nil {
		lock.Close()
		return runtimeimage.State{}, nil, fmt.Errorf("shared local runtime is unavailable or changed; run sdlc runtime status and rebuild if needed")
	}
	return state, lock, nil
}

func (manager Manager) Login(ctx context.Context, provider string) error {
	if !validProvider(provider) {
		return fmt.Errorf("provider must be codex or claude")
	}
	if !manager.Terminal() {
		return fmt.Errorf("login needs an interactive terminal for stdin, stdout and stderr; do not redirect login output")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	state, lock, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	id, err := manager.identity(true)
	if err != nil {
		return err
	}
	volume, err := manager.volume(ctx, id, provider, true)
	if err != nil {
		return err
	}
	if _, err := manager.container(ctx, state.ImageID, volume, provider, "init"); err != nil {
		return err
	}
	if _, err := manager.container(ctx, state.ImageID, volume, provider, "login"); err != nil {
		return err
	}
	// Prove the persisted credential can be loaded by a separate, offline container.
	result, err := manager.status(ctx, state.ImageID, volume, provider)
	if err != nil {
		return err
	}
	if result != "stored" {
		return fmt.Errorf("login completed but a fresh container could not load stored account credentials")
	}
	return nil
}

func (manager Manager) status(ctx context.Context, image, volume, provider string) (string, error) {
	output, err := manager.container(ctx, image, volume, provider, "status")
	if err != nil {
		return "", err
	}
	var result struct {
		State string `json:"state"`
	}
	if json.Unmarshal(output, &result) != nil || (result.State != "stored" && result.State != "missing" && result.State != "invalid") {
		return "", fmt.Errorf("authentication status returned an invalid response")
	}
	return result.State, nil
}

func (manager Manager) Status(ctx context.Context, provider string) (string, error) {
	if !validProvider(provider) {
		return "", fmt.Errorf("provider must be codex or claude")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	state, lock, err := manager.begin(ctx)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	id, err := manager.identity(false)
	if err != nil || id == "" {
		return "missing", err
	}
	volume, err := manager.volume(ctx, id, provider, false)
	if err != nil || volume == "" {
		return "missing", err
	}
	return manager.status(ctx, state.ImageID, volume, provider)
}
