package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
	"github.com/tjpeel/sdlc/internal/workrun"
)

// Runtime identity and GitHub identity freezing use offline fake Docker responses.
// Provider authentication, signing-key retrieval and publication remain forbidden.
func queuedRunFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := runGitFixture(t)
	for _, args := range [][]string{{"config", "user.name", "Example User"}, {"config", "user.email", "example@example.invalid"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git identity: %v: %s", err, output)
		}
	}
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
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), public...)
	fingerprint := sha256.Sum256(encoded)
	bootstrap := filepath.Join(state, "disposable-bootstrap")
	if err := os.WriteFile(bootstrap, []byte("offline-disposable-test-data"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := signing.Profile{Version: 1, ID: "queue-fixture", Reference: "op://YOUR_VAULT/YOUR_SIGNING_KEY/private key?ssh-format=openssh", PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(encoded), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(fingerprint[:]), BootstrapFile: bootstrap}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "profiles.local.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	const installation = "dddddddddddddddddddddddddddddddd"
	if err := os.WriteFile(filepath.Join(state, "github-installation.json"), []byte(`{"id":"`+installation+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	volume := "sdlc-github-auth-" + installation + "-github"
	metadata, err := json.Marshal([]map[string]any{{"Name": volume, "Driver": "local", "Options": map[string]string{}, "Labels": map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "github-auth", "io.sdlc.installation": installation, "io.sdlc.provider": "github"}}})
	if err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, state)
	image := "sha256:" + strings.Repeat("a", 64)
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  'context inspect') printf 'context\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '"unix:///fake.sock"' ;;
  'info --format') printf 'info\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '{"id":"fake-engine","os":"linux"}' ;;
  'image inspect') printf 'image\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '%s' ;;
  'volume ls') printf '%%s\n' '%s' ;;
  'volume inspect') [ "$3" = '%s' ] || exit 99; printf '%%s\n' '%s' ;;
  'ps --all') ;;
  'run --rm')
    last=''; previous=''; selected=''
    for argument in "$@"; do
      previous="$last"
      last="$argument"
      case "$argument" in 'type=volume,src=%s,dst=/github-auth,volume-nocopy,readonly') selected=yes ;; esac
    done
    [ "$selected" = yes ] || { printf forbidden > "$(dirname "$0")/called"; exit 99; }
    if [ "$previous" = repository ] && [ "$last" = example/project ]; then
      printf '%%s\n' '{"id":99,"name":"example/project","push":true}'; exit 0
    fi
    case "$last" in
      status) printf '%%s\n' '{"state":"stored"}' ;;
      identity) printf 'github-identity\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '{"id":1,"login":"testuser"}' ;;
      *) printf forbidden > "$(dirname "$0")/called"; exit 99 ;;
    esac ;;
  *) printf forbidden > "$(dirname "$0")/called"; exit 99 ;;
esac
`, image, volume, volume, string(metadata), volume)
	if err := os.WriteFile(filepath.Join(state, "fake-bin", "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: image, Source: state})
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
			identity := views[0].Journal.Plan.PublicationIdentity
			if identity == nil || identity.GitHubID != 1 || identity.GitHubLogin != "testuser" || identity.ProfileID != "queue-fixture" || identity.RepositoryID != 99 || identity.RepositoryName != "example/project" {
				t.Fatalf("queued run lost frozen fixture identity: %+v", identity)
			}
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
		t.Fatalf("unexpected provider, signing or connected command launched: %v", err)
	}
	calls, err := os.ReadFile(filepath.Join(state, "fake-bin", "metadata-calls"))
	if err != nil || strings.Count(string(calls), "github-identity\n") != 1 {
		t.Fatalf("expected one offline GitHub identity freeze: %s %v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(state, "auth-installation.json")); !os.IsNotExist(err) {
		t.Fatalf("test initialized provider authentication storage: %v", err)
	}
}

func TestRunCommandRegistersProviderQueueBeforeCheckingProviderAuthentication(t *testing.T) {
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
