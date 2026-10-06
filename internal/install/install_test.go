package install

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/buildinfo"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	source := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SDLC_STATE_DIR", t.TempDir())
	t.Setenv("GOOS", "")
	t.Setenv("GOARCH", "")
	if err := os.MkdirAll(filepath.Join(source, "cmd", "sdlc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module github.com/tjpeel/sdlc\n\ngo 1.24.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return source, bin
}

func writeCommand(t *testing.T, source, value string) {
	t.Helper()
	code := "package main\nimport \"fmt\"\nfunc main() { fmt.Println(" + value + ") }\n"
	if err := os.WriteFile(filepath.Join(source, "cmd", "sdlc", "main.go"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalInstallerReplacesManagedBuildAndPreservesOtherFiles(t *testing.T) {
	source, bin := fixture(t)
	later := t.TempDir()
	if err := os.WriteFile(filepath.Join(later, executableName()), []byte("lower-priority command"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "."+string(os.PathListSeparator)+bin+string(os.PathListSeparator)+later+string(os.PathListSeparator)+os.Getenv("PATH"))
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(bin, "runtime-state.json")
	if err := os.WriteFile(preserved, []byte("preserve this state"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"first-build", "second-build"} {
		writeCommand(t, source, "\""+version+"\"")
		installer := exec.Command("go", "run", "./cmd/sdlc-install", "--source", source, "--bin-dir", bin, "--cli-only")
		installer.Dir = root
		if output, err := installer.CombinedOutput(); err != nil {
			t.Fatalf("local installer: %v\n%s", err, output)
		}
		found, err := exec.LookPath(executableName())
		if err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(found, "--version").CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != version {
			t.Fatalf("installed command: %v, %s", err, output)
		}
	}
	content, err := os.ReadFile(preserved)
	if err != nil || string(content) != "preserve this state" {
		t.Fatal("reinstall changed unrelated state", err)
	}
}

func TestFailedReinstallPreservesExecutable(t *testing.T) {
	source, bin := fixture(t)
	writeCommand(t, source, "\"working-build\"")
	path, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	writeCommand(t, source, "undefinedSymbol")
	if _, err := Build(context.Background(), source, bin, io.Discard, io.Discard); err == nil {
		t.Fatal("broken build installed successfully")
	}
	output, err := exec.Command(path).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "working-build" {
		t.Fatal("failed reinstall damaged existing command", err)
	}
}

func TestInstalledRootCommandReportsBuildIdentity(t *testing.T) {
	_, bin := fixture(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path, err := Build(context.Background(), root, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	revision := exec.Command("git", "rev-parse", "HEAD")
	revision.Dir = root
	sha, err := revision.Output()
	if err != nil {
		t.Fatal(err)
	}
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = root
	dirty, err := status.Output()
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.TrimSpace(string(sha))
	if len(dirty) != 0 {
		identity += "-dirty"
	}
	expected := "sdlc " + buildinfo.Version + " (" + identity + "; " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if strings.TrimSpace(string(output)) != expected {
		t.Fatalf("version identity: want %q, got %q", expected, output)
	}
}

func TestUnmanagedCommandAndShadowedPathAreRejected(t *testing.T) {
	source, bin := fixture(t)
	writeCommand(t, source, "\"new-build\"")
	destination := filepath.Join(bin, executableName())
	if err := os.WriteFile(destination, []byte("unmanaged"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), source, bin, io.Discard, io.Discard); err == nil {
		t.Fatal("unmanaged command was replaced")
	}
	content, _ := os.ReadFile(destination)
	if string(content) != "unmanaged" {
		t.Fatal("unmanaged executable was changed")
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, executableName()), []byte("shadow"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := Build(context.Background(), source, bin, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "resolves first") {
		t.Fatal("shadowed installation was accepted", err)
	}
}
