package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/signing"
)

type setupPrompt struct {
	answers  []string
	calls    int
	secrets  int
	closed   bool
	fail     int
	onSecret func()
}

func (prompt *setupPrompt) Read(_ string, secret bool) ([]byte, error) {
	prompt.calls++
	if secret {
		prompt.secrets++
		if prompt.onSecret != nil {
			prompt.onSecret()
		}
	}
	if prompt.calls == prompt.fail {
		return nil, errors.New("disposable prompt ended")
	}
	if len(prompt.answers) == 0 {
		return nil, fmt.Errorf("unexpected prompt")
	}
	answer := prompt.answers[0]
	prompt.answers = prompt.answers[1:]
	return []byte(answer), nil
}
func (prompt *setupPrompt) Close() error { prompt.closed = true; return nil }

func signingTestState(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), public...)
	return filepath.Join(root, "state"), "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire)
}

func runSetupFixture(ctx context.Context, directory string, options signingOptions, prompt *setupPrompt, output *bytes.Buffer) error {
	return signingSetup(ctx, directory, options, output, func(context.Context) (signingPrompter, error) { return prompt, nil })
}

func TestSigningSetupKeepsTokenOutOfMetadataAndStatus(t *testing.T) {
	directory, public := signingTestState(t)
	fake := "fake-service-account-bootstrap-only"
	prompt := &setupPrompt{answers: []string{"YOUR_VAULT_ID", "YOUR_ITEM_ID", public + " comment@example.invalid", "", "yes", fake}}
	var output bytes.Buffer
	if err := runSetupFixture(context.Background(), directory, signingOptions{name: "personal"}, prompt, &output); err != nil {
		t.Fatal(err, output.String())
	}
	path, _ := signing.ProfilePath(directory, "personal")
	profile, err := signing.Load(path)
	if err != nil || profile.Provider != "1password" || profile.PublicKey != public || profile.Reference != "op://YOUR_VAULT_ID/YOUR_ITEM_ID/private key?ssh-format=openssh" {
		t.Fatal("incorrect profile", err)
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte(fake)) || strings.Contains(output.String(), fake) || prompt.secrets != 1 || !prompt.closed {
		t.Fatal("token was exposed or prompt was not hidden/closed")
	}
	bootstrap, _ := os.ReadFile(profile.BootstrapFile)
	if string(bootstrap) != fake+"\n" {
		t.Fatal("bootstrap not saved separately")
	}
	output.Reset()
	if err := signingStatus(directory, "personal", false, &output); err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{fake, "YOUR_VAULT_ID", "YOUR_ITEM_ID", profile.BootstrapFile, path} {
		if strings.Contains(output.String(), hidden) {
			t.Fatal("ordinary status disclosed private locator metadata")
		}
	}
	if !strings.Contains(output.String(), "not checked by local status") {
		t.Fatal("offline status claimed verification")
	}
	output.Reset()
	if err := signingStatus(directory, "personal", true, &output); err != nil || !strings.Contains(output.String(), profile.Reference) || !strings.Contains(output.String(), profile.BootstrapFile) || strings.Contains(output.String(), fake) {
		t.Fatal("explicit config inspection omitted locators or disclosed token", err)
	}
}

func TestSigningSetupNeverOverwritesOrReadsNewTokenForExistingProfile(t *testing.T) {
	directory, public := signingTestState(t)
	prompt := &setupPrompt{answers: []string{"YOUR_VAULT", "YOUR_KEY", public, "", "y", "fake-bootstrap"}}
	if err := runSetupFixture(context.Background(), directory, signingOptions{name: "personal"}, prompt, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path, _ := signing.ProfilePath(directory, "personal")
	before, _ := os.ReadFile(path)
	opened := false
	err := signingSetup(context.Background(), directory, signingOptions{name: "personal"}, &bytes.Buffer{}, func(context.Context) (signingPrompter, error) {
		opened = true
		return &setupPrompt{}, nil
	})
	after, _ := os.ReadFile(path)
	if err == nil || opened || !bytes.Equal(before, after) {
		t.Fatal("existing profile overwritten or another token requested", err)
	}
}

func TestSigningSetupCancellationAndInvalidIdentitySaveNothing(t *testing.T) {
	for _, kind := range []string{"decline", "bad public key", "bad fingerprint", "prompt ended", "context cancelled", "invalid vault"} {
		t.Run(kind, func(t *testing.T) {
			directory, public := signingTestState(t)
			prompt := &setupPrompt{answers: []string{"YOUR_VAULT", "YOUR_KEY", public, "", "y", "fake-bootstrap"}}
			ctx := context.Background()
			switch kind {
			case "decline":
				prompt.answers[4] = "no"
			case "bad public key":
				prompt.answers[2] = "fake-private-key-do-not-echo"
			case "bad fingerprint":
				prompt.answers[3] = "SHA256:" + strings.Repeat("A", 43)
			case "prompt ended":
				prompt.fail = 6
			case "context cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "invalid vault":
				prompt.answers[0] = "outside/vault"
			}
			var output bytes.Buffer
			if err := runSetupFixture(ctx, directory, signingOptions{name: "personal"}, prompt, &output); err == nil {
				t.Fatal("invalid or cancelled setup succeeded")
			}
			entries, err := os.ReadDir(directory)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "signing-setup-personal.lock" {
					t.Fatal("cancelled setup left a profile or token", entry.Name())
				}
			}
			if strings.Contains(output.String(), "fake-private-key") || strings.Contains(output.String(), "fake-bootstrap") {
				t.Fatal("setup disclosed input")
			}
		})
	}
}

func TestSigningSetupReusesOnlyExplicitSafeBootstrap(t *testing.T) {
	directory, public := signingTestState(t)
	bootstrap := filepath.Join(filepath.Dir(directory), "existing-bootstrap")
	if err := os.WriteFile(bootstrap, []byte("fake-existing-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt := &setupPrompt{answers: []string{"YOUR_VAULT", "YOUR_KEY", public, "", "y"}}
	if err := runSetupFixture(context.Background(), directory, signingOptions{name: "work", bootstrap: bootstrap}, prompt, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path, _ := signing.ProfilePath(directory, "work")
	profile, _ := signing.Load(path)
	if profile.BootstrapFile != bootstrap || prompt.secrets != 0 {
		t.Fatal("existing bootstrap copied or another token requested")
	}
	before, _ := os.ReadFile(bootstrap)
	os.Chmod(bootstrap, 0644)
	if err := signingStatus(directory, "work", false, &bytes.Buffer{}); err == nil {
		t.Fatal("unsafe bootstrap accepted by status")
	}
	after, _ := os.ReadFile(bootstrap)
	if !bytes.Equal(before, after) {
		t.Fatal("bootstrap altered by read-only check")
	}
}

func TestSigningSetupRetainsCreatedBootstrapWhenProfileSaveFails(t *testing.T) {
	directory, public := signingTestState(t)
	bootstrap, err := signing.BootstrapPath(directory, "personal")
	if err != nil {
		t.Fatal(err)
	}
	prompt := &setupPrompt{answers: []string{"YOUR_VAULT", "YOUR_KEY", public, "", "y", "fake-retained-bootstrap"}}
	prompt.onSecret = func() {
		// Force a profile collision after the leased preflight. An independent
		// profile already names the bootstrap, as --bootstrap-file permits.
		work, err := signing.NewProfile("work", "YOUR_VAULT", "YOUR_KEY", public, "", bootstrap)
		if err != nil || signing.StoreNew(directory, work, "work") != nil {
			t.Fatal("cannot prepare independent profile", err)
		}
		path, _ := signing.ProfilePath(directory, "personal")
		if err := os.WriteFile(path, []byte("disposable competing profile"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	err = runSetupFixture(context.Background(), directory, signingOptions{name: "personal"}, prompt, &output)
	if err == nil || !strings.Contains(err.Error(), "bootstrap remains") || !strings.Contains(err.Error(), "--bootstrap-file") || strings.Contains(err.Error(), "fake-retained-bootstrap") {
		t.Fatal("partial setup did not report safe recovery", err)
	}
	if err := signingStatus(directory, "work", false, &bytes.Buffer{}); err != nil {
		t.Fatal("failed setup deleted another profile's bootstrap", err)
	}
	contents, _ := os.ReadFile(bootstrap)
	if string(contents) != "fake-retained-bootstrap\n" || strings.Contains(output.String(), "fake-retained-bootstrap") {
		t.Fatal("bootstrap changed or was printed")
	}
}

func TestSigningMissingStatusAndArgumentBoundariesNeverConnect(t *testing.T) {
	directory, _ := signingTestState(t)
	t.Setenv("SDLC_STATE_DIR", directory)
	for _, args := range [][]string{{}, {"--help"}, {"setup", "--help"}, {"status", "--help"}, {"verify", "--help"}, {"configure", "--help"}} {
		var output bytes.Buffer
		if err := signingCommand(context.Background(), args, &output); err != nil || !strings.Contains(output.String(), "Usage:") {
			t.Fatal("help tried to use credentials", args, err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"setup", "--file", "anything"}, {"setup", "--provider=unimplemented"}, {"setup", "--provider="}, {"status", "--provider=1password"}, {"status", "--bootstrap-file", "anything"}, {"verify", "--show-config=false"}, {"configure", "--verify=false"}, {"configure"}, {"setup", "--profile="}, {"status", "--profile=../other"}, {"status", "extra"}} {
		if err := signingCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid signing arguments accepted", args)
		}
	}
	var output bytes.Buffer
	if err := signingCommand(context.Background(), []string{"status", "--profile", "personal"}, &output); err == nil || !strings.Contains(output.String(), "sdlc signing setup --profile personal") {
		t.Fatal("missing status did not guide setup", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("help or status created installation state")
	}
}
