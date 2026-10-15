package runtimeimage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise Docker's own ignore rules against the real bounded source closure.
// A scratch-only COPY exports files locally: no pulls, RUN commands or accounts.
func TestOfflineDockerBuildContextKeepsRequiredFiles(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_DOCKER_TESTS=1 for Docker context filtering checks")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory, cleanup, err := buildContext(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, catalogue := range []string{"skills", "agents"} {
		sourceFile(t, directory, "local-catalogues/"+catalogue+".tar", "disposable committed catalogue fixture")
	}
	// Avoid executing any runtime installers; exercise the real context filter only.
	sourceFile(t, directory, "Dockerfile", "FROM scratch\nCOPY . /\n")
	expected := map[string][]byte{}
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if relative == ".dockerignore" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		expected[relative] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	excluded := []string{"profiles.local.json", ".secrets/token", "source/unused/private.json", "local-catalogues/unselected.tar", "local-catalogues/.secrets/token"}
	for _, path := range excluded {
		sourceFile(t, directory, path, "PRIVATE-IGNORED-FIXTURE")
	}
	output := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "build", "--network=none", "--output", "type=local,dest="+output, directory)
	command.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	if log, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline context export failed: %v\n%s", err, log)
	}
	for relative, want := range expected {
		got, err := os.ReadFile(filepath.Join(output, relative))
		if err != nil {
			t.Errorf("required prepared file excluded by Docker: %s: %v", relative, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("prepared file changed: %s", relative)
		}
	}
	for _, relative := range excluded {
		if _, err := os.Stat(filepath.Join(output, relative)); !os.IsNotExist(err) {
			t.Errorf("unselected file reached Docker output: %s (%v)", relative, err)
		}
	}
}
