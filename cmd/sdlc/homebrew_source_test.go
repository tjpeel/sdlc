package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

// Build a disposable native Go command with the same public module identity.
// No installed Homebrew, account state or provider clients participate.
func bundledSourceFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keg := filepath.Join(root, "Cellar", "sdlc", "0.1.0-beta.5")
	source := filepath.Join(keg, "libexec", "source")
	if err := os.MkdirAll(filepath.Join(source, "cmd", "sdlc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(keg, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":           "module github.com/tjpeel/sdlc\n\ngo 1.25.0\n",
		"cmd/sdlc/main.go": "package main\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(keg, "bin", "sdlc")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", executable, "./cmd/sdlc")
	build.Dir = source
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build disposable native command: %v\n%s", err, data)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	metadata := map[string]any{"schema_version": 1, "formula": "local/sdlc/sdlc", "version": "0.1.0-beta.5", "revision": strings.Repeat("a", 40), "sha256": hex.EncodeToString(hash[:])}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(keg, "libexec", "sdlc-homebrew.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keg, "INSTALL_RECEIPT.json"), []byte(`{"source":{"tap":"local/sdlc"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return executable, source
}

func TestBundledRuntimeSourceRepairsRemovedPreviousKeg(t *testing.T) {
	executable, source := bundledSourceFixture(t)
	manager := newUpdateFixture(t)
	runGitFixture(t)
	oldSource := manager.state.Source
	recipe, err := readRuntimeRecipe(oldSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime", "Dockerfile"), recipe, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(oldSource); err != nil {
		t.Fatal(err)
	}
	selected, err := runtimeSource("", executable)
	if err != nil || selected != source {
		t.Fatalf("bundled default: %s %v", selected, err)
	}
	if err := runtimeUpdate(context.Background(), manager, runtimeupdates.Checker{Client: updateHTTP(false)}, selected, true, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeSavedWorkGuard(os.Getenv("SDLC_STATE_DIR"), source)(context.Background(), runtimeimage.State{ImageID: manager.state.ImageID, Source: oldSource}); err != nil {
		t.Fatal("bundled non-Git source blocked saved-work guard", err)
	}
	if manager.builds != 0 || manager.state.Source != oldSource {
		t.Fatal("preview altered the selected runtime")
	}
	if revision, err := runtimeSourceRevision(source, executable); err != nil || revision != strings.Repeat("a", 40) {
		t.Fatalf("bundle provenance: %s %v", revision, err)
	}
	if revision, err := runtimeSourceRevision(t.TempDir(), executable); err != nil || revision != "" {
		t.Fatalf("unrelated source inherited bundle provenance: %s %v", revision, err)
	}
	if got, err := runtimeSource("explicit/source", executable); err != nil || got != "explicit/source" {
		t.Fatalf("explicit source lost priority: %s %v", got, err)
	}
}

func TestVersionDetailsUsesBundledSourceOutsideCheckout(t *testing.T) {
	executable, source := bundledSourceFixture(t)
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", stateDir)
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := versionDetailsCommand(context.Background(), []string{"--details", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var view versionView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Installed.Version != "0.1.0-beta.5" || view.Installed.Revision != strings.Repeat("a", 40) || view.Source.Path != source || view.Source.Version != view.Installed.Version || view.Source.Revision != view.Installed.Revision || view.BuiltArtifact != "verified Homebrew package manifest" {
		t.Fatalf("missing bundled identity: %+v", view)
	}
	// An explicit source still requires the usual SDLC Git checkout evidence.
	if err := versionDetailsCommand(context.Background(), []string{"--source", source}, &bytes.Buffer{}); err == nil {
		t.Fatal("explicit non-checkout source accepted")
	}
}
