package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func TestStandaloneVerificationUsesSelectedNamedRuntime(t *testing.T) {
	for _, command := range []string{"provider", "github-auth", "github-status", "signing"} {
		t.Run(command, func(t *testing.T) {
			directory, profile := pairingFixture(t)
			t.Setenv("SDLC_STATE_DIR", directory)
			if err := runtimeimage.WriteDefaultName(directory, "test-selected"); err != nil {
				t.Fatal(err)
			}
			state := runtimeimage.State{Version: 1, Name: "test-selected", Engine: "test-engine", ImageID: "sha256:" + strings.Repeat("a", 64)}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "runtime.test-selected.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(directory, "runtime.json")); !os.IsNotExist(err) {
				t.Fatal("fixture must not contain the shared runtime record")
			}
			pair := githubprofile.Pair{Version: 1, GitHubProfile: "personal", AccountID: 123, Login: "example-user", SigningProfile: "personal-key", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
			if err := githubprofile.Store(directory, pair, false); err != nil {
				t.Fatal(err)
			}
			fakeBin := filepath.Join(directory, "fake-bin")
			if err := os.Mkdir(fakeBin, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(directory, "selected-runtime-inspected")
			script := "#!/bin/sh\ncase \"$1\" in\n  info) echo '{\"id\":\"test-engine\",\"os\":\"linux\"}' ;;\n  image) [ \"$2\" = inspect ] && [ \"$5\" = sdlc:test-selected ] || exit 90; : > \"$TEST_SELECTED_MARKER\"; echo '" + state.ImageID + "' ;;\n  *) exit 91 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(fakeBin, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_HOST", "unix:///test.sock")
			t.Setenv("DOCKER_CONTEXT", "")
			t.Setenv("TEST_SELECTED_MARKER", marker)

			var output bytes.Buffer
			var commandErr error
			var expected string
			switch command {
			case "provider":
				commandErr = auth(context.Background(), []string{"status", "--provider", "codex"})
				expected = "one or more providers need an account login"
			case "github-auth":
				commandErr = auth(context.Background(), []string{"status", "--service", "github", "--profile", "personal", "--verify"})
				expected = "GitHub login is missing"
			case "github-status":
				commandErr = githubCommand(context.Background(), []string{"status", "--profile", "personal", "--verify"}, &output)
				expected = "GitHub login is missing"
			case "signing":
				commandErr = signingCommand(context.Background(), []string{"verify", "--profile", "missing"}, &output)
				expected = "signing setup is incomplete"
			}
			if commandErr == nil || !strings.Contains(commandErr.Error(), expected) {
				t.Fatalf("%s: want %q, got %v; output %s", command, expected, commandErr, output.String())
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("%s did not inspect the selected named runtime: %v; output %s", command, err, output.String())
			}
		})
	}
}

func TestSelectedRuntimePreservesLocalFallbackAndRejectsInvalidSelection(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manager := runtimeimage.Manager{Directory: directory}
	got, err := selectedRuntime(manager)
	if err != nil || got.Name != "" || got.Directory != directory {
		t.Fatalf("local default: %+v, %v", got, err)
	}
	if _, err := got.Status(context.Background()); err == nil || strings.Contains(strings.ToLower(err.Error()), "rebuild") || strings.Contains(err.Error(), "runtime build") {
		t.Fatalf("missing runtime diagnosis should not require a rebuild: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "runtime-default.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := selectedRuntime(manager); err == nil {
		t.Fatal("invalid runtime selection accepted")
	}
	// Local account listings remain available without selecting a runtime.
	t.Setenv("SDLC_STATE_DIR", directory)
	var output bytes.Buffer
	if err := githubCommand(context.Background(), []string{"list"}, &output); err != nil {
		t.Fatalf("offline list: %v", err)
	}
}
