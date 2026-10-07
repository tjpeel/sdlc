package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
