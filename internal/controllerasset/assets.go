// Package controllerasset delivers current trusted controllers independently of worker images.
package controllerasset

import (
	"crypto/sha256"
	gobuildinfo "debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/tjpeel/sdlc/internal/buildinfo"
	"io"
	"os"
	"path/filepath"
)

type Manifest struct {
	Version  string            `json:"version"`
	Revision string            `json:"revision"`
	Dirty    bool              `json:"dirty"`
	CLIHash  string            `json:"cli_sha256"`
	Helpers  map[string]string `json:"helpers"`
}

func Hash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 128*1024*1024 {
		return "", fmt.Errorf("invalid controller asset")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("controller asset changed")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(f, 128*1024*1024+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func Directory(cli, hash string) string {
	return filepath.Join(filepath.Dir(cli), ".sdlc-controller-"+hash)
}

// Pin executable provenance while the process starts. Atomic CLI upgrades retain
// old content-addressed bundles for controllers that are already running.
var runningCLI = func() struct {
	path, hash string
	err        error
} {
	var result struct {
		path, hash string
		err        error
	}
	result.path, result.err = os.Executable()
	if result.err != nil {
		return result
	}
	result.path, result.err = filepath.EvalSymlinks(result.path)
	if result.err != nil {
		return result
	}
	metadata, err := gobuildinfo.ReadFile(result.path)
	if err != nil || metadata.Path != "github.com/tjpeel/sdlc/cmd/sdlc" {
		result.err = fmt.Errorf("publisher requires managed installed CLI")
		return result
	}
	result.hash, result.err = Hash(result.path)
	return result
}()

// Resolve accepts only immutable assets paired to the running managed CLI bytes.
func Resolve(platform string) (string, error) {
	if runningCLI.err != nil {
		return "", runningCLI.err
	}
	return resolveHash(runningCLI.path, runningCLI.hash, buildinfo.Current(), platform)
}
func resolve(cli string, identity buildinfo.Identity, platform string) (string, error) {
	hash, err := Hash(cli)
	if err != nil {
		return "", err
	}
	return resolveHash(cli, hash, identity, platform)
}
func resolveHash(cli, hash string, identity buildinfo.Identity, platform string) (string, error) {
	var err error
	directory := Directory(cli, hash)
	if err = privateImmutable(directory, true); err != nil {
		return "", err
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	if info, err := os.Lstat(manifestPath); err != nil || info.Size() > 4096 {
		return "", fmt.Errorf("invalid controller manifest bound")
	}
	if err = privateImmutable(manifestPath, false); err != nil {
		return "", err
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var m Manifest
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", fmt.Errorf("invalid controller manifest")
	}

	if m.CLIHash != hash || m.Version != identity.Version || m.Revision != identity.Revision || m.Dirty != identity.Dirty {
		return "", fmt.Errorf("controller provenance differs from installed CLI")
	}
	arch := ""
	switch platform {
	case "linux/amd64":
		arch = "amd64"
	case "linux/arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported publisher platform")
	}
	path := filepath.Join(directory, "sdlc-publisher-linux-"+arch)
	if info, err := os.Lstat(path); err != nil || info.Mode().Perm() != 0555 {
		return "", fmt.Errorf("publisher controller must have mode 0555")
	}
	if err = privateImmutable(path, false); err != nil {
		return "", err
	}
	actual, err := Hash(path)
	if err != nil || actual != m.Helpers[arch] {
		return "", fmt.Errorf("publisher controller hash mismatch")
	}
	metadata, err := gobuildinfo.ReadFile(path)
	if err != nil || metadata.Path != "github.com/tjpeel/sdlc/cmd/sdlc-publisher" {
		return "", fmt.Errorf("invalid publisher controller build")
	}
	settings := map[string]string{}
	for _, setting := range metadata.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != "linux" || settings["GOARCH"] != arch || settings["CGO_ENABLED"] != "0" || (settings["vcs.revision"] != "" && settings["vcs.revision"] != m.Revision) || (settings["vcs.modified"] == "true") != m.Dirty {
		return "", fmt.Errorf("publisher controller build provenance mismatch")
	}
	return path, nil
}

// Stage snapshots verified bytes into a private controller-owned immutable directory.
// The staged file is revalidated before any credentials are acquired.
func Stage(platform string) (string, func() error, error) {
	path, err := Resolve(platform)
	if err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp("", "sdlc-trusted-controller-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() error {
		if err := os.Chmod(directory, 0700); err != nil {
			return err
		}
		return os.RemoveAll(directory)
	}
	file, err := os.Open(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer file.Close()
	staged := filepath.Join(directory, "sdlc-publisher")
	output, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, copyErr := io.Copy(output, io.LimitReader(file, 128*1024*1024+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("cannot stage publisher controller")
	}
	verified, err := Resolve(platform)
	if err != nil || verified != path {
		cleanup()
		return "", nil, fmt.Errorf("publisher controller changed during staging")
	}
	sourceHash, err := Hash(verified)
	stagedHash, stagedErr := Hash(staged)
	if err != nil || stagedErr != nil || sourceHash != stagedHash {
		cleanup()
		return "", nil, fmt.Errorf("publisher controller changed during staging")
	}
	if err = os.Chmod(directory, 0555); err != nil {
		cleanup()
		return "", nil, err
	}
	return staged, cleanup, nil
}

// VerifyInstallation checks a prepared asset bundle against a candidate's identity.
// Runtime selection remains tied to os.Executable through Resolve and Stage.
func VerifyInstallation(cli string, identity buildinfo.Identity, platform string) error {
	_, err := resolve(cli, identity, platform)
	return err
}
