package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Profile, string) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), public...)
	hash := sha256.Sum256(wire)
	bootstrap := filepath.Join(directory, "bootstrap")
	if err := os.WriteFile(bootstrap, []byte("fake-service-account-input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := Profile{Version: 1, ID: "test-signing", Reference: "op://YOUR_VAULT/YOUR_SIGNING_KEY/private key?ssh-format=openssh", PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:]), BootstrapFile: bootstrap}
	path := filepath.Join(directory, "profiles.local.json")
	data, _ := json.Marshal(profile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return profile, path
}

func TestPrivateProfileValidation(t *testing.T) {
	profile, path := fixture(t)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("public profile accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(filepath.Dir(path), "linked")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink profile accepted")
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("source checkout profile accepted")
	}
	if err := profile.Validate(); err == nil {
		t.Fatal("source checkout bootstrap accepted")
	}
}

func TestProfileRejectsMismatchedFingerprintAndReference(t *testing.T) {
	for _, kind := range []string{"fingerprint", "reference", "key", "unknown field", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			profile, path := fixture(t)
			switch kind {
			case "fingerprint":
				profile.Fingerprint = "SHA256:" + strings.Repeat("A", 43)
			case "reference":
				profile.Reference = "\"" + profile.Reference + "\""
			case "key":
				profile.PublicKey = "ssh-ed25519 AAAA"
			}
			data, _ := json.Marshal(profile)
			if kind == "unknown field" {
				data = append(data[:len(data)-1], []byte(`,"token":"fake-should-not-be-configured"}`)...)
			}
			if kind == "trailing" {
				data = append(data, []byte(`{}`)...)
			}
			os.WriteFile(path, data, 0600)
			if _, err := Load(path); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestResolverKeepsBearerSecretOffArgumentsAndConfigEnvironment(t *testing.T) {
	profile, _ := fixture(t)
	calls := 0
	resolver := Resolver{Profile: profile, Run: func(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
		calls++
		data, _ := io.ReadAll(input)
		if !bytes.Equal(data, []byte("fake-service-account-input\n"+profile.Reference+"\n")) {
			t.Fatal("bootstrap not supplied through stdin")
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "fake-service-account-input") || strings.Contains(joined, profile.Reference) || strings.Contains(joined, "--mount") || strings.Contains(joined, "--env OP_SERVICE_ACCOUNT_TOKEN") {
			t.Fatal("credential channel leaked or mounted host files")
		}
		for _, required := range []string{"--read-only", "--user 1000:1000", "--cap-drop ALL", "--log-driver none", "--pull never", "--rm", "--entrypoint /usr/bin/timeout", "--kill-after=5s 35s", "io.sdlc.kind=signing", Image} {
			if !strings.Contains(joined, required) {
				t.Fatalf("missing %s", required)
			}
		}
		_, err := output.Write([]byte("fake-signing-key"))
		return err
	}}
	key, err := resolver.Resolve(context.Background())
	if err != nil || string(key) != "fake-signing-key" || calls != 1 {
		t.Fatal("resolver did not use the bounded private channel")
	}
	clear(key)
	os.Chmod(profile.BootstrapFile, 0644)
	if _, err := resolver.Resolve(context.Background()); err == nil || calls != 1 {
		t.Fatal("public bootstrap reached Docker")
	}
}

func TestResolverSuppressesSecretDiagnosticsAndBoundsOutput(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		profile, _ := fixture(t)
		resolver := Resolver{Profile: profile, Run: func(_ context.Context, _ io.Reader, out io.Writer, _ ...string) error {
			if oversize {
				out.Write(bytes.Repeat([]byte("x"), 65537))
				return nil
			}
			return fmt.Errorf("fake-service-account-input")
		}}
		if key, err := resolver.Resolve(context.Background()); key != nil || err == nil || strings.Contains(err.Error(), "fake-service-account-input") {
			t.Fatal("unbounded key or secret diagnostic returned")
		}
	}
}

func TestStoreKeepsConfigurationPrivateAndOutsideRepositories(t *testing.T) {
	profile, path := fixture(t)
	directory := filepath.Join(filepath.Dir(path), "state")
	if err := Store(directory, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(filepath.Join(directory, "profiles.local.json"))
	if err != nil || loaded != profile {
		t.Fatalf("stored profile differs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(directory, "profiles.local.json"))
	if strings.Contains(string(data), "fake-service-account-input") {
		t.Fatal("bootstrap contents stored")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := Store(directory, profile); err == nil {
		t.Fatal("public state directory accepted")
	}
	checkout := filepath.Join(filepath.Dir(path), "checkout")
	os.MkdirAll(filepath.Join(checkout, ".git"), 0700)
	if err := Store(filepath.Join(checkout, "state"), profile); err == nil {
		t.Fatal("state inside checkout accepted")
	}
	if _, err := os.Lstat(filepath.Join(checkout, "state")); !os.IsNotExist(err) {
		t.Fatal("unsafe state written before rejection")
	}
}

func TestNamedSigningProfilesStaySeparate(t *testing.T) {
	profile, path := fixture(t)
	directory := filepath.Join(filepath.Dir(path), "state")
	if err := Store(directory, profile, "personal"); err != nil {
		t.Fatal(err)
	}
	work := profile
	work.ID = "work-signing"
	if err := Store(directory, work, "work"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Profile{"personal": profile, "work": work} {
		file, err := ProfilePath(directory, name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Load(file)
		if err != nil || got != want {
			t.Fatalf("profile %s crossed: %v", name, err)
		}
	}
	for _, name := range []string{"../work", "Work", "work/name"} {
		if err := Store(directory, profile, name); err == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
	file, _ := ProfilePath(directory, "default")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("named configuration replaced default")
	}
}
