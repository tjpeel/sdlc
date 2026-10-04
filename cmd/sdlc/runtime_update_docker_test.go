package main

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
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
	"github.com/tjpeel/sdlc/internal/workrun"
)

var updateDockerImageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// This wrapper redirects only the exact shared runtime tag. Existing image
// identities can never be removed, including when the installed image seeds
// the disposable alias. Newly built identities are removed only while every
// remaining tag belongs to this test.
type namespacedUpdateDocker struct {
	local       runtimeimage.LocalDocker
	tag         string
	preexisting map[string]bool
	ownedTags   map[string]bool
	ownedIDs    map[string]bool
}

func (docker *namespacedUpdateDocker) arguments(args []string) []string {
	result := append([]string{}, args...)
	for index, argument := range result {
		if argument == runtimeimage.Image {
			result[index] = docker.tag
		}
	}
	return result
}

func (docker *namespacedUpdateDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	args = docker.arguments(args)
	if len(args) >= 3 && args[0] == "image" && args[1] == "rm" {
		if len(args) != 3 {
			return nil, errors.New("test image cleanup must name one owned resource")
		}
		target := args[2]
		if docker.preexisting[target] {
			return nil, nil
		}
		if docker.ownedIDs[target] {
			data, err := docker.local.Output(ctx, "image", "inspect", "--format", "{{json .RepoTags}}", target)
			if err != nil {
				return nil, err
			}
			var tags []string
			if json.Unmarshal(data, &tags) != nil {
				return nil, errors.New("cannot verify owned image cleanup")
			}
			for _, tag := range tags {
				if !docker.ownedTags[tag] {
					return nil, nil
				}
			}
		} else if !docker.ownedTags[target] {
			return nil, errors.New("test refused to remove an unowned Docker image")
		}
	}
	data, err := docker.local.Output(ctx, args...)
	if err == nil && len(args) >= 3 && args[0] == "image" && args[1] == "inspect" && docker.ownedTags[args[len(args)-1]] {
		id := strings.TrimSpace(string(data))
		if updateDockerImageID.MatchString(id) && !docker.preexisting[id] {
			docker.ownedIDs[id] = true
		}
	}
	return data, err
}

func (docker *namespacedUpdateDocker) Run(ctx context.Context, args ...string) error {
	if len(args) > 0 && (args[0] == "build" || args[0] == "pull") {
		return docker.PublicRun(ctx, args...)
	}
	args = docker.arguments(args)
	if len(args) >= 4 && args[0] == "image" && args[1] == "tag" && !docker.ownedTags[args[len(args)-1]] {
		return errors.New("test refused to select an unowned Docker tag")
	}
	return docker.local.Run(ctx, args...)
}

func (docker *namespacedUpdateDocker) PublicRun(ctx context.Context, args ...string) error {
	args = docker.arguments(args)
	if len(args) > 0 && args[0] == "build" {
		found := false
		for index := 1; index+1 < len(args); index++ {
			if args[index] != "--tag" {
				continue
			}
			tag := args[index+1]
			if !strings.HasPrefix(tag, "sdlc:build-") {
				return errors.New("test build must use a private candidate tag")
			}
			docker.ownedTags[tag] = true
			found = true
		}
		if !found {
			return errors.New("test build has no private candidate tag")
		}
	}
	return docker.local.PublicRun(ctx, args...)
}

// Explicit opt-in. Public metadata, apt/NuGet package restores and image pulls
// may use the network. No provider, vault or GitHub account state is mounted.
func TestRuntimeUpdateDocker(t *testing.T) {
	if os.Getenv("SDLC_RUNTIME_UPDATE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_RUNTIME_UPDATE_DOCKER_TESTS=1 for an isolated credential-free runtime update")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Minute)
	defer cancel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate public runtime source")
	}
	source, err := filepath.EvalSymlinks(filepath.Clean(filepath.Join(filepath.Dir(file), "../..")))
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.CreateTemp("", "sdlc-runtime-update-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Private build diagnostics:", log.Name())
	defer func() {
		log.Close()
		if !t.Failed() {
			os.Remove(log.Name())
		}
	}()
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	local := runtimeimage.LocalDocker{Stdout: log, Stderr: log}
	docker := &namespacedUpdateDocker{local: local, tag: "sdlc-runtime-update-test:" + hex.EncodeToString(token[:]), preexisting: map[string]bool{}, ownedTags: map[string]bool{}, ownedIDs: map[string]bool{}}
	docker.ownedTags[docker.tag] = true
	listing, err := local.Output(ctx, "image", "ls", "--quiet", "--no-trunc")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range strings.Fields(string(listing)) {
		if updateDockerImageID.MatchString(id) {
			docker.preexisting[id] = true
		}
	}
	original, originalErr := local.Output(ctx, "image", "inspect", "--format", "{{.Id}}", runtimeimage.Image)
	originalID := strings.TrimSpace(string(original))
	if originalErr == nil && !updateDockerImageID.MatchString(originalID) {
		t.Fatal("installed image identity is invalid")
	}
	if originalErr == nil {
		docker.preexisting[originalID] = true
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		tags := make([]string, 0, len(docker.ownedTags))
		for tag := range docker.ownedTags {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			if _, err := local.Output(cleanup, "image", "inspect", "--format", "{{.Id}}", tag); err != nil {
				continue
			}
			if _, err := docker.Output(cleanup, "image", "rm", tag); err != nil {
				t.Errorf("owned test tag cleanup failed: %v", err)
			}
		}
		for id := range docker.ownedIDs {
			if _, err := local.Output(cleanup, "image", "inspect", "--format", "{{.Id}}", id); err != nil {
				continue
			}
			if _, err := docker.Output(cleanup, "image", "rm", id); err != nil {
				t.Errorf("owned test image cleanup failed: %v", err)
			}
		}
		current, err := local.Output(cleanup, "image", "inspect", "--format", "{{.Id}}", runtimeimage.Image)
		if (originalErr == nil && (err != nil || strings.TrimSpace(string(current)) != originalID)) || (originalErr != nil && err == nil) {
			t.Error("actual shared runtime changed during the isolated test")
		}
	}()
	manager := runtimeimage.Manager{Directory: t.TempDir(), Docker: docker}
	seeded := false
	if originalErr == nil {
		containers, err := local.Output(ctx, "ps", "--all", "--filter", "ancestor="+originalID, "--format", "{{.ID}}")
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(containers)) == 0 {
			if seedErr := seedInstalledUpdateRuntime(ctx, manager, docker, source, originalID); seedErr == nil {
				seeded = true
				t.Log("Seeded only the disposable alias from the installed public inventory")
			} else {
				fmt.Fprintln(log, "Installed inventory cannot seed test:", seedErr)
			}
		}
	}
	if !seeded {
		t.Log("Building a credential-free disposable seed runtime")
		if _, err := manager.BuildWithOptions(ctx, source, runtimeimage.BuildOptions{Refresh: true}); err != nil {
			t.Fatalf("isolated seed build: %v; private diagnostics: %s", err, log.Name())
		}
	}
	before, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Updating the separate test runtime using current public metadata; build diagnostics:", log.Name())
	if err := runtimeUpdate(ctx, manager, runtimeupdates.New(), source, false, log); err != nil {
		t.Fatalf("isolated runtime update: %v; private diagnostics: %s", err, log.Name())
	}
	state, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.DependencyPins == nil || state.Inventory == nil || state.BuildRecipe == "" {
		t.Fatal("updated runtime did not retain pins, inventory and exact recipe")
	}
	if err := state.DependencyPins.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := runtimeimage.ValidateInventory(*state.Inventory); err != nil {
		t.Fatal(err)
	}
	packages, err := manager.PackageUpdates(ctx, state)
	if err != nil || len(packages) != len(state.Inventory.Packages) {
		t.Fatal("updated Debian package verification is incomplete", err)
	}
	for _, item := range packages {
		if item.Candidate == "" || item.Update {
			t.Fatalf("updated Debian package %s is not current", item.Name)
		}
	}
	t.Logf("Validated isolated runtime %s (seed %s), SDK %s, %d Debian packages", state.ImageID, before.ImageID, state.DependencyPins.Arguments["DOTNET_VERSION"], len(packages))
	if os.Getenv("SDLC_RUNTIME_UPDATE_DOTNET_TESTS") == "1" {
		t.Run("public dotnet integration", func(t *testing.T) { checkUpdatedDotnetFixture(t, ctx, manager, state, source, log) })
	}
}

func seedInstalledUpdateRuntime(ctx context.Context, manager runtimeimage.Manager, docker *namespacedUpdateDocker, source, id string) error {
	// The copied build recipe is root-owned mode 0600. This read-only metadata
	// container has no mounts or network and reads only the two public image files.
	probe := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "512m", "--cpus", "1", "--pids-limit", "128", "--log-driver", "none", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777", "--env", "HOME=/tmp", "--entrypoint", "/bin/cat", id}
	data, err := docker.Output(ctx, append(probe, "/opt/sdlc/runtime-inventory.json")...)
	if err != nil || len(data) > 4<<20 || !utf8.Valid(data) {
		return errors.New("installed inventory is unavailable or out of bounds")
	}
	var inventory runtimeimage.Inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return err
	}
	if err := runtimeimage.ValidateInventory(inventory); err != nil {
		return err
	}
	recipe, err := docker.Output(ctx, append(probe, "/opt/sdlc/runtime.Dockerfile")...)
	legacyRecipe := false
	if err != nil {
		presenceProbe := append([]string{}, probe...)
		presenceProbe[len(presenceProbe)-2] = "/bin/sh"
		absent, absentErr := docker.Output(ctx, append(presenceProbe, "-ec", "test ! -e /opt/sdlc/runtime.Dockerfile && test ! -L /opt/sdlc/runtime.Dockerfile && printf missing")...)
		if absentErr != nil || string(absent) != "missing" {
			return errors.New("installed recipe cannot be safely read")
		}
		if _, err := readRuntimeRecipe(source); err != nil {
			return err
		}
		// Preserve legacy provenance: production planning reads the saved source
		// recipe when an older runtime did not capture its exact build recipe.
		fmt.Fprintln(docker.local.Stdout, "Installed test seed predates recipe capture; source recipe is inferred as in legacy update planning")
		recipe = nil
		err = nil
		legacyRecipe = true
	}
	if err != nil || (!legacyRecipe && len(recipe) == 0) || len(recipe) > 1<<20 || !utf8.Valid(recipe) || bytes.IndexByte(recipe, 0) >= 0 {
		return errors.New("installed recipe is unavailable or out of bounds")
	}
	data, err = docker.Output(ctx, "info", "--format", `{"id":{{json .ID}},"os":{{json .OSType}}}`)
	if err != nil {
		return err
	}
	var engine struct {
		ID string `json:"id"`
		OS string `json:"os"`
	}
	if json.Unmarshal(data, &engine) != nil || engine.ID == "" || engine.OS != "linux" {
		return errors.New("test requires a local Linux-container Docker engine")
	}
	state := runtimeimage.State{Version: 1, Source: source, Revision: "installed-public-test-seed", Engine: engine.ID, ImageID: id, BuiltAt: time.Now().UTC(), Inventory: &inventory, BuildRecipe: string(recipe)}
	data, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if err := docker.Run(ctx, "image", "tag", id, runtimeimage.Image); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(manager.Directory, "runtime.json"), data, 0600); err != nil {
		return err
	}
	_, err = manager.Status(ctx)
	return err
}

var updatedDotnetPublicFiles = []string{
	".dockerignore", ".gitignore", "Directory.Build.props", "Dockerfile", "README.md", "Smoke.slnx", "global.json", "compose.yml", "test-environment.example",
	"docs/specification.md", "docs/tickets/01-count-items.md", "scripts/integration.py",
	"src/Smoke.Api/ItemName.cs", "src/Smoke.Api/Program.cs", "src/Smoke.Api/Smoke.Api.csproj",
	"tests/Smoke.IntegrationTests/ApiTests.cs", "tests/Smoke.IntegrationTests/Smoke.IntegrationTests.csproj", "tests/Smoke.UnitTests/ItemNameTests.cs", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj",
}

func checkUpdatedDotnetFixture(t *testing.T, ctx context.Context, manager runtimeimage.Manager, state runtimeimage.State, source string, diagnostics io.Writer) {
	t.Helper()
	fixture := t.TempDir()
	repository := filepath.Join(fixture, "repository")
	inputs := filepath.Join(fixture, "inputs")
	home := filepath.Join(fixture, "git-home")
	for _, path := range []string{repository, inputs, home} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	public, err := os.OpenRoot(filepath.Join(source, "examples", "dotnet-smoke"))
	if err != nil {
		t.Fatal(err)
	}
	defer public.Close()
	for _, relative := range updatedDotnetPublicFiles {
		parts := strings.Split(relative, "/")
		var sourceInfo os.FileInfo
		for index := range parts {
			info, err := public.Lstat(filepath.Join(parts[:index+1]...))
			if err != nil || info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) || (index == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() > 1<<20)) {
				t.Fatal("public .NET fixture contains an unsafe file", relative)
			}
			sourceInfo = info
		}
		file, err := public.Open(filepath.FromSlash(relative))
		if err != nil {
			t.Fatal("cannot open public .NET fixture", relative, err)
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(sourceInfo, opened) || !opened.Mode().IsRegular() {
			file.Close()
			t.Fatal("public .NET fixture changed while opening", relative)
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		if err != nil || len(data) > 1<<20 {
			t.Fatal("cannot read bounded public .NET fixture", relative, err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		path := filepath.Join(repository, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		base := []string{"-C", repository, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "user.name=Disposable Example", "-c", "user.email=example@example.invalid"}
		command := exec.CommandContext(ctx, "git", append(base, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0"}
		command.Stdout, command.Stderr = diagnostics, diagnostics
		if err := command.Run(); err != nil {
			t.Fatal("unsigned disposable Git fixture failed", err)
		}
	}
	git("init", "--initial-branch=main", "--template=")
	git(append([]string{"add", "--"}, updatedDotnetPublicFiles...)...)
	git("commit", "-m", "Create disposable public .NET fixture")
	environment := "SMOKE_MONGO_CONNECTION_STRING=mongodb://mongo:27017/?serverSelectionTimeoutMS=2000\nSMOKE_COMPOSE_MARKER=fake-dotenv-runtime-update\nSMOKE_ENV_FILE_MARKER=fake-dotenv-runtime-update\n"
	if err := os.WriteFile(filepath.Join(inputs, ".env"), []byte(environment), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import os,pathlib,subprocess
assert os.getuid()==1000
assert os.environ['DOCKER_HOST']=='unix:///run/sdlc/docker.sock'
for path in ['/provider-auth','/github-auth','/var/run/docker.sock',os.environ['CODEX_HOME'],os.environ['CLAUDE_CONFIG_DIR']]:
 assert not pathlib.Path(path).exists(),path
assert 'SMOKE_COMPOSE_MARKER' not in os.environ
assert 'SMOKE_ENV_FILE_MARKER' not in os.environ
assert 'SMOKE_ENV_FILE_MARKER=fake-dotenv-runtime-update' in pathlib.Path('.env').read_text()
subprocess.run(['dotnet','build','Smoke.slnx','--disable-build-servers','-m:1','-p:UseSharedCompilation=false'],check=True)
subprocess.run(['dotnet','test','tests/Smoke.UnitTests/Smoke.UnitTests.csproj','--no-build','--no-restore'],check=True)
subprocess.run(['python3','scripts/integration.py'],check=True)
print('Updated runtime public .NET build, unit and integration checks passed')`
	var output bytes.Buffer
	checker := workrun.DockerChecker{Runtime: manager, ImageID: state.ImageID, DockerTests: true, DaemonImage: state.DependencyPins.DaemonImage, InputDirectory: inputs}
	if err := checker.Check(ctx, repository, [][]string{{"python3", "-c", script}}, io.MultiWriter(diagnostics, &output)); err != nil {
		t.Fatal("updated runtime .NET checks failed", err)
	}
	if !strings.Contains(output.String(), "Updated runtime public .NET build, unit and integration checks passed") {
		t.Fatal("updated runtime .NET check evidence is incomplete")
	}
	t.Log("Updated runtime public .NET build, unit and integration checks passed")
}
