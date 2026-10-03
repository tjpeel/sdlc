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
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maximumInventorySize = 4 * 1024 * 1024
const inventoryPath = "/opt/sdlc/runtime-inventory.json"
const dependencyHelper = "/usr/local/lib/sdlc-dependencies.py"

type Inventory struct {
	Version      int          `json:"version"`
	Platform     string       `json:"platform"`
	Distribution string       `json:"distribution"`
	Dependencies []Dependency `json:"dependencies"`
	Packages     []Package    `json:"packages"`
}
type Dependency struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Track   string `json:"track,omitempty"`
	Version string `json:"version"`
}
type Package struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}
type PackageUpdate struct {
	Name         string `json:"name"`
	Installed    string `json:"installed"`
	Candidate    string `json:"candidate"`
	Architecture string `json:"architecture"`
	Update       bool   `json:"update"`
}

var (
	packageName          = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,127}$`)
	architecture         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	dependencyVersion    = regexp.MustCompile(`^[0-9][A-Za-z0-9.+:~_-]{0,127}$`)
	npmSource            = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9._-]+$`)
	displayName          = regexp.MustCompile(`^[A-Za-z0-9 @/().+_-]{1,160}$`)
	majorTrack           = regexp.MustCompile(`^[0-9]{1,3}$`)
	dotnetTrack          = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)
	imageTrack           = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	commitRevision       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	codexPlatformTrack   = regexp.MustCompile(`^(?:linux|darwin|win32)-(?:x64|arm64)$`)
	codexPlatformVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-(?:linux|darwin|win32)-(?:x64|arm64)$`)
)

// ValidateInventory accepts only public package identities and supported sources.
func ValidateInventory(inventory Inventory) error {
	invalid := errors.New("runtime inventory is invalid; rebuild from the SDLC clone")
	if inventory.Version != 1 || (inventory.Platform != "linux/amd64" && inventory.Platform != "linux/arm64") || inventory.Distribution != "debian:bookworm" || len(inventory.Dependencies) == 0 || len(inventory.Dependencies) > 4096 || len(inventory.Packages) == 0 || len(inventory.Packages) > 10000 {
		return invalid
	}
	dependencies := map[string]bool{}
	present := map[string]bool{}
	for _, entry := range inventory.Dependencies {
		if !displayName.MatchString(entry.Name) || len(entry.Source) > 256 {
			return invalid
		}
		if entry.Kind != "git" && entry.Kind != "image" && !dependencyVersion.MatchString(entry.Version) {
			return invalid
		}
		switch entry.Kind {
		case "npm":
			if !npmSource.MatchString(entry.Source) {
				return invalid
			}
			if entry.Track != "" {
				if !codexPlatformTrack.MatchString(entry.Track) || entry.Source != "@openai/codex" || entry.Name != "@openai/codex-"+entry.Track || !codexPlatformVersion.MatchString(entry.Version) || !strings.HasSuffix(entry.Version, "-"+entry.Track) {
					return invalid
				}
			}
		case "github-release":
			if entry.Track != "" || (entry.Source != "cli/cli" && entry.Source != "docker/cli" && entry.Source != "docker/compose" && entry.Source != "docker/buildx") {
				return invalid
			}
		case "git":
			if (entry.Source != "tjpeel/skills" && entry.Source != "tjpeel/agents") || entry.Track != "main" || !commitRevision.MatchString(entry.Version) {
				return invalid
			}
		case "image":
			if entry.Source != "library/node" || !imageTrack.MatchString(entry.Track) || !imageID.MatchString(entry.Version) {
				return invalid
			}
		case "node":
			if entry.Source != "nodejs" || !majorTrack.MatchString(entry.Track) || !strings.HasPrefix(entry.Version, entry.Track+".") {
				return invalid
			}
		case "dotnet-sdk":
			if entry.Source != "dotnet" || !dotnetTrack.MatchString(entry.Track) || !strings.HasPrefix(entry.Version, entry.Track+".") {
				return invalid
			}
		case "dotnet-runtime":
			if (entry.Source != "Microsoft.NETCore.App" && entry.Source != "Microsoft.AspNetCore.App") || !dotnetTrack.MatchString(entry.Track) || !strings.HasPrefix(entry.Version, entry.Track+".") {
				return invalid
			}
		default:
			return invalid
		}
		key := entry.Kind + "\x00" + entry.Source + "\x00" + entry.Track + "\x00" + entry.Version
		if dependencies[key] {
			return invalid
		}
		dependencies[key] = true
		if entry.Kind != "npm" || entry.Track == "" {
			present[entry.Kind+"/"+entry.Source] = true
		}
	}
	for _, required := range []string{"node/nodejs", "npm/npm", "npm/@openai/codex", "npm/@anthropic-ai/claude-code", "github-release/cli/cli", "github-release/docker/cli", "github-release/docker/compose", "github-release/docker/buildx", "git/tjpeel/skills", "git/tjpeel/agents", "image/library/node", "dotnet-sdk/dotnet", "dotnet-runtime/Microsoft.NETCore.App", "dotnet-runtime/Microsoft.AspNetCore.App"} {
		if !present[required] {
			return invalid
		}
	}
	packages := map[string]bool{}
	for _, entry := range inventory.Packages {
		if !packageName.MatchString(entry.Name) || !dependencyVersion.MatchString(entry.Version) || !architecture.MatchString(entry.Architecture) {
			return invalid
		}
		key := entry.Name + ":" + entry.Architecture
		if packages[key] {
			return invalid
		}
		packages[key] = true
	}
	return nil
}

func decodeInventory(data []byte) (*Inventory, error) {
	if len(data) > maximumInventorySize || !utf8.Valid(data) {
		return nil, errors.New("runtime inventory exceeds its bounds or is not UTF-8")
	}
	var inventory Inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return nil, errors.New("built runtime returned invalid inventory JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("built runtime returned trailing inventory data")
	}
	if err := ValidateInventory(inventory); err != nil {
		return nil, err
	}
	return &inventory, nil
}

func isolatedRun(network, user, entrypoint, image string) []string {
	args := []string{"run", "--rm", "--pull", "never", "--network", network, "--user", user, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "512m", "--cpus", "1", "--pids-limit", "128", "--log-driver", "none", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777", "--workdir", "/tmp"}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "ftp_proxy", "no_proxy", "all_proxy"} {
		args = append(args, "--env", key+"=")
	}
	return append(args, "--entrypoint", entrypoint, image)
}

func (manager Manager) saveInventory(ctx context.Context, image string) (*Inventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := isolatedRun("none", "1000:1000", "/bin/cat", image)
	output, err := manager.Docker.Output(ctx, append(args, inventoryPath)...)
	if err != nil {
		return nil, errors.New("built runtime inventory is unavailable; candidate was not selected")
	}
	return decodeInventory(output)
}

// PackageUpdates is an explicit online probe. Status and provider operations
// never call it. The disposable container gets no mounts or account state.
func (manager Manager) PackageUpdates(ctx context.Context, state State) ([]PackageUpdate, error) {
	if state.Inventory == nil {
		return nil, errors.New("runtime has no build inventory; rebuild before checking package updates")
	}
	if err := ValidateInventory(*state.Inventory); err != nil {
		return nil, err
	}
	if !imageID.MatchString(state.ImageID) {
		return nil, errors.New("runtime image identity is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	engine, err := manager.engine(ctx)
	if err != nil {
		return nil, errors.New("local Docker engine is unavailable for package checks")
	}
	if engine != state.Engine {
		return nil, errors.New("selected Docker engine differs from the recorded runtime")
	}
	var token [8]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, errors.New("cannot prepare isolated package check")
	}
	name := "sdlc-package-check-" + hex.EncodeToString(token[:])
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		manager.Docker.Output(cleanup, "rm", "--force", name)
	}()
	args := isolatedRun("bridge", "0:0", "python3", state.ImageID)
	args = append(args[:1], append([]string{"--name", name}, args[1:]...)...)
	output, err := manager.Docker.Output(ctx, append(args, dependencyHelper, "package-updates")...)
	if err != nil {
		return nil, errors.New("Debian package candidates are unavailable; retry the public package check")
	}
	if len(output) > maximumInventorySize || !utf8.Valid(output) {
		return nil, errors.New("package candidate output exceeds its bounds")
	}
	var updates []PackageUpdate
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&updates); err != nil {
		return nil, errors.New("package candidate output is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("package candidate output contains trailing data")
	}
	expected := map[string]Package{}
	for _, item := range state.Inventory.Packages {
		expected[item.Name+":"+item.Architecture] = item
	}
	if len(updates) != len(expected) {
		return nil, errors.New("package candidate output does not cover the installed inventory")
	}
	for _, item := range updates {
		key := item.Name + ":" + item.Architecture
		installed, ok := expected[key]
		if !ok || item.Installed != installed.Version || (item.Candidate != "" && !dependencyVersion.MatchString(item.Candidate)) || (item.Update && (item.Candidate == "" || item.Candidate == item.Installed)) {
			return nil, fmt.Errorf("package candidate output does not match the installed inventory")
		}
		delete(expected, key)
	}
	return updates, nil
}
