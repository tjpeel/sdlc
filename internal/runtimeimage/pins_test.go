package runtimeimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

func testPins() runtimepins.Pins {
	return runtimepins.Pins{
		Arguments: map[string]string{
			"CODEX_VERSION": "0.1.0", "CLAUDE_VERSION": "2.0.0", "GH_VERSION": "2.0.0",
			"NPM_VERSION": "11.0.0", "YARN_VERSION": "1.22.22", "DOTNET_VERSION": "10.0.100",
			"COMPOSE_VERSION": "2.0.0", "BUILDX_VERSION": "0.1.0",
			"SKILLS_REVISION": strings.Repeat("a", 40), "AGENTS_REVISION": strings.Repeat("b", 40),
		},
		Images: map[string]string{
			"node":   "node:24.0.0-bookworm@" + oldImage,
			"golang": "golang:1.27.1-bookworm@" + oldImage,
			"docker": "docker:29.0.0-cli@" + oldImage,
		},
		SigningImage: "1password/op:2.39.0@" + oldImage,
		DaemonImage:  "docker:29.0.0-dind@" + oldImage,
	}
}

func pinsFixture(t *testing.T) (Manager, *fakeDocker, string, runtimepins.Pins, []byte) {
	t.Helper()
	manager, docker, root := fixture(t)
	pins := testPins()
	keys := make([]string, 0, len(pins.Arguments))
	for key := range pins.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	recipe := "FROM " + pins.Images["golang"] + " AS publisher-build\nFROM " + pins.Images["docker"] + " AS docker-tools\nFROM node:24-bookworm@" + oldImage + "\n"
	for _, key := range keys {
		value := pins.Arguments[key]
		if key == "CODEX_VERSION" {
			value = "0.0.1"
		}
		recipe += "ARG " + key + "=" + value + "\n"
	}
	original := []byte(recipe)
	if err := os.WriteFile(filepath.Join(root, "runtime", "Dockerfile"), original, 0600); err != nil {
		t.Fatal(err)
	}
	inventory := testInventory()
	inventory.Dependencies[10].Track = "24.0.0-bookworm"
	inventory.Dependencies = append(inventory.Dependencies, Dependency{Name: "yarn", Kind: "npm", Source: "yarn", Version: "1.22.22"})
	docker.inventoryOverride = &inventory
	docker.opVersion = "2.39.0"
	docker.daemonVersion = "29.0.0"
	return manager, docker, root, pins, original
}

func recordPrevious(t *testing.T, manager Manager, docker *fakeDocker, root string, pins *runtimepins.Pins) []byte {
	t.Helper()
	docker.current = oldImage
	state := State{Version: 1, Source: root, Engine: docker.engine, ImageID: oldImage, DependencyPins: pins}
	if err := manager.save(state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPrevious(t *testing.T, manager Manager, docker *fakeDocker, before []byte) {
	t.Helper()
	after, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil || !reflect.DeepEqual(before, after) || docker.current != oldImage {
		t.Fatal("failed update changed the shared image or saved state", err)
	}
	for _, removed := range docker.removed {
		if removed == oldImage || strings.HasPrefix(removed, "1password/op:") || strings.HasPrefix(removed, "docker:") {
			t.Fatal("failed update removed a selected dependency", removed)
		}
	}
}

func TestUpdatePinsArePrivatePersistedAndInherited(t *testing.T) {
	manager, docker, root, pins, original := pinsFixture(t)
	recordPrevious(t, manager, docker, root, nil)
	callbackCalled := false
	state, err := manager.BuildWithOptions(context.Background(), "", BuildOptions{
		Pins: &pins, Refresh: true, ExpectedImageID: oldImage,
		ValidateCandidate: func(_ context.Context, candidate State) error {
			callbackCalled = true
			if candidate.ImageID != newImage || candidate.DependencyPins == nil || docker.current != oldImage {
				t.Fatal("candidate validation ran after selection or without managed pins")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !callbackCalled || state.DependencyPins == nil || !reflect.DeepEqual(*state.DependencyPins, pins) {
		t.Fatal("validated managed pins were not recorded")
	}
	if joined := strings.Join(docker.buildArgs[0], " "); !strings.Contains(joined, "--pull --no-cache") {
		t.Fatal("update build reused cached layers", joined)
	}
	expected, err := runtimepins.ApplyDockerfile(original, pins)
	if err != nil || !reflect.DeepEqual(docker.buildRecipe, expected) || state.BuildRecipe != string(expected) {
		t.Fatal("build did not use the pinned private recipe", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "runtime", "Dockerfile"))
	if err != nil || !reflect.DeepEqual(after, original) {
		t.Fatal("build modified the source Dockerfile", err)
	}
	if _, err := os.Stat(docker.context); !os.IsNotExist(err) {
		t.Fatal("private pinned context was retained")
	}
	for _, source := range []string{"", root} {
		rebuilt, err := manager.Build(context.Background(), source)
		if err != nil || rebuilt.DependencyPins == nil || !reflect.DeepEqual(*rebuilt.DependencyPins, pins) {
			t.Fatal("same-source rebuild lost recorded pins", err)
		}
	}
	_, _, other := fixture(t)
	docker.inventoryOverride = nil
	reset, err := manager.Build(context.Background(), other)
	if err != nil || reset.DependencyPins != nil || string(docker.buildRecipe) != "synthetic fixture" {
		t.Fatal("new source inherited another source's pins", err)
	}
}

func TestUpdatePlanIdentityIsCheckedBeforePullOrBuild(t *testing.T) {
	for _, cause := range []string{"record changed", "tag changed", "engine changed", "missing state", "invalid expected"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root, pins, _ := pinsFixture(t)
			before := recordPrevious(t, manager, docker, root, nil)
			expected := oldImage
			switch cause {
			case "record changed":
				expected = newImage
			case "tag changed":
				docker.current = newImage
			case "engine changed":
				docker.engine = "other-engine"
			case "missing state":
				if err := os.Remove(filepath.Join(manager.Directory, "runtime.json")); err != nil {
					t.Fatal(err)
				}
			case "invalid expected":
				expected = "sdlc:local"
			}
			if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{Pins: &pins, ExpectedImageID: expected}); err == nil {
				t.Fatal("stale plan was accepted")
			}
			if docker.builds != 0 {
				t.Fatal("stale plan built a candidate")
			}
			for _, call := range docker.calls {
				if call[0] == "pull" || call[0] == "run" {
					t.Fatal("stale plan pulled or probed images")
				}
			}
			if cause == "record changed" || cause == "invalid expected" {
				assertPrevious(t, manager, docker, before)
			}
		})
	}
}

func TestInvalidAuxiliaryAndInventoryCandidatesPreservePrevious(t *testing.T) {
	for _, cause := range []string{"invalid signing ref", "invalid daemon ref", "signing pull", "daemon pull", "signing probe", "daemon probe", "signing version", "daemon version", "codex inventory", "node inventory", "docker inventory", "missing yarn", "callback"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root, pins, _ := pinsFixture(t)
			before := recordPrevious(t, manager, docker, root, nil)
			options := BuildOptions{Pins: &pins, ExpectedImageID: oldImage}
			switch cause {
			case "invalid signing ref":
				pins.SigningImage = "1password/op:latest"
			case "invalid daemon ref":
				pins.DaemonImage = "docker:29.0.0-dind"
			case "signing pull":
				docker.pullFailed = pins.SigningImage
			case "daemon pull":
				docker.pullFailed = pins.DaemonImage
			case "signing probe":
				docker.auxiliaryProbeFailed = pins.SigningImage
			case "daemon probe":
				docker.auxiliaryProbeFailed = pins.DaemonImage
			case "signing version":
				docker.opVersion = "2.38.0"
			case "daemon version":
				docker.daemonVersion = "28.0.0"
			case "codex inventory":
				docker.inventoryOverride.Dependencies[2].Version = "0.0.1"
			case "node inventory":
				docker.inventoryOverride.Dependencies[10].Version = newImage
			case "docker inventory":
				docker.inventoryOverride.Dependencies[5].Version = "28.0.0"
			case "missing yarn":
				docker.inventoryOverride.Dependencies = docker.inventoryOverride.Dependencies[:14]
			case "callback":
				options.ValidateCandidate = func(context.Context, State) error { return errors.New("Debian candidates remain") }
			}
			if _, err := manager.BuildWithOptions(context.Background(), "", options); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unverified candidate was selected or leaked diagnostics", err)
			}
			assertPrevious(t, manager, docker, before)
		})
	}
}

func TestAuxiliaryProbesAreOfflineUnprivilegedAndKeepImages(t *testing.T) {
	manager, docker, root, pins, _ := pinsFixture(t)
	if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{Pins: &pins}); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, call := range docker.calls {
		if call[0] != "run" || call[len(call)-1] != "--version" {
			continue
		}
		image := call[len(call)-2]
		found[image] = true
		joined := strings.Join(call, " ")
		for _, required := range []string{"--network none", "--user 1000:1000", "--read-only", "--cap-drop ALL", "no-new-privileges", "--pull never", "--memory 512m", "--cpus 1", "--pids-limit 128", "--log-driver none", "HTTP_PROXY="} {
			if !strings.Contains(joined, required) {
				t.Fatalf("missing auxiliary isolation %s", required)
			}
		}
		for _, arg := range call {
			if arg == "--mount" || arg == "--volume" || arg == "-v" || arg == "--privileged" {
				t.Fatal("auxiliary version probe received host data or privilege")
			}
		}
	}
	if !found[pins.SigningImage] || !found[pins.DaemonImage] {
		t.Fatal("auxiliary digest probes absent")
	}
	for _, image := range docker.removed {
		if image == pins.SigningImage || image == pins.DaemonImage {
			t.Fatal("auxiliary digest was removed")
		}
	}
}

func TestUpdateLockContainersAndOldTagChangesAreProtected(t *testing.T) {
	for _, cause := range []string{"shared lease", "active container", "container started during build", "engine changed during build", "tag changed during build", "tag changed before build"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root, pins, _ := pinsFixture(t)
			before := recordPrevious(t, manager, docker, root, nil)
			switch cause {
			case "shared lease":
				lease, err := filelock.AcquireContext(context.Background(), filepath.Join(manager.Directory, "runtime-build.lock"), filelock.Shared, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
			case "active container":
				docker.active = true
			case "container started during build":
				docker.buildHook = func() { docker.active = true }
			case "engine changed during build":
				docker.buildHook = func() { docker.engine = "other-engine" }
			case "tag changed during build":
				docker.buildHook = func() { docker.current = "sha256:" + strings.Repeat("c", 64) }
			case "tag changed before build":
				docker.current = newImage
			}
			if _, err := manager.BuildWithOptions(context.Background(), "", BuildOptions{Pins: &pins}); err == nil {
				t.Fatal("unprotected update was selected")
			}
			if cause != "tag changed during build" && cause != "tag changed before build" {
				assertPrevious(t, manager, docker, before)
			}
			if cause == "shared lease" || cause == "active container" || cause == "tag changed before build" {
				if docker.builds != 0 {
					t.Fatal("preflight protection started a build")
				}
			}
		})
	}
}

func TestManagedInventoryChecksEachInstalledPin(t *testing.T) {
	for _, source := range []string{"@openai/codex", "@anthropic-ai/claude-code", "cli/cli", "npm", "yarn", "dotnet", "docker/compose", "docker/buildx", "tjpeel/skills", "tjpeel/agents", "nodejs", "docker/cli", "library/node"} {
		t.Run(source, func(t *testing.T) {
			_, docker, _, pins, _ := pinsFixture(t)
			inventory := *docker.inventoryOverride
			if err := validatePinnedInventory(inventory, pins); err != nil {
				t.Fatal(err)
			}
			for index := range inventory.Dependencies {
				if inventory.Dependencies[index].Source == source {
					inventory.Dependencies[index].Version = "0.0.0"
				}
			}
			if err := validatePinnedInventory(inventory, pins); err == nil {
				t.Fatal("installed mismatch was accepted", source)
			}
		})
	}
}

func TestPinnedTagAndStateSaveFailuresRestoreThePreviousTag(t *testing.T) {
	for _, cause := range []string{"tag", "state"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker, root, pins, _ := pinsFixture(t)
			if cause == "tag" {
				before := recordPrevious(t, manager, docker, root, nil)
				docker.tagFailed = true
				if _, err := manager.BuildWithOptions(context.Background(), "", BuildOptions{Pins: &pins}); err == nil {
					t.Fatal("failed tag reported success")
				}
				assertPrevious(t, manager, docker, before)
				return
			}
			docker.current = oldImage
			if err := os.Mkdir(filepath.Join(manager.Directory, "runtime.json"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{Pins: &pins}); err == nil {
				t.Fatal("failed state save reported success")
			}
			if docker.current != oldImage {
				t.Fatal("failed pinned state save left the candidate selected")
			}
		})
	}
}

func TestBuildRecipeRecordsImmutablePrivateContext(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "managed"}[managed], func(t *testing.T) {
			manager, docker, root, pins, _ := pinsFixture(t)
			options := BuildOptions{}
			if managed {
				options.Pins = &pins
			}
			state, err := manager.BuildWithOptions(context.Background(), root, options)
			if err != nil {
				t.Fatal(err)
			}
			if state.BuildRecipe == "" || state.BuildRecipe != string(docker.buildRecipe) {
				t.Fatal("saved recipe does not reflect the exact private build context")
			}
			if err := os.WriteFile(filepath.Join(root, "runtime", "Dockerfile"), []byte("changed source recipe after build"), 0600); err != nil {
				t.Fatal(err)
			}
			after, err := manager.Status(context.Background())
			if err != nil || after.BuildRecipe != state.BuildRecipe {
				t.Fatal("source change replaced the selected runtime's recipe", err)
			}
		})
	}
}

func TestRuntimeRecipeBoundsAndLegacyState(t *testing.T) {
	for _, recipe := range []string{"", "FROM synthetic\n", strings.Repeat("a", 1<<20)} {
		manager, docker, root := fixture(t)
		state := State{Version: 1, Source: root, Engine: docker.engine, ImageID: oldImage, BuildRecipe: recipe}
		if err := manager.save(state); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.read(); err != nil {
			t.Fatal("valid or legacy recipe rejected", err)
		}
	}
	for _, recipe := range []string{"FROM synthetic\x00\n", string([]byte{0xff}), strings.Repeat("a", 1<<20+1)} {
		manager, _, _ := fixture(t)
		data := []byte("{\"version\":1,\"image_id\":\"" + oldImage + "\",\"build_recipe\":\"" + recipe + "\"}")
		if err := os.WriteFile(filepath.Join(manager.Directory, "runtime.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.read(); err == nil {
			t.Fatal("invalid saved recipe accepted")
		}
	}
}
