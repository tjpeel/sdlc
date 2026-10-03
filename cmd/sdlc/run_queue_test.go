package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

// Only immutable engine/image identity probes are allowed. Auth storage,
// containers, provider clients and GitHub must remain untouched in these tests.
func queuedRunFixture(t *testing.T) (string, string, string) {
	t.Helper()
	runGitFixture(t)
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", state)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	for _, variable := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "TF_BUILD", "BUILD_BUILDID"} {
		t.Setenv(variable, "")
	}
	marker := forbidConnectedRunCommands(t, state)
	image := "sha256:" + strings.Repeat("a", 64)
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  'context inspect') printf 'context\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '"unix:///fake.sock"' ;;
  'info --format') printf 'info\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '{"id":"fake-engine","os":"linux"}' ;;
  'image inspect') printf 'image\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '%s' ;;
  *) printf forbidden > "$(dirname "$0")/called"; exit 99 ;;
esac
`, image)
	if err := os.WriteFile(filepath.Join(state, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: image, Source: state})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return state, marker, filepath.Join(state, "provider-codex.lock")
}

func waitForQueuedRun(t *testing.T, state string, completed <-chan error) runstatus.View {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		views, err := runstatus.New(state).List(time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if len(views) == 1 && views[0].Live && views[0].Activity.WaitReason == "provider_busy" {
			return views[0]
		}
		select {
		case err := <-completed:
			t.Fatalf("run stopped before reporting its queue: %v", err)
		case <-deadline.C:
			t.Fatalf("run did not register a live provider wait: %+v", views)
		case <-ticker.C:
		}
	}
}

func waitForQueuedRunExit(t *testing.T, completed <-chan error) error {
	t.Helper()
	select {
	case err := <-completed:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after queue release or cancellation")
		return nil
	}
}

func assertQueuedRunNoConnectedCalls(t *testing.T, state, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("auth container, provider client or GitHub command launched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "auth-installation.json")); !os.IsNotExist(err) {
		t.Fatalf("test initialized provider authentication storage: %v", err)
	}
}

func TestRunCommandRegistersProviderQueueBeforeCheckingAuthentication(t *testing.T) {
	state, marker, providerPath := queuedRunFixture(t)
	held, err := filelock.Acquire(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	completed := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		completed <- runCommand(ctx, runArgs("--repo", "example/project"), &output)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("queued run did not exit during test cleanup")
		}
	}()
	queued := waitForQueuedRun(t, state, completed)
	if queued.State != "prepared" || queued.Activity.WaitingProvider != "codex" || queued.Activity.WaitingSince.IsZero() || queued.Stopped || queued.Journal == nil {
		t.Fatalf("incorrect queued lifecycle: %+v", queued)
	}
	assertQueuedRunNoConnectedCalls(t, state, marker)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	err = waitForQueuedRunExit(t, completed)
	if !errors.Is(err, workrun.ErrStopped) {
		t.Fatalf("missing auth did not retain a stopped checkpoint: %v\n%s", err, output.String())
	}
	views, err := runstatus.New(state).List(time.Now().UTC())
	if err != nil || len(views) != 1 {
		t.Fatalf("registered run lost after queue: %+v %v", views, err)
	}
	stopped := views[0]
	if stopped.ID != queued.ID || stopped.State != "blocked" || !stopped.Stopped || stopped.Live || !strings.Contains(stopped.StopReason, "login") || stopped.Activity.WaitReason != "" {
		t.Fatalf("missing-auth lifecycle: %+v", stopped)
	}
	if !strings.Contains(output.String(), "codex") || !strings.Contains(strings.ToLower(output.String()), "waiting") {
		t.Fatalf("queue was not reported to the terminal: %s", output.String())
	}
	assertQueuedRunNoConnectedCalls(t, state, marker)
	lease, err := filelock.Acquire(providerPath)
	if err != nil {
		t.Fatalf("provider lease leaked after missing authentication: %v", err)
	}
	lease.Close()
}

func TestRunCommandCancelsQueuedProviderAndRetainsRegisteredCheckpoint(t *testing.T) {
	state, marker, providerPath := queuedRunFixture(t)
	held, err := filelock.Acquire(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	completed := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		completed <- runCommand(ctx, runArgs("--repo", "example/project"), &output)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("queued run did not exit during test cleanup")
		}
	}()
	queued := waitForQueuedRun(t, state, completed)
	cancel()
	err = waitForQueuedRunExit(t, completed)
	if !errors.Is(err, workrun.ErrStopped) {
		t.Fatalf("queue cancellation lost resumable checkpoint: %v\n%s", err, output.String())
	}
	views, err := runstatus.New(state).List(time.Now().UTC())
	if err != nil || len(views) != 1 {
		t.Fatalf("cancelled queue lost registration: %+v %v", views, err)
	}
	stopped := views[0]
	if stopped.ID != queued.ID || stopped.State != "blocked" || !stopped.Stopped || stopped.Live || !strings.Contains(stopped.StopReason, "cancel") {
		t.Fatalf("cancelled queue lifecycle: %+v", stopped)
	}
	assertQueuedRunNoConnectedCalls(t, state, marker)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{providerPath, filepath.Join(queued.Directory, "run.lock")} {
		lease, err := filelock.Acquire(path)
		if err != nil {
			t.Fatalf("cancelled controller leaked lease %s: %v", filepath.Base(path), err)
		}
		lease.Close()
	}
}
