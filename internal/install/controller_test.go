package install

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/controllerasset"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIOnlyInstallDeliversBothStaticControllersAndReusesVerifiedBundle(t *testing.T) {
	source, bin := fixture(t)
	writeCommand(t, source, `"sdlc test-beta (unknown; test/test)"`)
	path, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := controllerasset.Hash(path)
	directory := controllerasset.Directory(path, hash)
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest controllerasset.Manifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.CLIHash != hash || manifest.Version != "test-beta" {
		t.Fatal("controller pairing manifest incorrect")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		helper := filepath.Join(directory, "sdlc-publisher-linux-"+arch)
		metadata, err := buildinfo.ReadFile(helper)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]string{}
		for _, setting := range metadata.Settings {
			settings[setting.Key] = setting.Value
		}
		if settings["GOOS"] != "linux" || settings["GOARCH"] != arch || settings["CGO_ENABLED"] != "0" {
			t.Fatal("controller not static Linux selected arch")
		}
		info, _ := os.Lstat(helper)
		if info.Mode().Perm() != 0555 {
			t.Fatal("helper not read-only executable")
		}
	}
	if _, err = Build(context.Background(), source, bin, io.Discard, io.Discard); err != nil {
		t.Fatal("deterministic reinstall failed", err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if string(after) != string(manifestBytes) {
		t.Fatal("reinstall changed existing bundle")
	}
	before, _ := os.ReadFile(path)
	writeCommand(t, source, `"sdlc next-beta (unknown; test/test)"`)
	os.RemoveAll(filepath.Join(source, "cmd", "sdlc-publisher"))
	if _, err = Build(context.Background(), source, bin, io.Discard, io.Discard); err == nil {
		t.Fatal("missing helper source installed CLI")
	}
	installed, _ := os.ReadFile(path)
	if string(installed) != string(before) {
		t.Fatal("helper build failure replaced CLI")
	}
}
