package runtimeimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

var oldImage = "sha256:" + strings.Repeat("a", 64)
var newImage = "sha256:" + strings.Repeat("b", 64)

type fakeDocker struct {
	inventoryUnavailable              bool
	endpoint, engine, osType, current string
	buildFailed, probeFailed, active  bool
	inventoryFailed                   bool
	calls                             [][]string
	packageOutput                     []byte
	packageFailed                     bool
	builds                            int
	context                           string
	removed                           []string
	buildArgs                         [][]string
	buildRecipe                       []byte
	inventoryOverride                 *Inventory
	opVersion, daemonVersion          string
	opInitializationOutput            []byte
	opInitializationFailed            bool
	pullFailed, auxiliaryProbeFailed  string
	buildHook                         func()
	tagFailed                         bool
}

func (docker *fakeDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	docker.calls = append(docker.calls, append([]string{}, args...))
	switch args[0] {
	case "rm":
		return nil, nil
	case "context":
		return []byte(fmt.Sprintf("%q", docker.endpoint)), nil
	case "info":
		return []byte(fmt.Sprintf(`{"id":%q,"os":%q}`, docker.engine, docker.osType)), nil
	case "ps":
		if docker.active {
			return []byte("retained-container"), nil
		}
		return nil, nil
	case "run":
		if args[len(args)-1] == signingInitialization {
			if docker.opInitializationFailed {
				return nil, errors.New("disposable private native initialization diagnostic")
			}
			return docker.opInitializationOutput, nil
		}
		if args[len(args)-1] == inventoryPath {
			if docker.inventoryUnavailable {
				return nil, errors.New("disposable private inventory error")
			}
			if docker.inventoryFailed {
				return []byte(`{"version":0}`), nil
			}
			inventory := testInventory()
			if docker.inventoryOverride != nil {
				inventory = *docker.inventoryOverride
			}
			data, _ := json.Marshal(inventory)
			return data, nil
		}
		if args[len(args)-1] == "package-updates" {
			if docker.packageFailed {
				return nil, errors.New("private diagnostic must stay hidden")
			}
			return docker.packageOutput, nil
		}
		if args[len(args)-1] == "--version" {
			image := args[len(args)-2]
			if docker.auxiliaryProbeFailed == image {
				return nil, errors.New("disposable private auxiliary diagnostic")
			}
			if strings.HasPrefix(image, "1password/op:") {
				return []byte(docker.opVersion), nil
			}
			if strings.HasPrefix(image, "docker:") {
				return []byte("Docker version " + docker.daemonVersion + ", build disposable"), nil
			}
		}
		if docker.probeFailed {
			return nil, errors.New("tool startup failed")
		}
		return []byte("codex test-version\nclaude test-version\ngh test-version"), nil
	case "image":
		if args[1] == "rm" {
			docker.removed = append(docker.removed, args[2])
			if args[2] == Image {
				docker.current = ""
			}
			return nil, nil
		}
		name := args[len(args)-1]
		if strings.HasPrefix(name, "sdlc:build-") {
			return []byte(newImage), nil
		}
		if docker.current == "" {
			return nil, errors.New("image missing")
		}
		return []byte(docker.current), nil
	}
	return nil, fmt.Errorf("unexpected Docker command: %v", args)
}

func (docker *fakeDocker) Run(_ context.Context, args ...string) error {
	docker.calls = append(docker.calls, append([]string{}, args...))
	switch args[0] {
	case "pull":
		if docker.pullFailed == args[1] {
			return errors.New("disposable private pull diagnostic")
		}
		return nil
	case "build":
		docker.builds++
		docker.context = args[len(args)-1]
		docker.buildArgs = append(docker.buildArgs, append([]string{}, args...))
		docker.buildRecipe, _ = os.ReadFile(filepath.Join(docker.context, "Dockerfile"))
		if docker.buildHook != nil {
			docker.buildHook()
		}
		if docker.buildFailed {
			return errors.New("build failed")
		}
		return nil
	case "image":
		if args[1] == "tag" && args[3] == Image {
			docker.current = args[2]
			if docker.tagFailed && args[2] == newImage {
				return errors.New("disposable tag failure after application")
			}
			return nil
		}
	}
	return fmt.Errorf("unexpected Docker command: %v", args)
}

func fixture(t *testing.T) (Manager, *fakeDocker, string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	root := filepath.Join(t.TempDir(), "source with spaces")
	for _, name := range runtimeAssets {
		path := filepath.Join(root, "runtime", name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"go.mod": "module github.com/tjpeel/sdlc\n\ngo 1.24.0\n", "cmd/sdlc-publisher/main.go": "package main\nfunc main() {}\n", "cmd/sdlc-test-proxy/main.go": "package main\nfunc main() {}\n"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	docker := &fakeDocker{endpoint: "unix:///tmp/fake-docker.sock", engine: "test-engine", osType: "linux"}
	manager := Manager{Directory: t.TempDir(), Docker: docker}
	return manager, docker, root
}

func TestBuildAndStatusUseOneImageAndSavedSource(t *testing.T) {
	manager, docker, root := fixture(t)
	state, err := manager.Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(root)
	if state.ImageID != newImage || docker.current != newImage || state.Source != resolved || docker.context == filepath.Join(resolved, "runtime") {
		t.Fatal("build did not use sanitized runtime context and verified image")
	}
	if _, err := os.Stat(docker.context); !os.IsNotExist(err) {
		t.Fatal("temporary runtime context was retained")
	}
	if state.Engine != "test-engine" || state.Tools == "" {
		t.Fatal("runtime record has no engine or tool evidence")
	}
	if _, err := manager.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(context.Background(), ""); err != nil {
		t.Fatal("saved source did not permit a repeat build", err)
	}
	if docker.builds != 2 || docker.current != newImage {
		t.Fatal("repeat build did not use the same shared image")
	}
}

func TestFailedBuildOrProbePreservesPriorRuntime(t *testing.T) {
	for _, stage := range []string{"build", "probe", "inventory", "inventory unavailable"} {
		t.Run(stage, func(t *testing.T) {
			manager, docker, root := fixture(t)
			docker.current = oldImage
			before := State{Version: 1, Source: root, Engine: "test-engine", ImageID: oldImage}
			if err := manager.save(before); err != nil {
				t.Fatal(err)
			}
			docker.buildFailed = stage == "build"
			docker.probeFailed = stage == "probe"
			docker.inventoryFailed = stage == "inventory"
			docker.inventoryUnavailable = stage == "inventory unavailable"
			if _, err := manager.Build(context.Background(), ""); err == nil {
				t.Fatal("failed candidate was selected")
			}
			after, err := manager.read()
			if err != nil || after.ImageID != oldImage || docker.current != oldImage {
				t.Fatal("failed candidate damaged the prior runtime", err)
			}
		})
	}
}

func TestPreflightBlocksUnsupportedEnginesAndDependentContainers(t *testing.T) {
	for _, cause := range []string{"remote", "windows-containers", "dependent-container", "lock"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root := fixture(t)
			switch cause {
			case "remote":
				docker.endpoint = "ssh://example.invalid"
			case "windows-containers":
				docker.osType = "windows"
			case "dependent-container":
				docker.current, docker.active = oldImage, true
			case "lock":
				lock, err := filelock.Acquire(filepath.Join(manager.Directory, "runtime-build.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			if _, err := manager.Build(context.Background(), root); err == nil || docker.builds != 0 {
				t.Fatal("failed preflight started a build", err)
			}
		})
	}
}

func TestStatusRejectsMissingOrReplacedRuntime(t *testing.T) {
	manager, docker, root := fixture(t)
	if _, err := manager.Status(context.Background()); err == nil {
		t.Fatal("unconfigured runtime reported ready")
	}
	if _, err := manager.Build(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, replaced := range []string{"", oldImage} {
		docker.current = replaced
		if _, err := manager.Status(context.Background()); err == nil {
			t.Fatal("missing or replaced image reported ready")
		}
	}
	docker.current, docker.engine = newImage, "different-engine"
	if _, err := manager.Status(context.Background()); err == nil {
		t.Fatal("unregistered engine reported ready")
	}
}

func TestFailedStateSaveRestoresPreviousSharedTag(t *testing.T) {
	manager, docker, root := fixture(t)
	docker.current = oldImage
	if err := os.Mkdir(filepath.Join(manager.Directory, "runtime.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Build(context.Background(), root); err == nil {
		t.Fatal("unrecorded runtime reported ready")
	}
	if docker.current != oldImage {
		t.Fatal("failed state save left the new shared image selected")
	}
}

func testInventory() Inventory {
	deps := []Dependency{
		{Name: "Node.js", Kind: "node", Source: "nodejs", Track: "24", Version: "24.0.0"},
		{Name: "npm", Kind: "npm", Source: "npm", Version: "11.0.0"},
		{Name: "Codex", Kind: "npm", Source: "@openai/codex", Version: "0.1.0"},
		{Name: "Claude", Kind: "npm", Source: "@anthropic-ai/claude-code", Version: "2.0.0"},
		{Name: "GitHub CLI", Kind: "github-release", Source: "cli/cli", Version: "2.0.0"},
		{Name: "Docker CLI", Kind: "github-release", Source: "docker/cli", Version: "29.0.0"},
		{Name: "Docker Compose", Kind: "github-release", Source: "docker/compose", Version: "2.0.0"},
		{Name: "Docker Buildx", Kind: "github-release", Source: "docker/buildx", Version: "0.1.0"},
		{Name: "Skills catalogue", Kind: "git", Source: "tjpeel/skills", Track: "main", Version: strings.Repeat("a", 40)},
		{Name: "Agents catalogue", Kind: "git", Source: "tjpeel/agents", Track: "main", Version: strings.Repeat("b", 40)},
		{Name: "Node base image", Kind: "image", Source: "library/node", Track: "24-bookworm", Version: oldImage},
		{Name: ".NET SDK", Kind: "dotnet-sdk", Source: "dotnet", Track: "10.0", Version: "10.0.100"},
		{Name: ".NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.NETCore.App", Track: "10.0", Version: "10.0.0"},
		{Name: "ASP.NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.AspNetCore.App", Track: "10.0", Version: "10.0.0"},
	}
	return Inventory{Version: 1, Platform: "linux/arm64", Distribution: "debian:bookworm", Dependencies: deps, Packages: []Package{{Name: "example-package", Version: "1:1.0-1", Architecture: "arm64"}}}
}
func TestInventoryValidation(t *testing.T) {
	if err := ValidateInventory(testInventory()); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []string{"version", "platform", "source", "duplicate dependency", "duplicate package", "missing tool", "package name", "version contents", "image digest", "track"} {
		t.Run(cause, func(t *testing.T) {
			inventory := testInventory()
			switch cause {
			case "version":
				inventory.Version = 2
			case "platform":
				inventory.Platform = "unsupported"
			case "source":
				inventory.Dependencies[4].Source = "example.invalid/private"
			case "duplicate dependency":
				inventory.Dependencies = append(inventory.Dependencies, inventory.Dependencies[0])
			case "duplicate package":
				inventory.Packages = append(inventory.Packages, inventory.Packages[0])
			case "missing tool":
				inventory.Dependencies = inventory.Dependencies[1:]
			case "package name":
				inventory.Packages[0].Name = "../example"
			case "version contents":
				inventory.Packages[0].Version = "example credential text"
			case "image digest":
				inventory.Dependencies[10].Version = "mutable-tag"
			case "track":
				inventory.Dependencies[0].Track = "25"
			}
			if ValidateInventory(inventory) == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
	for _, data := range [][]byte{[]byte(`{"version":1}`), []byte{0xff}, make([]byte, maximumInventorySize+1)} {
		if _, err := decodeInventory(data); err == nil {
			t.Fatal("invalid inventory bytes accepted")
		}
	}
}
func TestInventoryProbeIsOfflineAndStatusDoesNotProbe(t *testing.T) {
	manager, docker, root := fixture(t)
	state, err := manager.Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Inventory == nil {
		t.Fatal("build inventory not recorded")
	}
	found := false
	for _, call := range docker.calls {
		if call[0] == "run" && call[len(call)-1] == inventoryPath {
			joined := strings.Join(call, " ")
			for _, required := range []string{"--network none", "--read-only", "--cap-drop ALL", "no-new-privileges", "--pull never", "HTTP_PROXY="} {
				if !strings.Contains(joined, required) {
					t.Fatalf("missing isolation %s", required)
				}
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no inventory probe")
	}
	docker.calls = nil
	if _, err = manager.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range docker.calls {
		if call[0] == "run" {
			t.Fatal("local status executed a container")
		}
	}
	state.Inventory = nil
	if err = manager.save(state); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Status(context.Background()); err != nil {
		t.Fatal("legacy runtime state rejected", err)
	}
}
func TestPackageUpdatesIsolationAndCompleteResults(t *testing.T) {
	manager, docker, _ := fixture(t)
	inventory := testInventory()
	state := State{Version: 1, Engine: "test-engine", ImageID: newImage, Inventory: &inventory}
	docker.packageOutput = []byte(`[{"name":"example-package","installed":"1:1.0-1","candidate":"1:1.0-2","architecture":"arm64","update":true}]`)
	updates, err := manager.PackageUpdates(context.Background(), state)
	if err != nil || len(updates) != 1 || !updates[0].Update {
		t.Fatal(updates, err)
	}
	var call []string
	for _, candidate := range docker.calls {
		if candidate[0] == "run" {
			call = candidate
		}
	}
	joined := strings.Join(call, " ")
	for _, required := range []string{"--network bridge", "--user 0:0", "--memory 512m", "--cpus 1", "--pids-limit 128", "--log-driver none", "size=256m", "--read-only", "--cap-drop ALL", "no-new-privileges", "--pull never", "HTTP_PROXY=", newImage, dependencyHelper} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing isolation %s", required)
		}
	}
	for _, arg := range call {
		if arg == "--mount" || arg == "--volume" || arg == "-v" {
			t.Fatal("package probe mounted host data")
		}
	}
	for _, output := range []string{`[]`, `[{"name":"other","installed":"1:1.0-1","candidate":"","architecture":"arm64","update":false}]`, `[{"name":"example-package","installed":"1:1.0-1","candidate":"","architecture":"arm64","update":true}]`} {
		docker.packageOutput = []byte(output)
		if _, err = manager.PackageUpdates(context.Background(), state); err == nil {
			t.Fatal("invalid package result accepted")
		}
	}
	docker.packageFailed = true
	if _, err = manager.PackageUpdates(context.Background(), state); err == nil || strings.Contains(err.Error(), "private diagnostic") {
		t.Fatal("package error leaked raw diagnostics", err)
	}
	state.Inventory = nil
	if _, err = manager.PackageUpdates(context.Background(), state); err == nil {
		t.Fatal("legacy state silently inferred inventory")
	}
}

func TestCodexPlatformAliasInventory(t *testing.T) {
	for _, tag := range []string{"linux-x64", "linux-arm64", "darwin-x64", "darwin-arm64", "win32-x64", "win32-arm64"} {
		inventory := testInventory()
		alias := Dependency{Name: "@openai/codex-" + tag, Kind: "npm", Source: "@openai/codex", Track: tag, Version: "0.159.3-" + tag}
		inventory.Dependencies = append(inventory.Dependencies, alias)
		if err := ValidateInventory(inventory); err != nil {
			t.Fatalf("valid platform %s rejected: %v", tag, err)
		}
	}
	for _, cause := range []string{"unsupported tag", "wrong source", "wrong suffix", "wrong alias", "missing main package"} {
		t.Run(cause, func(t *testing.T) {
			inventory := testInventory()
			alias := Dependency{Name: "@openai/codex-linux-arm64", Kind: "npm", Source: "@openai/codex", Track: "linux-arm64", Version: "0.159.3-linux-arm64"}
			switch cause {
			case "unsupported tag":
				alias.Track = "unknown"
			case "wrong source":
				alias.Source = "example-published"
			case "wrong suffix":
				alias.Version = "0.159.3-linux-x64"
			case "wrong alias":
				alias.Name = "example-alias"
			case "missing main package":
				inventory.Dependencies = append(inventory.Dependencies[:2], inventory.Dependencies[3:]...)
			}
			inventory.Dependencies = append(inventory.Dependencies, alias)
			if ValidateInventory(inventory) == nil {
				t.Fatal("invalid alias inventory accepted")
			}
		})
	}
}
