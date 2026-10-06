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
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

const Image = "sdlc:local"

type State struct {
	Version        int               `json:"version"`
	Source         string            `json:"source"`
	Revision       string            `json:"source_revision"`
	Engine         string            `json:"engine_id"`
	ImageID        string            `json:"image_id"`
	BuiltAt        time.Time         `json:"built_at"`
	Tools          string            `json:"tools"`
	Inventory      *Inventory        `json:"inventory,omitempty"`
	DependencyPins *runtimepins.Pins `json:"dependency_pins,omitempty"`
	BuildRecipe    string            `json:"build_recipe,omitempty"`
}

type BuildOptions struct {
	// SourcePins ignores private dependency overrides and uses the source Dockerfile.
	SourcePins bool
	// SourceRevision supplies verified archive provenance when no Git checkout exists.
	// A checkout's own revision takes priority.
	SourceRevision string
	// ValidatePrevious runs under the writer lock before Docker operations.
	ValidatePrevious  func(context.Context, State) error
	Pins              *runtimepins.Pins
	Refresh           bool
	ExpectedImageID   string
	ValidateCandidate func(context.Context, State) error
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
var sourceCommitID = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

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
	if err := json.Unmarshal(data, &state); !utf8.Valid(data) || err != nil || state.Version != 1 || !imageID.MatchString(state.ImageID) {
		return State{}, fmt.Errorf("runtime state is invalid; rebuild from the SDLC clone")
	}
	if state.Inventory != nil {
		if err := ValidateInventory(*state.Inventory); err != nil {
			return State{}, err
		}
	}
	if state.DependencyPins != nil {
		if err := state.DependencyPins.Validate(); err != nil {
			return State{}, fmt.Errorf("runtime dependency pins are invalid; rebuild from the SDLC clone")
		}
	}
	if state.BuildRecipe != "" {
		if err := validateBuildRecipe([]byte(state.BuildRecipe)); err != nil {
			return State{}, errors.New("runtime build recipe is invalid; rebuild from the SDLC clone")
		}
	}
	return state, nil
}

func validateBuildRecipe(recipe []byte) error {
	if len(recipe) == 0 || len(recipe) > 1<<20 || !utf8.Valid(recipe) || bytes.IndexByte(recipe, 0) >= 0 {
		return errors.New("runtime Dockerfile exceeds its bounds or contains invalid text")
	}
	return nil
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
	return manager.BuildWithOptions(ctx, source, BuildOptions{})
}

// BuildWithOptions verifies a private candidate before changing the shared tag
// or saved state. The writer lock also excludes authenticated runtime leases.
func (manager Manager) BuildWithOptions(ctx context.Context, source string, options BuildOptions) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if options.SourceRevision != "" && !sourceCommitID.MatchString(options.SourceRevision) {
		return State{}, errors.New("archive source revision must be a full Git commit ID")
	}
	if options.ExpectedImageID != "" && !imageID.MatchString(options.ExpectedImageID) {
		return State{}, errors.New("expected runtime image identity is invalid")
	}
	if options.SourcePins && options.Pins != nil {
		return State{}, errors.New("source pins cannot be combined with explicit dependency pins")
	}
	if options.Pins != nil {
		if err := options.Pins.Validate(); err != nil {
			return State{}, err
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
	previous, previousErr := manager.read()
	if options.ValidatePrevious != nil && previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return State{}, fmt.Errorf("cannot verify recorded runtime before replacing it: %w", previousErr)
	}
	if previousErr == nil && options.ValidatePrevious != nil {
		if err := options.ValidatePrevious(ctx, previous); err != nil {
			return State{}, err
		}
	}
	if source == "" {
		if previousErr != nil {
			return State{}, previousErr
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
	pins := options.Pins
	if pins == nil && !options.SourcePins && previousErr == nil && previous.Source == root {
		pins = previous.DependencyPins
	}
	pins = copyPins(pins)
	contextDirectory, cleanupContext, err := buildContext(root)
	if err != nil {
		return State{}, err
	}
	defer cleanupContext()
	path := filepath.Join(contextDirectory, "Dockerfile")
	recipe, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	if pins != nil {
		recipe, err = runtimepins.ApplyDockerfile(recipe, *pins)
		if err != nil {
			return State{}, err
		}
		if err := os.WriteFile(path, recipe, 0600); err != nil {
			return State{}, err
		}
	}
	if err := validateBuildRecipe(recipe); err != nil {
		return State{}, err
	}
	engine, err := manager.engine(ctx)
	if err != nil {
		return State{}, err
	}
	oldID, inspectErr := manager.inspect(ctx, Image)
	if inspectErr != nil {
		if previousErr == nil && previous.Engine == engine {
			return State{}, fmt.Errorf("recorded shared image cannot be inspected; resolve its Docker state before rebuilding: %w", inspectErr)
		}
		oldID = ""
	}
	// A lost runtime.json must not hide saved work frozen to an existing tag.
	// This is the inspected image identity, not reconstructed recorded state.
	if errors.Is(previousErr, os.ErrNotExist) && oldID != "" && options.ValidatePrevious != nil {
		if err := options.ValidatePrevious(ctx, State{ImageID: oldID, Engine: engine, Source: root}); err != nil {
			return State{}, err
		}
	}
	if options.ExpectedImageID != "" && (previousErr != nil || previous.Engine != engine || previous.ImageID != options.ExpectedImageID || oldID != options.ExpectedImageID) {
		return State{}, errors.New("runtime changed since the update plan; check updates again")
	}
	if previousErr == nil && previous.Engine == engine && oldID != previous.ImageID {
		return State{}, errors.New("shared image was changed outside the CLI; resolve its Docker state before rebuilding")
	}
	if err := manager.noDependentContainers(ctx, oldID); err != nil {
		return State{}, err
	}
	if pins != nil {
		if err := manager.probeAuxiliaryImages(ctx, *pins); err != nil {
			return State{}, err
		}
	}
	revision := options.SourceRevision
	if revision == "" {
		revision = "unknown"
	}
	useCheckoutRevision := true
	if options.SourceRevision != "" {
		// A keg can live inside Homebrew's own Git repository. Only the
		// selected source's checkout can override verified archive provenance.
		top, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel").Output()
		checkout, resolveErr := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
		useCheckoutRevision = err == nil && resolveErr == nil && checkout == root
	}
	git := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	if output, err := git.Output(); err == nil && useCheckoutRevision {
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
	buildArgs := []string{"build", "--pull"}
	if options.Refresh {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, "--tag", candidate, "--label", "io.sdlc.managed=true", "--label", "io.sdlc.source-revision="+revision, contextDirectory)
	build := manager.Docker.Run
	if options.Refresh || pins != nil {
		build = manager.publicRun
	}
	if err := build(ctx, buildArgs...); err != nil {
		return State{}, err
	}
	id, err := manager.inspect(ctx, candidate)
	if err != nil {
		return State{}, err
	}
	probeContext, cancelProbe := context.WithTimeout(ctx, time.Minute)
	defer cancelProbe()
	probeArgs := isolatedRun("none", "1000:1000", "/bin/sh", id)
	tools, err := manager.Docker.Output(probeContext, append(probeArgs, "-ec", "codex --version; claude --version; gh --version; dotnet --version; docker --version; docker compose version")...)
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
	if pins != nil {
		if err := validatePinnedInventory(*inventory, *pins); err != nil {
			return State{}, err
		}
	}
	state := State{Version: 1, Source: root, Revision: revision, Engine: engine, ImageID: id, BuiltAt: time.Now().UTC(), Tools: strings.TrimSpace(string(tools)), Inventory: inventory, DependencyPins: pins, BuildRecipe: string(recipe)}
	if options.ValidateCandidate != nil {
		if err := options.ValidateCandidate(ctx, state); err != nil {
			return State{}, fmt.Errorf("built runtime failed update validation: %w", err)
		}
	}
	// Docker clients outside SDLC do not hold the writer lock. Recheck both the
	// old tag and its containers before replacing it or removing its image.
	currentEngine, err := manager.engine(ctx)
	if err != nil {
		return State{}, err
	}
	if currentEngine != engine {
		return State{}, errors.New("selected Docker engine changed during the build; candidate was not selected")
	}
	currentID, currentErr := manager.inspect(ctx, Image)
	if (oldID == "" && currentErr == nil) || (oldID != "" && (currentErr != nil || currentID != oldID)) {
		return State{}, errors.New("shared runtime changed during the build; candidate was not selected")
	}
	if err := manager.noDependentContainers(ctx, oldID); err != nil {
		return State{}, err
	}
	if err := manager.Docker.Run(ctx, "image", "tag", id, Image); err != nil {
		return State{}, errors.Join(err, manager.restoreSharedTag(oldID))
	}
	if err := manager.save(state); err != nil {
		return State{}, errors.Join(fmt.Errorf("cannot save runtime state: %w", err), manager.restoreSharedTag(oldID))
	}
	if oldID != "" && oldID != id {
		if _, err := manager.Docker.Output(ctx, "image", "rm", oldID); err != nil {
			return state, fmt.Errorf("new runtime is ready, but superseded image cleanup failed: %w", err)
		}
	}
	return state, nil
}

func (manager Manager) restoreSharedTag(oldID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if oldID != "" {
		return manager.Docker.Run(ctx, "image", "tag", oldID, Image)
	}
	_, err := manager.Docker.Output(ctx, "image", "rm", Image)
	return err
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
