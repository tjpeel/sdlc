package runtimeimage

import (
	"bytes"
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
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const Image = "sdlc:local"

type State struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"`
	Revision  string     `json:"source_revision"`
	Engine    string     `json:"engine_id"`
	ImageID   string     `json:"image_id"`
	BuiltAt   time.Time  `json:"built_at"`
	Tools     string     `json:"tools"`
	Inventory *Inventory `json:"inventory,omitempty"`
}

type Docker interface {
	Output(context.Context, ...string) ([]byte, error)
	Run(context.Context, ...string) error
}

type LocalDocker struct {
	Stdout, Stderr io.Writer
}

func (docker LocalDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (docker LocalDocker) Run(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout, command.Stderr = docker.Stdout, docker.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", args[0], err)
	}
	return nil
}

type Manager struct {
	Directory string
	Docker    Docker
}

func New(stdout, stderr io.Writer) (Manager, error) {
	directory := os.Getenv("SDLC_STATE_DIR")
	if directory == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Manager{}, err
		}
		directory = filepath.Join(base, "sdlc")
	}
	return Manager{directory, LocalDocker{stdout, stderr}}, nil
}

func (manager Manager) engine(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		output, err := manager.Docker.Output(ctx, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}")
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(output, &endpoint); err != nil {
			return "", fmt.Errorf("cannot read Docker context endpoint: %w", err)
		}
	}
	if !strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://") {
		return "", fmt.Errorf("runtime builds require a local Docker engine (Unix socket or Windows named pipe)")
	}
	output, err := manager.Docker.Output(ctx, "info", "--format", `{"id":{{json .ID}},"os":{{json .OSType}}}`)
	if err != nil {
		return "", fmt.Errorf("Docker is unavailable; start a local Linux-container engine: %w", err)
	}
	var info struct {
		ID string `json:"id"`
		OS string `json:"os"`
	}
	if err := json.Unmarshal(output, &info); err != nil || info.ID == "" {
		return "", fmt.Errorf("Docker returned invalid engine information")
	}
	if info.OS != "linux" {
		return "", fmt.Errorf("Docker must use Linux containers")
	}
	return info.ID, nil
}

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (manager Manager) inspect(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := manager.Docker.Output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(output))
	if !imageID.MatchString(id) {
		return "", fmt.Errorf("Docker returned an invalid image identity")
	}
	return id, nil
}

func (manager Manager) read() (State, error) {
	data, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil {
		return State{}, fmt.Errorf("no recorded runtime; run sdlc runtime build --source SDLC_DIRECTORY: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || !imageID.MatchString(state.ImageID) {
		return State{}, fmt.Errorf("runtime state is invalid; rebuild from the SDLC clone")
	}
	if state.Inventory != nil {
		if err := ValidateInventory(*state.Inventory); err != nil {
			return State{}, err
		}
	}
	return state, nil
}

func (manager Manager) save(state State) error {
	file, err := os.CreateTemp(manager.Directory, ".runtime-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(state); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(manager.Directory, "runtime.json"))
}

func (manager Manager) Build(ctx context.Context, source string) (State, error) {
	if source == "" {
		previous, err := manager.read()
		if err != nil {
			return State{}, err
		}
		source = previous.Source
	}
	root, err := filepath.Abs(source)
	if err != nil {
		return State{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return State{}, err
	}
	contextDirectory := filepath.Join(root, "runtime")
	for _, name := range []string{"Dockerfile", ".dockerignore", "entrypoint.py", "dependencies.py", "bin/sdlc-job"} {
		info, err := os.Stat(filepath.Join(contextDirectory, name))
		if err != nil || !info.Mode().IsRegular() {
			return State{}, fmt.Errorf("source must be the SDLC clone with runtime/%s", name)
		}
	}
	if err := os.MkdirAll(manager.Directory, 0700); err != nil {
		return State{}, err
	}
	lock, err := filelock.Acquire(filepath.Join(manager.Directory, "runtime-build.lock"))
	if err != nil {
		return State{}, err
	}
	defer lock.Close()
	engine, err := manager.engine(ctx)
	if err != nil {
		return State{}, err
	}
	oldID, inspectErr := manager.inspect(ctx, Image)
	if inspectErr != nil {
		if recorded, err := manager.read(); err == nil && recorded.Engine == engine {
			return State{}, fmt.Errorf("recorded shared image cannot be inspected; resolve its Docker state before rebuilding: %w", inspectErr)
		}
		oldID = ""
	}
	if oldID != "" {
		containers, err := manager.Docker.Output(ctx, "ps", "--all", "--filter", "ancestor="+oldID, "--format", "{{.ID}}")
		if err != nil {
			return State{}, err
		}
		if len(bytes.TrimSpace(containers)) != 0 {
			return State{}, fmt.Errorf("containers still depend on the shared runtime; finish or remove them before rebuilding")
		}
	}
	revision := "unknown"
	git := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	if output, err := git.Output(); err == nil {
		revision = strings.TrimSpace(string(output))
		status := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain")
		if output, err := status.Output(); err != nil {
			return State{}, fmt.Errorf("cannot determine runtime source state: %w", err)
		} else if len(output) != 0 {
			revision += "-dirty"
		}
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return State{}, err
	}
	candidate := "sdlc:build-" + hex.EncodeToString(token[:])
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		manager.Docker.Output(cleanup, "image", "rm", candidate)
	}()
	if err := manager.Docker.Run(ctx, "build", "--pull", "--tag", candidate,
		"--label", "io.sdlc.managed=true", "--label", "io.sdlc.source-revision="+revision, contextDirectory); err != nil {
		return State{}, err
	}
	id, err := manager.inspect(ctx, candidate)
	if err != nil {
		return State{}, err
	}
	probeContext, cancelProbe := context.WithTimeout(ctx, time.Minute)
	defer cancelProbe()
	tools, err := manager.Docker.Output(probeContext, "run", "--rm", "--network", "none", "--user", "1000:1000",
		"--entrypoint", "/bin/sh", id, "-ec", "codex --version; claude --version; gh --version; dotnet --version; docker --version; docker compose version")
	if err != nil {
		return State{}, fmt.Errorf("built runtime tool probe failed: %w", err)
	}
	if len(bytes.TrimSpace(tools)) == 0 {
		return State{}, fmt.Errorf("built runtime tool probe returned no versions")
	}
	inventory, err := manager.saveInventory(ctx, id)
	if err != nil {
		return State{}, err
	}
	state := State{Version: 1, Source: root, Revision: revision, Engine: engine, ImageID: id, BuiltAt: time.Now().UTC(), Tools: strings.TrimSpace(string(tools)), Inventory: inventory}
	if err := manager.Docker.Run(ctx, "image", "tag", id, Image); err != nil {
		return State{}, err
	}
	if err := manager.save(state); err != nil {
		var rollback error
		if oldID != "" {
			rollback = manager.Docker.Run(context.Background(), "image", "tag", oldID, Image)
		} else {
			_, rollback = manager.Docker.Output(context.Background(), "image", "rm", Image)
		}
		return State{}, errors.Join(fmt.Errorf("cannot save runtime state: %w", err), rollback)
	}
	if oldID != "" && oldID != id {
		if _, err := manager.Docker.Output(ctx, "image", "rm", oldID); err != nil {
			return state, fmt.Errorf("new runtime is ready, but superseded image cleanup failed: %w", err)
		}
	}
	return state, nil
}

func (manager Manager) Status(ctx context.Context) (State, error) {
	state, err := manager.read()
	if err != nil {
		return State{}, err
	}
	engine, err := manager.engine(ctx)
	if err != nil {
		return State{}, err
	}
	if state.Engine != engine {
		return State{}, fmt.Errorf("selected Docker engine differs from the recorded runtime; build on this engine explicitly")
	}
	id, err := manager.inspect(ctx, Image)
	if err != nil {
		return State{}, fmt.Errorf("shared runtime is missing or unavailable; run sdlc runtime build: %w", err)
	}
	if id != state.ImageID {
		return State{}, fmt.Errorf("shared image was changed outside the CLI; rebuild to verify and register it")
	}
	return state, nil
}
