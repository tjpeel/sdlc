//go:build !windows

package workrun

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func TestPublisherBundleReadableWithPrivateUmask(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.bundle")
	content := []byte("disposable public source bundle")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	destination := filepath.Join(directory, "staged.bundle")
	if err := stagePublisherBundle(source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("staged bundle is unreadable by the isolated publisher UID")
	}
	staged, err := os.ReadFile(destination)
	if err != nil || string(staged) != string(content) {
		t.Fatal("staging changed the exported source")
	}
}

// Use an existing frozen image to exercise its strict decoder and non-root
// bundle access without account state, a signing key or network access.
func TestPublisherFrozenImageRequestDocker(t *testing.T) {
	image := os.Getenv("SDLC_PUBLISHER_TEST_IMAGE")
	if os.Getenv("SDLC_DOCKER_TESTS") != "1" || image == "" {
		t.Skip("set SDLC_DOCKER_TESTS=1 and SDLC_PUBLISHER_TEST_IMAGE to a frozen runtime image ID")
	}
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image) {
		t.Fatal("test requires an immutable image ID")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(directory, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0777); err != nil {
		t.Fatal(err)
	}
	source, bundle := filepath.Join(directory, "source"), filepath.Join(directory, "bundle")
	if err := os.WriteFile(source, []byte("disposable-public-source"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0077)
	err = stagePublisherBundle(source, bundle)
	syscall.Umask(previous)
	if err != nil {
		t.Fatal(err)
	}
	// Substitute only key inspection. Reaching this shim proves strict request
	// decoding passed; reading the bundle proves UID 1000 can access staging.
	shim := filepath.Join(directory, "ssh-keygen")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\ncat /source.bundle > /publisher/bundle-read || exit 2\nprintf reached > /publisher/decoded\nexit 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shim, 0755); err != nil {
		t.Fatal(err)
	}
	request := publisherRequestForContainer(PublisherRequest{Action: "publish", Plan: Plan{PublicationIdentity: frozenTestIdentity(), SigningImage: "controller-pin", DaemonImage: "controller-pin"}})
	requestPath := filepath.Join(directory, "request.json")
	if err := saveJSON(requestPath, request); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(requestPath, 0644); err != nil {
		t.Fatal(err)
	}
	args := checkContainerEnvironment([]string{"run", "--rm", "--interactive", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=32m,mode=1777"})
	for _, pair := range [][2]string{{requestPath, "/request.json"}, {bundle, "/source.bundle"}, {shim, "/usr/bin/ssh-keygen"}} {
		args = append(args, "--mount", mount(pair[0], pair[1], true))
	}
	args = append(args, "--mount", mount(state, "/publisher", false), "--entrypoint", "/usr/local/bin/sdlc-publisher", image)
	command := exec.Command("docker", args...)
	command.Stdin = strings.NewReader("disposable-fake-key")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal("credential-free frozen publisher test could not start")
	}
	var response PublisherResponse
	if json.Unmarshal(output, &response) != nil || response.Error == "" || bytes.Contains(output, []byte("disposable-fake-key")) {
		t.Fatal("frozen publisher did not return a bounded signing refusal")
	}
	decoded, err := os.ReadFile(filepath.Join(state, "decoded"))
	if err != nil || string(decoded) != "reached" {
		t.Fatal("frozen publisher rejected the controller wire request before key inspection")
	}
	staged, err := os.ReadFile(filepath.Join(state, "bundle-read"))
	if err != nil || string(staged) != "disposable-public-source" {
		t.Fatal("frozen publisher UID could not read the staged bundle")
	}
}
