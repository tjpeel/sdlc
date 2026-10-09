package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/terminallaunch"
)

func TestPreparedLaunchCapturesOnlySearchPath(t *testing.T) {
	root, err := filepath.EvalSymlinks(runGitFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")
	t.Setenv("SDLC_TEST_PRIVATE_VALUE", "disposable-private-value")
	id, err := terminallaunch.NewID()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = launchPreparedRun(context.Background(), root, runArgs("--dry-run"), "", id, true, &output, nil)
	if !errors.Is(err, terminallaunch.ErrUnsupported) {
		t.Fatal(err)
	}
	store, err := launchStore("")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.Directory, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "disposable-private-value") || strings.Contains(output.String(), "search_path") {
		t.Fatal("private environment leaked into the receipt or launch display")
	}
	request, err := store.Consume(id)
	if err != nil {
		t.Fatal(err)
	}
	if request.SearchPath == nil || *request.SearchPath != path {
		t.Fatal("launch did not capture the calling search path")
	}
}

func TestLaunchExecuteRestoresSearchPathBeforePlanValidation(t *testing.T) {
	root, err := filepath.EvalSymlinks(runGitFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, root)
	path := os.Getenv("PATH")
	args := runArgs("--repo", "example/project", "--dry-run")
	plan, err := offlinePlan(context.Background(), args, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := terminallaunch.NewID()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = launchPreparedRun(context.Background(), root, args, planHash(plan), id, true, &output, nil)
	if !errors.Is(err, terminallaunch.ErrUnsupported) {
		t.Fatal(err)
	}
	state := os.Getenv("SDLC_STATE_DIR")
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("git"); err == nil {
		t.Fatal("fixture did not remove Git from the helper environment")
	}
	output.Reset()
	if err := launchCommand(context.Background(), []string{"execute", "--id", id, "--state-dir", state}, &output); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PATH") != path {
		t.Fatal("helper did not restore the recorded path")
	}
	if docker, err := exec.LookPath("docker"); err != nil || docker != filepath.Join(root, "fake-bin", "docker") {
		t.Fatal("controller dependencies are unavailable after handoff", docker, err)
	}
	store, err := launchStore(state)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Status(id)
	if err != nil || receipt.State != "finished" || !strings.Contains(output.String(), "Offline plan") {
		t.Fatalf("offline helper failed: receipt=%+v error=%v output=%s", receipt, err, output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline helper invoked a connected command", err)
	}
}
