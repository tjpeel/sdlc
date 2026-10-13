package controllerasset

import (
	"context"
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestControllerAssetsRejectUntrustedOrMismatchedBytes(t *testing.T) {
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "go.mod"), []byte("module github.com/tjpeel/sdlc\n\ngo 1.25.0\n"), 0600)
	helperDir := filepath.Join(source, "cmd", "sdlc-publisher")
	os.MkdirAll(helperDir, 0700)
	os.WriteFile(filepath.Join(helperDir, "main.go"), []byte("package main\nfunc main(){}\n"), 0600)
	bin := t.TempDir()
	cli := filepath.Join(bin, "sdlc")
	os.WriteFile(cli, []byte("disposable trusted CLI fixture"), 0555)
	cliHash, _ := Hash(cli)
	directory := Directory(cli, cliHash)
	os.Mkdir(directory, 0700)
	t.Cleanup(func() { os.Chmod(directory, 0700) })
	identity := buildinfo.Identity{Version: "test-version", Revision: "test-revision"}
	manifest := Manifest{Version: identity.Version, Revision: identity.Revision, CLIHash: cliHash, Helpers: map[string]string{}}
	for _, arch := range []string{"amd64", "arm64"} {
		path := filepath.Join(directory, "sdlc-publisher-linux-"+arch)
		command := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-X github.com/tjpeel/sdlc/internal/buildinfo.Revision="+identity.Revision, "-o", path, "./cmd/sdlc-publisher")
		command.Dir = source
		command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0", "GOPROXY=off")
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(data))
		}
		os.Chmod(path, 0555)
		manifest.Helpers[arch], _ = Hash(path)
	}
	write := func(m Manifest) {
		os.Chmod(directory, 0700)
		path := filepath.Join(directory, "manifest.json")
		os.Chmod(path, 0600)
		data, _ := json.Marshal(m)
		if err := os.WriteFile(path, data, 0444); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, 0444)
		os.Chmod(directory, 0555)
	}
	write(manifest)
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := resolve(cli, identity, "linux/"+arch); err != nil {
			t.Fatal("validated helper rejected", err)
		}
	}
	if _, err := resolve(cli, identity, "linux/386"); err == nil {
		t.Fatal("unsupported arch accepted")
	}
	wrong := manifest
	wrong.Version = "stale"
	write(wrong)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("stale version accepted")
	}
	write(manifest)
	wrong = manifest
	wrong.Revision = "stale-revision"
	write(wrong)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("stale revision accepted")
	}
	write(manifest)
	path := filepath.Join(directory, "sdlc-publisher-linux-amd64")
	os.Chmod(path, 0755)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("writable helper accepted")
	}
	os.Chmod(path, 0500)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("helper unreadable by container UID accepted")
	}
	os.Chmod(path, 0755)
	os.WriteFile(path, []byte("changed helper"), 0755)
	os.Chmod(path, 0555)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("changed helper accepted")
	}
	os.Chmod(directory, 0700)
	os.Remove(path)
	if _, err := resolve(cli, identity, "linux/amd64"); err == nil {
		t.Fatal("missing helper accepted")
	}
}
