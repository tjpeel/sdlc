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
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
)

// Runtime identity and GitHub identity freezing use offline fake Docker responses.
// Provider authentication, signing-key retrieval and publication remain forbidden.
func queuedRunFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := runGitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", state)
	configureTestHeadroom(t, state)
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
	pair := githubprofile.Pair{Version: 1, GitHubProfile: "default", AccountID: 1, Login: "testuser", SigningProfile: "default", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
	if err := githubprofile.Store(state, pair, false); err != nil {
		t.Fatal(err)
	}
	if err := githubprofile.SaveSelection(state, root, "example/project", pair); err != nil {
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
    if [ "$previous" = signing-keys ] && [ "$last" = testuser ]; then
      printf '%%s\n' '["%s"]'; exit 0
    fi
    case "$last" in
      status) printf '%%s\n' '{"state":"stored"}' ;;
      identity) printf 'github-identity\n' >> "$(dirname "$0")/metadata-calls"; printf '%%s\n' '{"id":1,"login":"testuser"}' ;;
      *) printf forbidden > "$(dirname "$0")/called"; exit 99 ;;
    esac ;;
  *) printf forbidden > "$(dirname "$0")/called"; exit 99 ;;
esac
`, image, volume, volume, string(metadata), volume, profile.PublicKey)
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
	return state, marker, filepath.Join(state, "runtime-build.lock")
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
		if len(views) == 1 && views[0].Live && views[0].Activity.WaitReason == "runtime_busy" {
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

func TestRunCommandRuntimeBuildContentionCancelsBeforeProviderAuthentication(t *testing.T) {
	state, marker, runtimePath := queuedRunFixture(t)
	held, err := filelock.Acquire(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	err = runCommand(ctx, runArgs("--repo", "example/project"), &output)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runtime queue cancellation: %v", err)
	}
	views, err := runstatus.New(state).List(time.Now().UTC())
	if err != nil || len(views) != 0 {
		t.Fatalf("runtime contention launched a run: %+v %v", views, err)
	}
	if data, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Fatalf("runtime contention made connected call: %s %v", data, err)
	}
	held.Close()
	released, err := filelock.Acquire(runtimePath)
	if err != nil {
		t.Fatal("runtime queue leaked lock", err)
	}
	released.Close()
}
