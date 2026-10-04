package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyVersionOneProfileDefaultsToOnePassword(t *testing.T) {
	profile, path := fixture(t)
	if profile.Provider != "" {
		t.Fatal("legacy fixture unexpectedly selects provider")
	}
	loaded, err := Load(path)
	if err != nil || loaded.Provider != "" || loaded.EffectiveProvider() != DefaultProvider {
		t.Fatal("legacy profile lost default provider", err)
	}
	if err := ValidateProvider(""); err != nil {
		t.Fatal("omitted provider rejected", err)
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"provider"`)) {
		t.Fatal("legacy omitted provider JSON changed")
	}
}

func TestNewProfileStoresExplicitOnePasswordProvider(t *testing.T) {
	public, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(public.BootstrapFile), "provider-state")
	profile, err := NewProfile("work", "YOUR_VAULT", "YOUR_ITEM", public.PublicKey, public.Fingerprint, public.BootstrapFile)
	if err != nil || profile.Provider != DefaultProvider || profile.EffectiveProvider() != DefaultProvider {
		t.Fatal("new profile lacks explicit default", err)
	}
	if err := StoreNew(directory, profile, "work"); err != nil {
		t.Fatal(err)
	}
	path, _ := ProfilePath(directory, "work")
	loaded, err := Load(path)
	if err != nil || loaded != profile || loaded.Version != 1 {
		t.Fatal("explicit provider failed version1 roundtrip", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(`"provider": "1password"`)) {
		t.Fatal("provider not persisted", err)
	}
}

func TestUnsupportedSecretProviderStopsBeforeCredentialReadAndCommand(t *testing.T) {
	for _, provider := range []string{"private-provider-marker", "1Password", " 1password", "1password ", "aws", "\n"} {
		t.Run("unsupported", func(t *testing.T) {
			profile, _ := fixture(t)
			profile.Provider = provider
			// Missing bootstrap would yield a different error if loading happened.
			profile.BootstrapFile = filepath.Join(filepath.Dir(profile.BootstrapFile), "missing-bootstrap")
			expected := ValidateProvider(provider)
			if expected == nil || strings.Contains(expected.Error(), "private-provider-marker") {
				t.Fatal("unsupported provider accepted or exposed")
			}
			called := false
			resolver := Resolver{Profile: profile, Run: func(context.Context, io.Reader, io.Writer, ...string) error { called = true; return nil }}
			if _, err := resolver.Resolve(context.Background()); err == nil || err.Error() != expected.Error() || called {
				t.Fatal("unsupported provider reached credential loading or Docker", err)
			}
			if err := CheckBootstrap(profile); err == nil || err.Error() != expected.Error() {
				t.Fatal("offline bootstrap check read unsupported provider input", err)
			}
			// Provider validation also precedes reference and Git-boundary validation.
			profile.Reference = "invalid-reference"
			if err := profile.Validate(); err == nil || err.Error() != expected.Error() {
				t.Fatal("provider preflight ordering changed", err)
			}
		})
	}
}
