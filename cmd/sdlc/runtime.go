package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

func runtimeCommand(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || (args[0] != "build" && args[0] != "status" && args[0] != "update") {
		return fmt.Errorf("unknown runtime command; run sdlc --help")
	}
	flags := flag.NewFlagSet("runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var source string
	var offline bool
	var githubProfile string
	var dryRun bool
	var all bool
	if args[0] == "build" || args[0] == "update" {
		flags.StringVar(&source, "source", "", "SDLC clone (uses saved source when omitted)")
		if args[0] == "update" {
			flags.BoolVar(&dryRun, "dry-run", false, "check public releases and preview the update without pulling images or rebuilding")
		}
	} else {
		flags.BoolVar(&offline, "offline", false, "verify the local image and list its inventory without checking upstream updates")
		flags.BoolVar(&all, "all", false, "include bundled npm dependencies and individual Debian updates")
		flags.StringVar(&githubProfile, "github-profile", "default", "show local paired signing readiness for this GitHub profile")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("runtime %s accepts only its named options", args[0])
	}
	if args[0] == "status" {
		if err := githubauth.ValidateProfile(githubProfile); err != nil {
			return err
		}
	}
	manager, err := runtimeimage.New(output, diagnostics)
	if err != nil {
		return err
	}
	if args[0] == "status" {
		// Pairing/signing readiness does not mask runtime/dependency errors.
		pair, pairErr := githubprofile.Load(manager.Directory, githubProfile)
		if pairErr != nil {
			fmt.Fprintln(output, "GitHub pairing needs attention: run sdlc github pair --profile "+githubProfile+".")
		} else if err := pairedSigningStatus(manager, pair, output); err != nil {
			fmt.Fprintf(output, "Signing setup needs attention: %v\n", err)
		}
		return runtimeStatus(ctx, manager, runtimeupdates.New(), offline, all, output)
	}
	if args[0] == "update" {
		return runtimeUpdate(ctx, manager, runtimeupdates.New(), source, dryRun, output)
	}
	state, err := manager.Build(ctx, source)
	if err != nil {
		return err
	}
	return printRuntime(output, state)
}

func pairedSigningStatus(runtime runtimeimage.Manager, pair githubprofile.Pair, output io.Writer) error {
	printPair(output, pair, "")
	profile, err := signingProfile(runtime, pair.SigningProfile)
	if err != nil {
		return err
	}
	if err := pair.CheckSigning(profile); err != nil {
		return err
	}
	return signingStatus(runtime.Directory, pair.SigningProfile, false, output)
}

func printRuntime(output io.Writer, state runtimeimage.State) error {
	_, err := fmt.Fprintf(output, "Shared image: %s\nImage ID: %s\nSource revision: %s\n%s\n", runtimeimage.Image, state.ImageID, state.Revision, state.Tools)
	return err
}

type runtimeStatusManager interface {
	Status(context.Context) (runtimeimage.State, error)
	PackageUpdates(context.Context, runtimeimage.State) ([]runtimeimage.PackageUpdate, error)
}

func runtimeStatus(ctx context.Context, manager runtimeStatusManager, checker runtimeupdates.Checker, offline, all bool, output io.Writer) error {
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if err := printRuntime(output, state); err != nil {
		return err
	}
	if state.Inventory == nil {
		if _, err := fmt.Fprintln(output, "Dependency inventory unavailable for this older image; rebuild with sdlc runtime build to record all dependency versions and catalogue revisions."); err != nil {
			return err
		}
		if !offline {
			return fmt.Errorf("dependency check incomplete: this image needs a build inventory")
		}
		return nil
	}
	if !offline {
		if _, err := fmt.Fprintln(output, "Checking public upstream metadata (45-second limit)..."); err != nil {
			return err
		}
	}
	checkContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	packageCheck := func(ctx context.Context) ([]runtimeimage.PackageUpdate, error) {
		return manager.PackageUpdates(ctx, state)
	}
	var report runtimeupdates.Report
	if state.Source != "" || state.BuildRecipe != "" || state.DependencyPins != nil {
		// The recipe supplies build-only and sidecar provenance for older images.
		// Failure remains visible as incomplete metadata rather than preventing
		// the inventory's other checks.
		recipe := []byte(state.BuildRecipe)
		if len(recipe) == 0 {
			recipe, _ = readRuntimeRecipe(state.Source)
		}
		report, err = checker.CheckState(checkContext, state, recipe, offline, packageCheck)
	} else {
		report, err = checker.Check(checkContext, *state.Inventory, offline, packageCheck)
	}
	if err != nil {
		return err
	}
	print := report.PrintSummary
	if all {
		print = report.Print
	}
	if err := print(output); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if report.Incomplete() {
		return fmt.Errorf("dependency check incomplete; see unavailable results above")
	}
	return nil
}

type runtimeUpdateManager interface {
	runtimeStatusManager
	BuildWithOptions(context.Context, string, runtimeimage.BuildOptions) (runtimeimage.State, error)
}

func runtimeUpdate(ctx context.Context, manager runtimeUpdateManager, checker runtimeupdates.Checker, source string, dryRun bool, output io.Writer) error {
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if state.Inventory == nil {
		return fmt.Errorf("dependency inventory unavailable; rebuild with sdlc runtime build before updating")
	}
	if source == "" {
		source = state.Source
	} else {
		source, err = filepath.Abs(source)
		if err != nil {
			return fmt.Errorf("cannot resolve runtime source")
		}
	}
	recipe, err := readRuntimeRecipe(source)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Checking public releases and preparing dependency updates (45-second limit)..."); err != nil {
		return err
	}
	checkContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	plan, err := checker.Plan(checkContext, state, recipe, func(ctx context.Context) ([]runtimeimage.PackageUpdate, error) {
		return manager.PackageUpdates(ctx, state)
	})
	cancel()
	if err != nil {
		return fmt.Errorf("runtime update plan failed; the selected runtime is unchanged: %w", err)
	}
	if err := plan.Print(output); err != nil {
		return err
	}
	if dryRun {
		_, err := fmt.Fprintln(output, "Preview complete; no images pulled, pins saved or runtime rebuilt. Apply with sdlc runtime update.")
		return err
	}
	if _, err := fmt.Fprintln(output, "Rebuilding a fresh runtime with the selected versions. Login volumes and signing profiles stay in their existing storage."); err != nil {
		return err
	}
	candidate, err := manager.BuildWithOptions(ctx, source, runtimeimage.BuildOptions{
		Pins: &plan.Pins, Refresh: true, ExpectedImageID: state.ImageID,
		ValidateCandidate: func(ctx context.Context, candidate runtimeimage.State) error {
			if err := validateRuntimePacks(candidate, plan.ExpectedRuntimes); err != nil {
				return err
			}
			updates, err := manager.PackageUpdates(ctx, candidate)
			if err != nil {
				return fmt.Errorf("candidate Debian package verification failed: %w", err)
			}
			if len(updates) != len(candidate.Inventory.Packages) {
				return fmt.Errorf("candidate Debian package verification is incomplete")
			}
			for _, dependency := range updates {
				if dependency.Candidate == "" || dependency.Update {
					return fmt.Errorf("candidate Debian packages are unavailable or still need updates")
				}
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	if err := printRuntime(output, candidate); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Updated runtime selected. Checking its installed dependency inventory..."); err != nil {
		return err
	}
	// Keep unresolved parent-controlled dependencies and metadata errors visible.
	// A post-selection metadata failure does not undo a validated installation.
	return runtimeStatus(ctx, manager, checker, false, false, output)
}

func validateRuntimePacks(candidate runtimeimage.State, expected map[string]string) error {
	if candidate.Inventory == nil || len(expected) != 2 || expected["Microsoft.NETCore.App"] == "" || expected["Microsoft.AspNetCore.App"] == "" {
		return fmt.Errorf("candidate .NET runtime verification is incomplete")
	}
	found := make(map[string]bool)
	for _, dependency := range candidate.Inventory.Dependencies {
		if dependency.Kind != "dotnet-runtime" {
			continue
		}
		if found[dependency.Source] || expected[dependency.Source] != dependency.Version {
			return fmt.Errorf("candidate .NET runtime packs do not match the planned versions")
		}
		found[dependency.Source] = true
	}
	if len(found) != len(expected) {
		return fmt.Errorf("candidate .NET runtime verification is incomplete")
	}
	return nil
}

func readRuntimeRecipe(source string) ([]byte, error) {
	if source == "" || !filepath.IsAbs(source) {
		return nil, fmt.Errorf("runtime source is unavailable; specify --source with the SDLC clone")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil, fmt.Errorf("cannot open runtime source; specify --source with the SDLC clone")
	}
	defer root.Close()
	for index, name := range []string{"runtime", filepath.Join("runtime", "Dockerfile")} {
		info, err := root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (index == 0 && !info.IsDir()) || (index == 1 && (!info.Mode().IsRegular() || info.Size() > 2<<20)) {
			return nil, fmt.Errorf("runtime recipe must be a bounded regular file without symlinks")
		}
	}
	file, err := root.Open(filepath.Join("runtime", "Dockerfile"))
	if err != nil {
		return nil, fmt.Errorf("cannot read runtime recipe")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, fmt.Errorf("cannot read bounded runtime recipe")
	}
	return data, nil
}
