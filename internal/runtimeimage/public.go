package runtimeimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimepins"
)

type publicDocker interface {
	PublicRun(context.Context, ...string) error
}

// PullPublic pulls approved public sidecars without host registry credentials.
// The ordinary Docker interface remains usable by offline test implementations.
func (manager Manager) PullPublic(ctx context.Context, reference string) error {
	if runtimepins.ValidateSigningImage(reference) != nil && runtimepins.ValidateDaemonImage(reference) != nil {
		return errors.New("public sidecar pull requires an approved exact digest reference")
	}
	return manager.publicRun(ctx, "pull", reference)
}

func (manager Manager) publicRun(ctx context.Context, args ...string) error {
	if docker, ok := manager.Docker.(publicDocker); ok {
		return docker.PublicRun(ctx, args...)
	}
	return manager.Docker.Run(ctx, args...)
}

// PublicRun gives the native Docker client a disposable config with no auths,
// credential helpers, contexts or builder state. Only endpoint resolution uses
// the host configuration; the registry operation always uses the fresh one.
func (docker LocalDocker) PublicRun(ctx context.Context, args ...string) error {
	if len(args) == 0 || (args[0] != "pull" && args[0] != "build") {
		return errors.New("public Docker operation must be a pull or build")
	}
	endpoint, err := docker.publicEndpoint(ctx)
	if err != nil {
		return err
	}
	var pluginDirectories []string
	if args[0] == "build" {
		pluginDirectories, err = docker.publicPluginDirectories(ctx)
		if err != nil {
			return err
		}
	}
	directory, err := os.MkdirTemp("", "sdlc-public-docker-")
	if err != nil {
		return errors.New("cannot prepare private public-image Docker configuration")
	}
	defer os.RemoveAll(directory)
	configuration := struct {
		Auths             map[string]any `json:"auths"`
		PluginDirectories []string       `json:"cliPluginsExtraDirs,omitempty"`
	}{Auths: map[string]any{}, PluginDirectories: pluginDirectories}
	data, err := json.Marshal(configuration)
	if err != nil || os.WriteFile(filepath.Join(directory, "config.json"), data, 0600) != nil {
		return errors.New("cannot write private public-image Docker configuration")
	}
	command := exec.CommandContext(ctx, "docker", append([]string{"--config", directory, "--host", endpoint}, args...)...)
	command.Env = publicDockerEnvironment(os.Environ(), directory)
	command.Stdout, command.Stderr = docker.Stdout, docker.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("public docker %s: %w", args[0], err)
	}
	return nil
}

func (docker LocalDocker) publicEndpoint(ctx context.Context) (string, error) {
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		metadataContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		output, err := docker.Output(metadataContext, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}")
		cancel()
		if err != nil || json.Unmarshal(output, &endpoint) != nil {
			return "", errors.New("cannot resolve the selected local Docker endpoint for public images")
		}
	}
	if (!strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://")) || strings.ContainsAny(endpoint, "\x00\r\n") {
		return "", errors.New("public image operations require a local Docker engine")
	}
	return endpoint, nil
}

func (docker LocalDocker) publicPluginDirectories(ctx context.Context) ([]string, error) {
	// Resolve only installation and system locations. Native Docker config may
	// point at arbitrary executable plugins, so never inherit its plugin metadata.
	// The Docker executable and plugins installed alongside it remain trusted
	// host software, just like the PATH-selected Docker client itself.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("native Docker client is unavailable")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, errors.New("cannot resolve native Docker installation")
	}
	installation := filepath.Dir(binary)
	directories := []string{
		installation,
		filepath.Clean(filepath.Join(installation, "..", "cli-plugins")),
		"/opt/homebrew/lib/docker/cli-plugins",
		"/usr/local/lib/docker/cli-plugins", "/usr/local/libexec/docker/cli-plugins",
		"/usr/lib/docker/cli-plugins", "/usr/libexec/docker/cli-plugins",
	}
	for _, directory := range directories {
		plugin := filepath.Join(directory, "docker-buildx")
		if filepath.Ext(binary) == ".exe" {
			plugin += ".exe"
		}
		info, err := os.Stat(plugin)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("native Docker build plugin is unavailable")
		}
		return []string{directory}, nil
	}
	// Docker can still use its compiled-in system plugin paths or legacy builder.
	return nil, nil
}

func publicDockerEnvironment(environment []string, directory string) []string {
	// An allowlist also excludes credentials used by arbitrary credential helpers,
	// proxy authentication, Docker custom headers and host Buildx configuration.
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SystemRoot": true, "WINDIR": true, "COMSPEC": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true,
		"TERM": true, "COLUMNS": true, "DOCKER_DEFAULT_PLATFORM": true, "DOCKER_BUILDKIT": true,
	}
	result := make([]string, 0, len(allowed)+4)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			result = append(result, entry)
		}
	}
	return append(result, "DOCKER_CONFIG="+directory, "HOME="+directory, "USERPROFILE="+directory, "XDG_CONFIG_HOME="+filepath.Join(directory, "xdg-config"))
}
