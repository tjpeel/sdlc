package main

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/homebrew"
	"github.com/tjpeel/sdlc/internal/install"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// updateCommand runs the selected checkout's installer, so an older installed
// executable does not impose its build or runtime policy on newer source.
func updateCommand(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	source := flags.String("source", "", "SDLC checkout (defaults to the saved installation source)")
	binDir := flags.String("bin-dir", "", "existing PATH directory (defaults to the managed installed executable)")
	cliOnly := flags.Bool("cli-only", false, "install the CLI and keep the selected runtime")
	dependencies := flags.Bool("dependencies", false, "refresh public dependencies instead of using the checkout's pins")
	dryRun := flags.Bool("dry-run", false, "show the local installation plan without building, fetching or writing")
	pull := flags.Bool("pull", false, "fast-forward a clean source checkout before installing")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*cliOnly && *dependencies) {
		return fmt.Errorf("update accepts named options; --cli-only and --dependencies cannot be combined")
	}
	manager, err := runtimeimage.New(output, diagnostics)
	if err != nil {
		return err
	}
	if path, err := exec.LookPath("sdlc"); err == nil {
		pkg, err := homebrew.Detect(path)
		if err != nil {
			return err
		}
		if pkg != nil {
			if *binDir != "" {
				return fmt.Errorf("Homebrew owns the installed CLI destination; omit --bin-dir")
			}
			return updateHomebrew(ctx, manager.Directory, *source, *cliOnly, *dependencies, *dryRun, *pull, output, diagnostics)
		}
	}
	root, err := updateSource(ctx, manager.Directory, *source)
	if err != nil {
		return err
	}
	bin := *binDir
	if bin == "" {
		bin, err = installedBinDirectory()
		if err != nil {
			return err
		}
	}
	options := install.Options{CLIOnly: *cliOnly, Dependencies: *dependencies, DryRun: true, StateDirectory: manager.Directory}
	// Validate destination ownership and PATH before a requested source pull.
	// The real installation rechecks both after building the candidate.
	preview := io.Discard
	if *dryRun {
		preview = output
	}
	if _, err := install.BuildWithOptions(ctx, root, bin, options, preview, diagnostics); err != nil {
		return err
	}
	if *pull {
		if err := requireCleanUpdateSource(ctx, root); err != nil {
			return err
		}
		if *dryRun {
			_, err := fmt.Fprintln(output, "Would fast-forward the clean checkout with git pull --ff-only. No remote was contacted; the preview describes the current local source.")
			return err
		}
		command := exec.CommandContext(ctx, "git", "-C", root, "pull", "--ff-only", "--no-rebase")
		command.Stdout, command.Stderr = output, diagnostics
		if err := command.Run(); err != nil {
			return fmt.Errorf("source fast-forward failed; installation was not started: %w", err)
		}
		if _, err := install.ValidateSource(root); err != nil {
			return err
		}
	}
	if *dryRun {
		return nil
	}
	installerArgs := []string{"run", "./cmd/sdlc-install", "--source", root, "--bin-dir", bin}
	if *cliOnly {
		installerArgs = append(installerArgs, "--cli-only")
	}
	if *dependencies {
		installerArgs = append(installerArgs, "--dependencies")
	}
	command := exec.CommandContext(ctx, "go", installerArgs...)
	command.Dir = root
	if cwd, err := os.Getwd(); err == nil {
		if callerRoot, err := checkoutRoot(ctx, cwd); err == nil {
			command.Env = append(os.Environ(), "SDLC_UPDATE_PROJECT_ROOT="+callerRoot)
		}
	}
	command.Stdout, command.Stderr = output, diagnostics
	if err := command.Run(); err != nil {
		return fmt.Errorf("local update did not complete; see the installer result above: %w", err)
	}
	return nil
}

func updateHomebrew(ctx context.Context, stateDir, explicitSource string, cliOnly, dependencies, dryRun, pull bool, output, diagnostics io.Writer) error {
	root := explicitSource
	if root == "" {
		data, err := viewReadFile(filepath.Join(stateDir, "homebrew.json"), 16384, true)
		if err != nil {
			return fmt.Errorf("Homebrew source checkout is unavailable; use --source SDLC_DIRECTORY to refresh the local tap, or brew upgrade %s: %w", homebrew.Formula, err)
		}
		var receipt struct {
			SchemaVersion int    `json:"schema_version"`
			Formula       string `json:"formula"`
			Source        string `json:"source"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil || receipt.SchemaVersion != 1 || receipt.Formula != homebrew.Formula || !filepath.IsAbs(receipt.Source) {
			return fmt.Errorf("invalid Homebrew source receipt; specify --source SDLC_DIRECTORY")
		}
		root = receipt.Source
	}
	root, err := install.ValidateSource(root)
	if err != nil {
		return err
	}
	sourceIdentity, err := project.InspectIdentity(ctx, root)
	if err != nil || sourceIdentity.Dirty || sourceIdentity.Root != root {
		return fmt.Errorf("Homebrew packages committed source; commit or set aside source changes before updating")
	}
	installer := filepath.Join(root, "scripts", "install_homebrew.py")
	if info, err := os.Lstat(installer); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("selected source is missing the Homebrew installer")
	}
	fmt.Fprintf(output, "Homebrew: %s\nSource: %s\n", homebrew.Formula, root)
	if dryRun {
		fmt.Fprintln(output, "Dry run: refresh the committed snapshot, local formula and checksum; brew upgrade "+homebrew.Formula)
		if cliOnly {
			fmt.Fprintln(output, "Runtime: unchanged (--cli-only)")
		} else if dependencies {
			fmt.Fprintln(output, "Runtime: refresh public dependencies using the installed package source")
		} else {
			fmt.Fprintln(output, "Runtime: rebuild from the installed package with source-pins")
		}
	}
	if pull {
		if err := requireCleanUpdateSource(ctx, root); err != nil {
			return err
		}
		if dryRun {
			_, err := fmt.Fprintln(output, "Would fast-forward the clean checkout with git pull --ff-only. No remote was contacted; the preview describes current local source.")
			return err
		}
		command := exec.CommandContext(ctx, "git", "-C", root, "pull", "--ff-only", "--no-rebase")
		command.Stdout, command.Stderr = output, diagnostics
		if err := command.Run(); err != nil {
			return fmt.Errorf("source fast-forward failed; Homebrew update was not started: %w", err)
		}
	}
	if dryRun {
		return nil
	}
	args := []string{installer, "--source", root, "--state-dir", stateDir}
	if cliOnly {
		args = append(args, "--cli-only")
	}
	if dependencies {
		args = append(args, "--dependencies")
	}
	command := exec.CommandContext(ctx, "python3", args...)
	command.Dir = root
	if cwd, err := os.Getwd(); err == nil {
		if callerRoot, err := checkoutRoot(ctx, cwd); err == nil {
			command.Env = append(os.Environ(), "SDLC_UPDATE_PROJECT_ROOT="+callerRoot)
		}
	}
	command.Stdout, command.Stderr = output, diagnostics
	if err := command.Run(); err != nil {
		return fmt.Errorf("Homebrew update did not complete; see the installation result above: %w", err)
	}
	return nil
}

func updateSource(ctx context.Context, stateDir, explicit string) (string, error) {
	if explicit != "" {
		return install.ValidateSource(explicit)
	}
	receipt, err := install.ReadReceipt(stateDir)
	if err == nil {
		return install.ValidateSource(receipt.Source)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("saved installation source is unavailable; specify --source: %w", err)
	}
	// Installations created before receipts existed already recorded the source
	// in their runtime state. Read it locally; updating does not need Docker just
	// to locate the checkout.
	data, err := viewReadFile(filepath.Join(stateDir, "runtime.json"), 1<<20, true)
	if err == nil {
		var state runtimeimage.State
		if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Source == "" || !filepath.IsAbs(state.Source) {
			return "", fmt.Errorf("saved runtime source is invalid; specify --source")
		}
		return install.ValidateSource(state.Source)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("saved runtime source is unavailable; specify --source")
	}
	cwd, err := os.Getwd()
	if err == nil {
		if root, err := checkoutRoot(ctx, cwd); err == nil {
			if root, err := install.ValidateSource(root); err == nil {
				return root, nil
			}
		}
	}
	return "", fmt.Errorf("SDLC installation source is not recorded; specify --source SDLC_DIRECTORY")
}

func installedBinDirectory() (string, error) {
	path, err := exec.LookPath("sdlc")
	if err != nil {
		return "", fmt.Errorf("no installed sdlc on PATH; specify --bin-dir PATH_DIRECTORY")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("installed sdlc must be a regular managed executable; use the source installer with an explicit --bin-dir")
	}
	metadata, err := buildinfo.ReadFile(path)
	if err != nil || metadata.Path != "github.com/tjpeel/sdlc/cmd/sdlc" {
		return "", fmt.Errorf("refusing to update an unmanaged sdlc on PATH")
	}
	return filepath.Dir(path), nil
}

func requireCleanUpdateSource(ctx context.Context, root string) error {
	identity, err := project.InspectIdentity(ctx, root)
	if err != nil || identity.Dirty || identity.Branch == "" || identity.Branch == "HEAD" {
		return fmt.Errorf("--pull requires a clean source checkout on a branch; commit or set aside source changes, or omit --pull to install them locally")
	}
	return nil
}
