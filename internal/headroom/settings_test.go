package headroom

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalSettingsPersistenceAndWorkerRoute(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if _, err := Load(directory); err == nil || !strings.Contains(err.Error(), "set --proxy-url") {
		t.Fatal("missing address was not actionable", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("load created settings")
	}
	saved, err := Save(directory, "http://127.0.0.1:8787/")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil || saved != loaded || loaded.ProxyURL != "http://127.0.0.1:8787" || loaded.WorkerURL() != "http://host.docker.internal:8787" {
		t.Fatal(loaded, err)
	}
	info, err := os.Stat(filepath.Join(directory, "headroom.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings must be private", err)
	}
	docker := &fakeDocker{}
	instance, err := Start(context.Background(), docker, loaded, "unused")
	if err != nil || instance != nil || len(docker.calls) != 0 {
		t.Fatal("external proxy lifecycle was managed", err, docker.calls)
	}
	for _, address := range []string{"https://example.invalid:9443", "http://localhost:8787", "http://[::1]:8787"} {
		c, err := Save(directory, address)
		if err != nil || c.Validate() != nil {
			t.Fatal(address, err)
		}
		if address == "https://example.invalid:9443" && c.WorkerURL() != address {
			t.Fatal("non-loopback URL changed")
		}
	}
}

func TestExternalProxyRejectsAmbiguousOrCredentialURLs(t *testing.T) {
	directory := t.TempDir()
	original, err := Save(directory, "http://localhost:8787")
	if err != nil {
		t.Fatal(err)
	}
	credentialURL := (&url.URL{Scheme: "http", Host: "example.invalid", User: url.UserPassword("EXAMPLE_USER", "EXAMPLE_PASSWORD")}).String()
	for _, address := range []string{"", "localhost:8787", "ftp://localhost:8787", "http://example.invalid/dashboard", "http://example.invalid/v1", credentialURL, "http://example.invalid?token=example", "http://example.invalid#fragment", "http://example.invalid:0", "http://example.invalid:65536", "http://example.invalid:", "http://example.invalid/?", "http://example.invalid/\n"} {
		if _, err := Save(directory, address); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	if loaded, err := Load(directory); err != nil || loaded != original {
		t.Fatal("invalid update changed saved address", loaded, err)
	}
	for _, c := range []Config{{Mode: "external"}, {Mode: "external", ProxyURL: original.ProxyURL, ImageID: digest}, {Mode: "external", ProxyURL: original.ProxyURL, PolicyVersion: PolicyVersion}, {ProxyURL: original.ProxyURL}, {Mode: "optimize", ProxyURL: original.ProxyURL, ImageID: digest, PolicyVersion: PolicyVersion}} {
		if c.Validate() == nil {
			t.Fatal("ambiguous route accepted", c)
		}
	}
}
