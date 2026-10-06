package homebrew

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPackage(t *testing.T) {
	root := t.TempDir()
	keg := filepath.Join(root, "Cellar", "sdlc", "0.1.0-beta.5")
	source := filepath.Join(keg, "libexec", "source")
	command := filepath.Join(source, "cmd", "sdlc")
	executable := filepath.Join(keg, "bin", "sdlc")
	for _, path := range []string{command, filepath.Dir(executable), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, contents []byte) {
		t.Helper()
		if err := os.WriteFile(path, contents, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "go.mod"), []byte("module github.com/tjpeel/sdlc\n\ngo 1.25.0\n"))
	write(filepath.Join(command, "main.go"), []byte("package main\nfunc main() {}\n"))
	build := exec.Command("go", "build", "-buildvcs=false", "-o", executable, "./cmd/sdlc")
	build.Dir = source
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build disposable SDLC executable: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	metadata := manifest{SchemaVersion: 1, Formula: Formula, Version: "0.1.0-beta.5", Revision: strings.Repeat("a", 40), SHA256: hex.EncodeToString(hash[:])}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(keg, "libexec", ManifestFilename)
	receiptPath := filepath.Join(keg, "INSTALL_RECEIPT.json")
	receipt := []byte(`{"source":{"tap":"local/sdlc"},"unrelated_homebrew_field":true}`)
	write(manifestPath, encoded)
	write(receiptPath, receipt)
	link := filepath.Join(root, "bin", "sdlc")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	canonicalExecutable, _ := filepath.EvalSymlinks(executable)
	canonicalSource, _ := filepath.EvalSymlinks(source)
	for _, path := range []string{executable, link} {
		got, err := Detect(path)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Executable != canonicalExecutable || got.Source != canonicalSource || got.Version != "0.1.0-beta.5" || got.Revision != strings.Repeat("a", 40) || got.Formula != Formula {
			t.Fatalf("incorrect package from %s: %+v", path, got)
		}
	}
	t.Run("tampered binary", func(t *testing.T) {
		write(executable, append(binary, 'x'))
		defer write(executable, binary)
		if _, err := Detect(link); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("tampered executable accepted: %v", err)
		}
	})
	t.Run("wrong tap", func(t *testing.T) {
		write(receiptPath, []byte(`{"source":{"tap":"another/sdlc"}}`))
		defer write(receiptPath, receipt)
		if _, err := Detect(link); err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Fatalf("foreign tap accepted: %v", err)
		}
	})
	t.Run("missing manifest", func(t *testing.T) {
		if err := os.Remove(manifestPath); err != nil {
			t.Fatal(err)
		}
		defer write(manifestPath, encoded)
		if _, err := Detect(link); err == nil {
			t.Fatal("claimed package with missing manifest accepted")
		}
	})
	t.Run("source redirected outside keg", func(t *testing.T) {
		moved := source + "-moved"
		if err := os.Rename(source, moved); err != nil {
			t.Fatal(err)
		}
		defer func() {
			os.Remove(source)
			os.Rename(moved, source)
		}()
		if err := os.Symlink(moved, source); err != nil {
			t.Fatal(err)
		}
		if _, err := Detect(link); err == nil {
			t.Fatal("package with redirected source accepted")
		}
	})
}

func TestDetectOrdinaryExecutable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sdlc")
	if err := os.WriteFile(path, []byte("ordinary executable"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, filepath.Join(root, "missing")} {
		got, err := Detect(candidate)
		if err != nil || got != nil {
			t.Fatalf("ordinary path treated as Homebrew: %+v, %v", got, err)
		}
	}
}
