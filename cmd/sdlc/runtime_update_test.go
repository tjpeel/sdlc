package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

type updateFixture struct {
	buildFailure   bool
	sourceEdit     bool
	state          runtimeimage.State
	options        runtimeimage.BuildOptions
	builds         int
	wrongRuntime   bool
	stalePackages  bool
	packageFailure bool
}

func (fixture *updateFixture) Status(context.Context) (runtimeimage.State, error) {
	return fixture.state, nil
}

func (fixture *updateFixture) PackageUpdates(_ context.Context, state runtimeimage.State) ([]runtimeimage.PackageUpdate, error) {
	if fixture.packageFailure {
		return nil, errors.New("disposable public package failure")
	}
	installed := state.Inventory.Packages[0].Version
	return []runtimeimage.PackageUpdate{{Name: "git", Installed: installed, Candidate: "1:2.39.5-2", Architecture: "arm64", Update: installed != "1:2.39.5-2" || fixture.stalePackages}}, nil
}

func (fixture *updateFixture) BuildWithOptions(ctx context.Context, source string, options runtimeimage.BuildOptions) (runtimeimage.State, error) {
	fixture.builds++
	if fixture.buildFailure {
		return runtimeimage.State{}, errors.New("disposable build failure")
	}
	fixture.options = options
	if !options.Refresh || options.Pins == nil || options.ExpectedImageID != fixture.state.ImageID {
		return runtimeimage.State{}, errors.New("update omitted fresh-build or stale-plan protection")
	}
	if err := options.Pins.Validate(); err != nil {
		return runtimeimage.State{}, err
	}
	candidate := fixture.state
	candidate.ImageID = "sha256:" + strings.Repeat("b", 64)
	candidate.Source = source
	candidate.DependencyPins = options.Pins
	inventory := *fixture.state.Inventory
	inventory.Dependencies = append([]runtimeimage.Dependency{}, inventory.Dependencies...)
	inventory.Packages = []runtimeimage.Package{{Name: "git", Version: "1:2.39.5-2", Architecture: "arm64"}}
	arguments := map[string]string{"@openai/codex": "CODEX_VERSION", "@anthropic-ai/claude-code": "CLAUDE_VERSION", "npm": "NPM_VERSION", "yarn": "YARN_VERSION", "cli/cli": "GH_VERSION", "docker/compose": "COMPOSE_VERSION", "docker/buildx": "BUILDX_VERSION", "tjpeel/skills": "SKILLS_REVISION", "tjpeel/agents": "AGENTS_REVISION", "dotnet": "DOTNET_VERSION"}
	for i, dependency := range inventory.Dependencies {
		if argument := arguments[dependency.Source]; argument != "" {
			inventory.Dependencies[i].Version = options.Pins.Arguments[argument]
		}
		if dependency.Kind == "node" {
			inventory.Dependencies[i].Version = "24.2.0"
		}
		if dependency.Kind == "dotnet-runtime" {
			inventory.Dependencies[i].Version = "10.0.2"
			if fixture.wrongRuntime {
				inventory.Dependencies[i].Version = "10.0.1"
			}
		}
		if dependency.Source == "docker/cli" {
			inventory.Dependencies[i].Version = "29.8.3"
		}
		if dependency.Kind == "image" {
			parts := strings.Split(options.Pins.Images["node"], "@")
			inventory.Dependencies[i].Track = strings.TrimPrefix(parts[0], "node:")
			inventory.Dependencies[i].Version = parts[1]
		}
	}
	candidate.Inventory = &inventory
	recipe, err := readRuntimeRecipe(source)
	if err != nil {
		return runtimeimage.State{}, err
	}
	recipe, err = runtimepins.ApplyDockerfile(recipe, *options.Pins)
	if err != nil {
		return runtimeimage.State{}, err
	}
	candidate.BuildRecipe = string(recipe)
	if err := options.ValidateCandidate(ctx, candidate); err != nil {
		return runtimeimage.State{}, err
	}
	if fixture.sourceEdit {
		data, _ := os.ReadFile(filepath.Join(source, "runtime", "Dockerfile"))
		os.WriteFile(filepath.Join(source, "runtime", "Dockerfile"), append(data, []byte("# local edit during build\n")...), 0600)
	}
	fixture.state = candidate
	return candidate, nil
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	source := t.TempDir()
	recipe, err := os.ReadFile(filepath.Join("..", "..", "runtime", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime", "Dockerfile"), recipe, 0600); err != nil {
		t.Fatal(err)
	}
	inventory := statusInventory()
	inventory.Dependencies = append(inventory.Dependencies, runtimeimage.Dependency{Name: "Yarn", Kind: "npm", Source: "yarn", Version: "1.22.21"})
	return &updateFixture{state: runtimeimage.State{Version: 1, Source: source, ImageID: "sha256:" + strings.Repeat("a", 64), Inventory: inventory, BuildRecipe: string(recipe)}}
}

func updateHTTP(blockClaude bool) statusHTTP {
	return func(request *http.Request) (*http.Response, error) {
		var payload any
		switch request.URL.Host {
		case "registry.npmjs.org":
			name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/"), "/latest")
			versions := map[string]string{"@openai/codex": "0.159.4", "@anthropic-ai/claude-code": "2.1.288", "npm": "11.2.0", "yarn": "1.22.22"}
			if blockClaude && name == "@anthropic-ai/claude-code" {
				return nil, errors.New("synthetic metadata failure")
			}
			payload = map[string]string{"name": name, "version": versions[name]}
		case "api.github.com":
			path := request.URL.Path
			switch {
			case strings.Contains(path, "/commits/"):
				payload = map[string]string{"sha": strings.Repeat("a", 40)}
			case strings.Contains(path, "/compare/"):
				payload = map[string]string{"status": "ahead"}
			case strings.HasSuffix(path, "/tags"):
				payload = []map[string]string{{"name": "v29.8.3"}}
			default:
				versions := map[string]string{"cli": "2.3.0", "compose": "5.6.0", "buildx": "0.31.0"}
				parts := strings.Split(path, "/")
				payload = map[string]any{"tag_name": "v" + versions[parts[3]], "draft": false, "prerelease": false}
			}
		case "nodejs.org":
			payload = []map[string]string{{"version": "v24.2.0"}}
		case "go.dev":
			payload = []map[string]any{{"version": "go1.27.2", "stable": true}}
		case "builds.dotnet.microsoft.com":
			payload = map[string]any{"channel-version": "10.0", "releases": []any{map[string]any{"sdk": map[string]string{"version": "10.0.402"}, "runtime": map[string]string{"version": "10.0.2"}, "aspnetcore-runtime": map[string]string{"version": "10.0.2"}}}}
		case "hub.docker.com":
			payload = map[string]any{"next": nil, "results": []map[string]string{{"name": "2.40.0"}}}
		case "auth.docker.io":
			payload = map[string]string{"token": "fake-anonymous-registry-token"}
		case "registry-1.docker.io":
			payload = map[string]any{"schemaVersion": 2, "manifests": []any{}}
		default:
			return nil, fmt.Errorf("unapproved fixture metadata host %s", request.URL.Host)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		headers := make(http.Header)
		if request.URL.Host == "registry-1.docker.io" {
			digest := sha256.Sum256(data)
			headers.Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(digest[:]))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(bytes.NewReader(data))}, nil
	}
}

func TestRuntimeUpdatePreviewAcceptsRelativeSource(t *testing.T) {
	manager := newUpdateFixture(t)
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Rel(working, manager.state.Source)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, source, true, &output); err != nil {
		t.Fatal(err)
	}
	if manager.builds != 0 || manager.state.DependencyPins != nil || !strings.Contains(output.String(), "Preview complete") {
		t.Fatal("relative source preview changed the runtime", output.String())
	}
}

func TestRuntimeUpdatePreviewPreservesSourceAndSelectedRuntime(t *testing.T) {
	manager := newUpdateFixture(t)
	original := manager.state.BuildRecipe
	var output bytes.Buffer
	if err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, "", true, &output); err != nil {
		t.Fatal(err)
	}
	if manager.builds != 0 || manager.state.DependencyPins != nil || manager.state.BuildRecipe != original {
		t.Fatal("preview changed installation", manager.state)
	}
	recipe, err := readRuntimeRecipe(manager.state.Source)
	if err != nil || string(recipe) != original {
		t.Fatal("preview modified source recipe", err)
	}
	for _, want := range []string{"1Password resolver", "Docker check daemon", "Planned build actions", "Preview complete; no images pulled"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("missing preview evidence", want, output.String())
		}
	}
}

func TestRuntimeUpdateStopsBeforeBuildOnManagedMetadataFailure(t *testing.T) {
	manager := newUpdateFixture(t)
	before := manager.state.ImageID
	err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(true)}, "", false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "selected runtime is unchanged") || manager.builds != 0 || manager.state.ImageID != before {
		t.Fatal("incomplete plan changed runtime", err, manager.builds)
	}
}

func TestRuntimeUpdateCandidateValidationPrecedesSelection(t *testing.T) {
	for _, scenario := range []string{"old runtime pack", "stale Debian"} {
		t.Run(scenario, func(t *testing.T) {
			manager := newUpdateFixture(t)
			manager.wrongRuntime = scenario == "old runtime pack"
			manager.stalePackages = scenario == "stale Debian"
			before := manager.state.ImageID
			err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, "", false, &bytes.Buffer{})
			if err == nil || manager.builds != 1 || manager.state.ImageID != before || manager.state.DependencyPins != nil {
				t.Fatal("invalid candidate selected", err, manager.builds, manager.state)
			}
		})
	}
}

func TestRuntimeUpdateAppliesFreshBuildThenChecksInstalledInventory(t *testing.T) {
	manager := newUpdateFixture(t)
	original := manager.state.BuildRecipe
	var output bytes.Buffer
	if err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, "", false, &output); err != nil {
		t.Fatal(err, output.String())
	}
	if manager.builds != 1 || manager.state.DependencyPins == nil || manager.state.DependencyPins.Arguments["DOTNET_VERSION"] != "10.0.402" || !strings.Contains(output.String(), "Updated runtime selected") {
		t.Fatal("update did not select validated candidate", output.String())
	}
	recipe, err := readRuntimeRecipe(manager.state.Source)
	if err != nil || string(recipe) != original {
		t.Fatal("executing update edited source", err)
	}
}

func TestRuntimeRecipeRejectsLinkedAndOversizedInputs(t *testing.T) {
	manager := newUpdateFixture(t)
	path := filepath.Join(manager.state.Source, "runtime", "Dockerfile")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "outside"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeRecipe(manager.state.Source); err == nil {
		t.Fatal("linked source recipe accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (2<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeRecipe(manager.state.Source); err == nil {
		t.Fatal("oversized recipe accepted")
	}
}

func TestAgentToolsDockerfileWriteFollowsValidatedRuntime(t *testing.T) {
	for _, scenario := range []string{"dry-run", "success", "already-current", "build-failure", "local-edit"} {
		t.Run(scenario, func(t *testing.T) {
			manager := newUpdateFixture(t)
			source := runGitFixture(t)
			os.Mkdir(filepath.Join(source, "runtime"), 0700)
			original, _ := os.ReadFile(filepath.Join(manager.state.Source, "runtime", "Dockerfile"))
			target := filepath.Join(source, "runtime", "Dockerfile")
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			manager.state.Source = source
			if scenario == "already-current" {
				for index, dependency := range manager.state.Inventory.Dependencies {
					switch dependency.Source {
					case "@openai/codex":
						manager.state.Inventory.Dependencies[index].Version = "0.159.4"
					case "@anthropic-ai/claude-code":
						manager.state.Inventory.Dependencies[index].Version = "2.1.288"
					case "tjpeel/skills", "tjpeel/agents":
						manager.state.Inventory.Dependencies[index].Version = strings.Repeat("a", 40)
					}
				}
			}
			manager.buildFailure = scenario == "build-failure"
			manager.sourceEdit = scenario == "local-edit"
			oldID := manager.state.ImageID
			var output bytes.Buffer
			err := runtimeUpdateWithSourcePolicy(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, source, scenario == "dry-run", true, true, &output)
			after, _ := os.ReadFile(target)
			switch scenario {
			case "dry-run":
				if err != nil || manager.builds != 0 || !bytes.Equal(original, after) || !strings.Contains(output.String(), "Dockerfile: ARG CODEX_VERSION=") {
					t.Fatal(err, output.String())
				}
			case "build-failure":
				if err == nil || manager.state.ImageID != oldID || !bytes.Equal(original, after) {
					t.Fatal("failed build changed source/runtime", err)
				}
			case "local-edit":
				if err == nil || !strings.Contains(err.Error(), "runtime updated, but source Dockerfile was not written") || manager.state.ImageID == oldID || !bytes.Contains(after, []byte("# local edit during build")) {
					t.Fatal("partial result lost source edit or runtime", err)
				}
			case "success", "already-current":
				if err != nil || manager.state.ImageID == oldID || !strings.Contains(output.String(), "Review and commit") {
					t.Fatal(err, output.String())
				}
				for _, key := range []string{"CODEX_VERSION", "CLAUDE_VERSION", "SKILLS_REVISION", "AGENTS_REVISION"} {
					if !bytes.Contains(after, []byte("ARG "+key+"="+manager.options.Pins.Arguments[key])) {
						t.Fatal("source differs from runtime plan", key)
					}
				}
			}
		})
	}
}

func TestDockerfileFlagRequiresTargetedModeAndCheckout(t *testing.T) {
	for _, args := range [][]string{{"update", "--update-dockerfile"}, {"update", "--agent-tools", "--update-dockerfile"}, {"build", "--update-dockerfile"}} {
		if err := runtimeCommand(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid Dockerfile flag combination accepted", args)
		}
	}
	manager := newUpdateFixture(t)
	if err := runtimeUpdateWithSourcePolicy(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, manager.state.Source, false, true, true, io.Discard); err == nil || !strings.Contains(err.Error(), "checkout") || manager.builds != 0 {
		t.Fatal("package/noncheckout source accepted", err)
	}
}
