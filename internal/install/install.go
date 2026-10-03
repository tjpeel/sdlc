package install

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const commandPath = "github.com/tjpeel/sdlc/cmd/sdlc"

func executableName() string {
	if runtime.GOOS == "windows" {
		return "sdlc.exe"
	}
	return "sdlc"
}

func directory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return resolved, nil
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// Build keeps the last installed executable intact until its replacement builds.
func Build(ctx context.Context, source, binDir string, stdout, stderr io.Writer) (string, error) {
	root, err := directory(source)
	if err != nil {
		return "", fmt.Errorf("source directory: %w", err)
	}
	bin, err := directory(binDir)
	if err != nil {
		return "", fmt.Errorf("bin directory: %w", err)
	}
	onPath := false
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" && runtime.GOOS == "windows" {
			continue
		}
		if candidate, err := directory(entry); err == nil && samePath(candidate, bin) {
			onPath = true
			break
		}
		candidate, err := filepath.Abs(filepath.Join(entry, executableName()))
		if err != nil {
			return "", err
		}
		if _, err := exec.LookPath(candidate); err == nil {
			return "", fmt.Errorf("another sdlc resolves first on PATH; select that directory or adjust PATH")
		}
	}
	if !onPath {
		return "", fmt.Errorf("bin directory must already be on PATH")
	}
	destination := filepath.Join(bin, executableName())
	lockPath := filepath.Join(bin, ".sdlc-install.lock")
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return "", fmt.Errorf("cannot acquire install lock: %w", err)
	}
	defer lock.Close()
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("refusing to replace a non-regular sdlc executable")
		}
		metadata, err := buildinfo.ReadFile(destination)
		if err != nil || metadata.Path != commandPath {
			return "", fmt.Errorf("refusing to replace an unmanaged sdlc executable")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	temporary, err := os.CreateTemp(bin, ".sdlc-build-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	temporary.Close()
	defer os.Remove(path)
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=true", "-o", path, "./cmd/sdlc")
	build.Dir = root
	build.Stdout, build.Stderr = stdout, stderr
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("local Go build: %w", err)
	}
	metadata, err := buildinfo.ReadFile(path)
	if err != nil || metadata.Path != commandPath {
		return "", fmt.Errorf("built file is not the SDLC command")
	}
	for _, setting := range metadata.Settings {
		if (setting.Key == "GOOS" && setting.Value != runtime.GOOS) ||
			(setting.Key == "GOARCH" && setting.Value != runtime.GOARCH) {
			return "", fmt.Errorf("local install requires a native build; clear GOOS and GOARCH")
		}
	}
	if err := os.Chmod(path, 0755); err != nil {
		return "", err
	}
	if err := os.Rename(path, destination); err != nil {
		return "", fmt.Errorf("replace executable (close any running Windows sdlc first): %w", err)
	}
	resolved, err := exec.LookPath(executableName())
	if err != nil {
		return "", fmt.Errorf("installed command did not resolve on PATH: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil || !samePath(resolved, destination) {
		return "", fmt.Errorf("installed command is shadowed on PATH")
	}
	return destination, nil
}
