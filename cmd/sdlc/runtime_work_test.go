package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestRuntimeGuardPreservesStoppedHumanWorkAndAllowsReadyStandalone(t *testing.T) {
	j, marker := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = image
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root)
	previous := runtimeimage.State{ImageID: image, Source: j.Plan.Root}
	err := guard(context.Background(), previous)
	if err == nil || !strings.Contains(err.Error(), j.ID) || !strings.Contains(err.Error(), "--sdlc-only") {
		t.Fatal("pending run did not protect runtime", err)
	}
	j.State = "ready"
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background(), previous); err != nil {
		t.Fatal("completed standalone run blocked update", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("guard invoked connected provider")
	}
}

func TestRuntimeGuardFindsForgottenRunsAndRejectsCorruptJournals(t *testing.T) {
	j, _ := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = image
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runs", j.ID+".json")); err != nil {
		t.Fatal(err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: image}); err == nil || !strings.Contains(err.Error(), j.ID) {
		t.Fatal("forgotten run was not found", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background(), runtimeimage.State{ImageID: image}); err == nil || !strings.Contains(err.Error(), "invalid checkpoint") {
		t.Fatal("corrupt saved run was ignored", err)
	}
	if err := runtimeBuildSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root, true, &bytes.Buffer{})(context.Background(), runtimeimage.State{ImageID: image}); err == nil || !strings.Contains(err.Error(), "invalid checkpoint") {
		t.Fatal("force ignored a corrupt saved run", err)
	}
}

func TestRuntimeGuardProtectsPreparedAndUnmergedFeatures(t *testing.T) {
	driver, result := featureAdoptionFixture(t, "ready")
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{}}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), driver.plan.Root)
	previous := runtimeimage.State{ImageID: driver.settings.ImageID}
	for _, mode := range []string{"prepared", "ready", "merged"} {
		if mode != "prepared" {
			for _, ticket := range state.Plan.Tickets {
				next := result
				next.State = mode
				state.Results[ticket.File] = next
			}
		}
		if err := workseries.Save(dir, &state); err != nil {
			t.Fatal(err)
		}
		err := guard(context.Background(), previous)
		if mode == "merged" {
			if err != nil {
				t.Fatal("merged feature blocked update", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "feature TASK-1") {
			t.Fatal("incomplete feature did not protect runtime", mode, err)
		}
		before := dashboardTree(t, filepath.Join(driver.plan.Root, ".sdlc", "work"))
		var output bytes.Buffer
		if err := runtimeBuildSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), driver.plan.Root, true, &output)(context.Background(), previous); err != nil {
			t.Fatal("force refused stopped feature", err)
		}
		if (mode != "merged") != strings.Contains(output.String(), "feature TASK-1") {
			t.Fatal("force did not identify affected stopped work", output.String())
		}
		if !reflect.DeepEqual(before, dashboardTree(t, filepath.Join(driver.plan.Root, ".sdlc", "work"))) {
			t.Fatal("force changed saved feature files")
		}
	}
}

func TestRuntimeGuardProtectsLiveForgottenReadyRun(t *testing.T) {
	j, _ := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID, j.State = image, "ready"
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runs", j.ID+".json")); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Acquire(filepath.Join(dir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root)
	if err := guard(context.Background(), runtimeimage.State{ImageID: image}); err == nil || !strings.Contains(err.Error(), j.ID) {
		t.Fatal("live forgotten run was not protected", err)
	}
	if err := runtimeBuildSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), j.Plan.Root, true, &bytes.Buffer{})(context.Background(), runtimeimage.State{ImageID: image}); err == nil || !strings.Contains(err.Error(), j.ID) {
		t.Fatal("force ignored a live forgotten controller", err)
	}
}

func TestRuntimeBuildForceReplacesDefaultWithoutChangingSavedFeature(t *testing.T) {
	driver, _ := featureAdoptionFixture(t, "ready")
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := workseries.Save(directory, &workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{}}); err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, driver.plan.Root)
	bin := filepath.Dir(marker)
	oldImage, newImage := driver.settings.ImageID, "sha256:"+strings.Repeat("b", 64)
	t.Setenv("SDLC_TEST_OLD_IMAGE", oldImage)
	t.Setenv("SDLC_TEST_NEW_IMAGE", newImage)
	t.Setenv("SDLC_UPDATE_PROJECT_ROOT", driver.plan.Root)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	script := `#!/bin/sh
set -eu
directory="$(dirname "$0")"
for argument do last="$argument"; done
case "$1" in
  context) printf '"unix:///tmp/offline-runtime.sock"\n' ;;
  info) printf '{"id":"offline-engine","os":"linux"}\n' ;;
  ps) ;;
  build) printf built > "$directory/built" ;;
  run)
    if [ "$last" = /opt/sdlc/runtime-inventory.json ]; then cat "$directory/inventory.json";
    else printf 'offline tool versions\n'; fi ;;
  image)
    case "$2" in
      inspect)
        case "$last" in
          sdlc:build-*) printf '%s\n' "$SDLC_TEST_NEW_IMAGE" ;;
          sdlc:local) cat "$directory/current-image" ;;
          *) exit 98 ;;
        esac ;;
      tag) printf '%s\n' "$3" > "$directory/current-image" ;;
      rm) printf '%s\n' "$3" >> "$directory/removed-images" ;;
      *) exit 98 ;;
    esac ;;
  *) exit 98 ;;
esac
`
	inventory, err := json.Marshal(statusInventory())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"docker": []byte(script), "current-image": []byte(oldImage + "\n"), "inventory.json": inventory} {
		if err := os.WriteFile(filepath.Join(bin, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	source, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(file), "../.."))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := json.Marshal(runtimeimage.State{Version: 1, Source: source, Engine: "offline-engine", ImageID: oldImage})
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := os.Getenv("SDLC_STATE_DIR")
	if err := os.MkdirAll(stateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, "runtime.json"), previous, 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, filepath.Join(driver.plan.Root, ".sdlc", "work"))
	var output bytes.Buffer
	args := []string{"build", "--source", source}
	if err := runtimeCommand(context.Background(), args, &output, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "feature TASK-1") {
		t.Fatal("ordinary build failed to protect stopped feature", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "built")); !os.IsNotExist(err) {
		t.Fatal("ordinary build reached Docker build", err)
	}
	output.Reset()
	if err := runtimeCommand(context.Background(), append(args, "--force"), &output, &bytes.Buffer{}); err != nil {
		t.Fatal("forced build failed", err, output.String())
	}
	var selected runtimeimage.State
	data, err := os.ReadFile(filepath.Join(stateDirectory, "runtime.json"))
	if err != nil || json.Unmarshal(data, &selected) != nil || selected.ImageID != newImage {
		t.Fatal("forced build did not record replacement runtime", err)
	}
	removed, err := os.ReadFile(filepath.Join(bin, "removed-images"))
	if err != nil || strings.Contains(string(removed), oldImage) {
		t.Fatal("forced build removed previous image", err, string(removed))
	}
	if !strings.Contains(output.String(), "feature TASK-1") || !strings.Contains(output.String(), "cannot resume against the replacement runtime") {
		t.Fatal("forced build omitted affected feature and resume consequence", output.String())
	}
	if !reflect.DeepEqual(before, dashboardTree(t, filepath.Join(driver.plan.Root, ".sdlc", "work"))) {
		t.Fatal("forced build changed saved feature files")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("forced build invoked connected provider")
	}
}

func TestRuntimeScanRejectsSymlinkedMetadataEvenWhenWorkIsMissing(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".sdlc")); err != nil {
		t.Fatal(err)
	}
	if err := scanRuntimeWork(context.Background(), root, "image", map[string]bool{}); err == nil {
		t.Fatal("symlinked metadata was ignored")
	}
}

func TestRuntimeGuardResolvesSourceRootAliases(t *testing.T) {
	j, _ := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = image
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(j.Plan.Root, alias); err != nil {
		t.Fatal(err)
	}
	guard := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), alias)
	if err := guard(context.Background(), runtimeimage.State{ImageID: image, Source: alias}); err == nil || !strings.Contains(err.Error(), "runtime replacement would prevent resuming") {
		t.Fatal("root alias was not resolved", err)
	}
}

type missingRecordGuardDocker struct {
	image     string
	mutations int
}

func (d *missingRecordGuardDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "context":
		return []byte(`"unix:///tmp/disposable-docker.sock"`), nil
	case "info":
		return []byte(`{"id":"offline-engine","os":"linux"}`), nil
	case "image":
		if len(args) > 1 && args[1] == "inspect" {
			return []byte(d.image), nil
		}
	}
	d.mutations++
	return nil, errors.New("unexpected Docker operation")
}
func (d *missingRecordGuardDocker) Run(context.Context, ...string) error {
	d.mutations++
	return errors.New("unexpected Docker build")
}

func TestRuntimeGuardProtectsPausedWorkWhenRuntimeRecordIsLost(t *testing.T) {
	j, _ := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = image
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	_, filename, _, _ := runtime.Caller(0)
	source, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "../.."))
	if err != nil {
		t.Fatal(err)
	}
	docker := &missingRecordGuardDocker{image: image}
	manager := runtimeimage.Manager{Directory: os.Getenv("SDLC_STATE_DIR"), Docker: docker}
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	_, err = manager.BuildWithOptions(context.Background(), source, runtimeimage.BuildOptions{SourcePins: true, ValidatePrevious: runtimeSavedWorkGuard(manager.Directory, source)})
	if err == nil || !strings.Contains(err.Error(), j.ID) || docker.mutations != 0 {
		t.Fatal("lost runtime record bypassed paused-work protection", err, docker.mutations)
	}
}

func TestRuntimeGuardAllowsMovedBuildSourceButRequiresRegisteredRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", stateDir)
	old := filepath.Join(t.TempDir(), "old-clone")
	if err := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), root)(context.Background(), runtimeimage.State{ImageID: "image", Source: old}); err != nil {
		t.Fatal("moved build-only clone blocked relocation", err)
	}
	j, _ := attentionFixture(t)
	if err := os.Rename(j.Plan.Root, j.Plan.Root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(j.Plan.Root+"-moved", j.Plan.Root) })
	if err := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), root)(context.Background(), runtimeimage.State{ImageID: "image", Source: j.Plan.Root}); err == nil {
		t.Fatal("missing registered work root was skipped")
	}
}

func TestRuntimeGuardFindsForgottenRunInUpdateCallerRoot(t *testing.T) {
	j, _ := attentionFixture(t)
	image := "sha256:" + strings.Repeat("a", 64)
	j.ImageID = image
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runs", j.ID+".json")); err != nil {
		t.Fatal(err)
	}
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(source)
	t.Setenv("SDLC_UPDATE_PROJECT_ROOT", j.Plan.Root)
	if err := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), source)(context.Background(), runtimeimage.State{ImageID: image, Source: source}); err == nil || !strings.Contains(err.Error(), j.ID) {
		t.Fatal("original update caller's forgotten run was not found", err)
	}
}

func TestRuntimeGuardRejectsInvalidOrMissingUpdateCallerRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", state)
	for _, caller := range []string{"relative/project", filepath.Join(root, "missing")} {
		t.Setenv("SDLC_UPDATE_PROJECT_ROOT", caller)
		if err := runtimeSavedWorkGuard(state, root)(context.Background(), runtimeimage.State{ImageID: "image", Source: root}); err == nil {
			t.Fatal("unsafe caller root was ignored", caller)
		}
	}
}
