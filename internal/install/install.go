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

	"github.com/tjpeel/sdlc/internal/runtimeimage"

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

// Options controls whether installation also prepares the managed runtime.
type Options struct {
	CLIOnly          bool
	Dependencies     bool
	AgentTools       bool
	UpdateDockerfile bool
	DryRun           bool
	StateDirectory   string
}

// Build retains the original CLI-only installation API.
func Build(ctx context.Context, source, binDir string, stdout, stderr io.Writer) (string, error) {
	return BuildWithOptions(ctx, source, binDir, Options{CLIOnly: true}, stdout, stderr)
}

// BuildWithOptions prepares the runtime with the candidate before replacing the host command.
func BuildWithOptions(ctx context.Context, source, binDir string, options Options, stdout, stderr io.Writer) (string, error) {
	if (options.CLIOnly && (options.Dependencies || options.AgentTools)) || (options.Dependencies && options.AgentTools) {
		return "", fmt.Errorf("--sdlc-only, --agent-tools and --dependencies are mutually exclusive")
	}

	if options.UpdateDockerfile && !options.AgentTools {
		return "", fmt.Errorf("--update-dockerfile requires --agent-tools")
	}
	if options.UpdateDockerfile {
		info, err := os.Lstat(source)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("--update-dockerfile source checkout must be a real directory without symlinks")
		}
	}
	root, err := ValidateSource(source)
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
	if err := validateDestination(destination); err != nil {
		return "", err
	}
	stateDirectory := options.StateDirectory
	if stateDirectory == "" {
		manager, err := runtimeimage.New(stdout, stderr)
		if err != nil {
			return "", err
		}
		stateDirectory = manager.Directory
	}
	stateDirectory, err = canonicalStateDirectory(stateDirectory)
	if err != nil {
		return "", err
	}
	// Explicit installer arguments may replace a stale receipt after a checkout move.
	if info, err := os.Lstat(filepath.Join(stateDirectory, ReceiptFilename)); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 16384 || info.Mode().Perm()&0077 != 0 {
			return "", fmt.Errorf("existing installation receipt must be a private regular file under 16 KiB")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	runtimeSteps := [][]string{{"runtime", "build", "--source", root, "--source-pins"}}
	if options.Dependencies || options.AgentTools {
		info, err := os.Lstat(filepath.Join(stateDirectory, "runtime.json"))
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("runtime record must be a regular file")
			}
			runtimeSteps = nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		updateArgs := []string{"runtime", "update", "--source", root}
		if options.AgentTools {
			updateArgs = append(updateArgs, "--agent-tools")
		}
		if options.UpdateDockerfile {
			updateArgs = append(updateArgs, "--update-dockerfile")
		}
		runtimeSteps = append(runtimeSteps, updateArgs)
	}
	fmt.Fprintf(stdout, "Source: %s\nExecutable: %s\n", root, destination)
	if options.CLIOnly {
		fmt.Fprintln(stdout, "Runtime: unchanged (--sdlc-only)")
	} else if options.AgentTools {
		fmt.Fprintln(stdout, "Runtime: update agent tools; preserve other pins; bootstrap with source pins if missing")
	} else if options.Dependencies {
		fmt.Fprintln(stdout, "Runtime: refresh dependencies; bootstrap with source pins if missing")
	} else {
		fmt.Fprintln(stdout, "Runtime: rebuild with source pins")
	}
	if options.UpdateDockerfile {
		fmt.Fprintln(stdout, "Dockerfile: update only the four agent-tool pins in the source checkout after runtime validation; review and commit the change to share with CI")
	}
	if options.DryRun {
		fmt.Fprintln(stdout, "Dry run: build and validate a native candidate")
		if !options.CLIOnly {
			for _, runtimeArgs := range runtimeSteps {
				fmt.Fprintf(stdout, "Dry run: candidate %s\n", strings.Join(runtimeArgs, " "))
			}
		}
		fmt.Fprintln(stdout, "Dry run: atomically replace executable and save private installation receipt; reopen the shell")
		return destination, nil
	}
	lockPath := filepath.Join(bin, ".sdlc-install.lock")
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return "", fmt.Errorf("cannot acquire install lock: %w", err)
	}
	defer lock.Close()
	if err := validateDestination(destination); err != nil {
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
	versionCommand := exec.CommandContext(ctx, path, "--version")
	versionBytes, err := versionCommand.Output()
	if err != nil {
		return "", fmt.Errorf("candidate version: %w", err)
	}
	version := strings.TrimSpace(string(versionBytes))
	revision, dirty := "", false
	for _, setting := range metadata.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			dirty = setting.Value == "true"
		}
	}
	if len(version) == 0 || len(version) > 4096 {
		return "", fmt.Errorf("candidate returned an invalid version")
	}
	hash, err := executableHash(path)
	if err != nil {
		return "", err
	}
	runtimeChanged := false
	if !options.CLIOnly {
		projectRoot := os.Getenv("SDLC_UPDATE_PROJECT_ROOT")
		if projectRoot == "" {
			projectRoot, err = os.Getwd()
			if err != nil {
				return "", fmt.Errorf("invoking project directory: %w", err)
			}
			projectRoot, err = filepath.EvalSymlinks(projectRoot)
			if err != nil {
				return "", fmt.Errorf("invoking project directory: %w", err)
			}
		}

		for _, runtimeArgs := range runtimeSteps {
			prepare := exec.CommandContext(ctx, path, runtimeArgs...)
			prepare.Dir = root
			prepare.Env = append(os.Environ(), "SDLC_STATE_DIR="+stateDirectory, "SDLC_UPDATE_PROJECT_ROOT="+projectRoot)
			prepare.Stdout, prepare.Stderr = stdout, stderr
			if err := prepare.Run(); err != nil {
				return "", fmt.Errorf("candidate runtime preparation failed; host executable unchanged (runtime already prepared: %t; check runtime status before retrying): %w", runtimeChanged, err)
			}
			runtimeChanged = true
		}
	}
	if err := os.Rename(path, destination); err != nil {
		return "", fmt.Errorf("replace executable failed (runtime prepared: %t; close any running Windows sdlc first): %w", runtimeChanged, err)
	}
	resolved, err := exec.LookPath(executableName())
	if err != nil {
		return destination, fmt.Errorf("host executable installed (runtime prepared: %t), but command did not resolve on PATH: %w", runtimeChanged, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return destination, fmt.Errorf("host executable installed (runtime prepared: %t), but PATH resolution failed: %w", runtimeChanged, err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil || !samePath(resolved, destination) {
		return destination, fmt.Errorf("host executable installed (runtime prepared: %t), but command is shadowed on PATH", runtimeChanged)
	}
	if err := saveReceipt(stateDirectory, Receipt{SchemaVersion: 1, Source: root, Executable: destination, Version: version, Revision: revision, Dirty: dirty, SHA256: hash}); err != nil {
		return destination, fmt.Errorf("host executable installed (runtime prepared: %t), but installation receipt failed: %w", runtimeChanged, err)
	}
	fmt.Fprintln(stdout, "Reopen existing SDLC shells to use the installed command.")
	return destination, nil
}

func validateDestination(destination string) error {
	// Homebrew owns files inside its kegs as well as the symlink on PATH.
	// Source installation must not alter an installed package in place.
	keg := filepath.Dir(filepath.Dir(destination))
	if filepath.Base(filepath.Dir(destination)) == "bin" && filepath.Base(filepath.Dir(keg)) == "sdlc" {
		if _, err := os.Lstat(filepath.Join(keg, "INSTALL_RECEIPT.json")); err == nil {
			return fmt.Errorf("Homebrew owns this executable; use sdlc update or brew upgrade local/sdlc/sdlc")
		}
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace a non-regular sdlc executable")
		}
		metadata, err := buildinfo.ReadFile(destination)
		if err != nil || metadata.Path != commandPath {
			return fmt.Errorf("refusing to replace an unmanaged sdlc executable")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
