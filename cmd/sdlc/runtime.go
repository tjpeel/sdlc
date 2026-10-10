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
	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/homebrew"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

func runtimeCommand(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && args[0] == "headroom" {
		if len(args) != 2 || (args[1] != "build" && args[1] != "status") {
			return fmt.Errorf("usage: sdlc runtime headroom build|status")
		}
		var config headroom.Config
		var err error
		if args[1] == "build" {
			config, err = headroom.Build(ctx, providerauth.LocalDocker{})
		} else {
			config, err = headroom.Resolve(ctx, providerauth.LocalDocker{}, "passthrough")
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Headroom %s: %s\nImage ID: %s\nPolicy: %s\n", headroom.Version, headroom.Image, config.ImageID, config.PolicyVersion)
		return err
	}
	if len(args) == 0 || (args[0] != "build" && args[0] != "status" && args[0] != "update") {
		return fmt.Errorf("unknown runtime command; run sdlc --help")
	}
	flags := flag.NewFlagSet("runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var source string
	var name, skillsSource string
	var offline bool
	var githubProfile string
	var dryRun bool
	var all bool
	var sourcePins bool
	var force bool
	var agentTools bool
	var updateDockerfile bool
	flags.StringVar(&name, "name", "", "select a separate named runtime (default: shared local image)")
	if args[0] == "build" || args[0] == "update" {
		flags.StringVar(&source, "source", "", "SDLC source (uses bundled Homebrew source or saved source when omitted)")
		if args[0] == "build" {
			flags.StringVar(&skillsSource, "skills-source", "", "clean committed local skills Git catalogue (requires --name)")
			flags.BoolVar(&sourcePins, "source-pins", false, "use source Dockerfile pins instead of private dependency overrides")
			flags.BoolVar(&force, "force", false, "replace runtime despite stopped saved work; retain its files and previous image")
		}
		if args[0] == "update" {
			flags.BoolVar(&updateDockerfile, "update-dockerfile", false, "with --agent-tools, save selected pins in the explicit source checkout")
			flags.BoolVar(&agentTools, "agent-tools", false, "update only Codex, Claude Code, skills and agents; retain other pins")
			flags.BoolVar(&dryRun, "dry-run", false, "check public releases and preview the update without pulling images or rebuilding")
		}
	} else {
		flags.BoolVar(&offline, "offline", false, "verify the local image and list its inventory without checking upstream updates")
		flags.BoolVar(&all, "all", false, "include bundled npm dependencies and individual Debian updates")
		flags.StringVar(&githubProfile, "github-profile", "", "show local paired signing readiness for this profile (uses the project account when omitted)")
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
	if updateDockerfile && (!agentTools || source == "") {
		return fmt.Errorf("--update-dockerfile requires --agent-tools and an explicit --source checkout")
	}
	manager, err := runtimeimage.NewNamed(output, diagnostics, name)
	if err != nil {
		return err
	}
	if args[0] == "status" {
		// Pairing/signing readiness does not mask runtime/dependency errors.
		runtimeGitHubStatus(ctx, manager, githubProfile, output)
		return runtimeStatus(ctx, manager, runtimeupdates.New(), offline, all, output)
	}
	source, err = runtimeSource(source, "")
	if err != nil {
		return err
	}
	if args[0] == "update" {
		if name != "" {
			return fmt.Errorf("named runtimes use runtime build --name NAME; runtime update manages the default runtime")
		}
		return runtimeUpdateWithSourcePolicy(ctx, manager, runtimeupdates.New(), source, dryRun, agentTools, updateDockerfile, output)
	}
	policy := "retain private dependency pins for the same source"
	if sourcePins {
		policy = "use source Dockerfile pins; discard private overrides after a successful build"
	}
	fmt.Fprintln(output, "Dependency policy: "+policy)
	revision, err := runtimeSourceRevision(source, "")
	if err != nil {
		return err
	}
	state, err := manager.BuildWithOptions(ctx, source, runtimeimage.BuildOptions{SkillsSource: skillsSource, SourcePins: sourcePins, SourceRevision: revision, KeepPreviousImage: force, ValidatePrevious: runtimeBuildSavedWorkGuard(manager.Directory, source, force, output)})
	if err != nil {
		return err
	}
	if state.SkillsRevision != "" {
		fmt.Fprintf(output, "Local skills commit: %s\n", state.SkillsRevision)
	}
	return printRuntime(output, state)
}

func runtimeGitHubStatus(ctx context.Context, manager runtimeimage.Manager, explicit string, output io.Writer) {
	var pair githubprofile.Pair
	var err error
	if explicit != "" {
		pair, err = githubprofile.Load(manager.Directory, explicit)
	} else {
		root, repository, checkoutErr := checkoutRepository(ctx, manager.Directory, "")
		if root == "" {
			fmt.Fprintln(output, "GitHub pairing not checked: run from a project checkout, or use --github-profile NAME (see sdlc github list).")
			return
		}
		err = checkoutErr
		if err == nil {
			pair, err = githubprofile.Select(manager.Directory, root, repository, "")
		}
	}
	if err != nil {
		if explicit != "" && os.IsNotExist(err) {
			fmt.Fprintf(output, "GitHub profile %s is unpaired. Choose a configured signing profile, then use sdlc github pair --profile %s --signing-profile KEY_PROFILE.\n", explicit, explicit)
		} else {
			fmt.Fprintf(output, "GitHub pairing/selection needs attention: %v\nInspect: sdlc github status; list accounts with sdlc github list.\n", err)
		}
		return
	}
	if err := pairedSigningStatus(manager, pair, output); err != nil {
		fmt.Fprintf(output, "Signing setup needs attention: %v\n", err)
	}
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
	_, err := fmt.Fprintf(output, "Shared image: %s\nImage ID: %s\nSource revision: %s\n%s\n", runtimeimage.Manager{Name: state.Name}.ImageName(), state.ImageID, state.Revision, state.Tools)
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
	return runtimeUpdateWithPolicy(ctx, manager, checker, source, dryRun, false, output)
}

func runtimeUpdateWithPolicy(ctx context.Context, manager runtimeUpdateManager, checker runtimeupdates.Checker, source string, dryRun, scoped bool, output io.Writer) error {
	return runtimeUpdateWithSourcePolicy(ctx, manager, checker, source, dryRun, scoped, false, output)
}

func runtimeUpdateWithSourcePolicy(ctx context.Context, manager runtimeUpdateManager, checker runtimeupdates.Checker, source string, dryRun, scoped, updateDockerfile bool, output io.Writer) error {
	if updateDockerfile && (!scoped || source == "") {
		return fmt.Errorf("--update-dockerfile requires --agent-tools and an explicit --source checkout")
	}
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if state.Inventory == nil {
		return fmt.Errorf("dependency inventory unavailable; rebuild with sdlc runtime build before updating")
	}
	source, err = runtimeSource(source, "")
	if err != nil {
		return err
	}
	if source == "" {
		source = state.Source
	} else {
		source, err = filepath.Abs(source)
		if err != nil {
			return fmt.Errorf("cannot resolve runtime source")
		}
	}
	if updateDockerfile {
		sourceInfo, statErr := os.Lstat(source)
		if statErr != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("--update-dockerfile source checkout must be a real directory without symlinks")
		}
		identity, err := project.InspectIdentity(ctx, source)
		canonical, pathErr := filepath.EvalSymlinks(source)
		if err != nil || pathErr != nil || identity.Root != canonical {
			return fmt.Errorf("--update-dockerfile requires the actual SDLC source checkout; installed package archives cannot be edited")
		}
	}
	recipe, err := readRuntimeRecipe(source)
	if err != nil {
		return err
	}
	message := "Checking public releases and preparing dependency updates (45-second limit)..."
	if scoped {
		message = "Checking agent-tool releases; retaining other dependency pins (45-second limit)..."
	}
	if _, err := fmt.Fprintln(output, message); err != nil {
		return err
	}
	checkContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	var plan runtimeupdates.Plan
	if scoped {
		plan, err = checker.PlanAgentTools(checkContext, state, recipe)
	} else {
		plan, err = checker.Plan(checkContext, state, recipe, func(ctx context.Context) ([]runtimeimage.PackageUpdate, error) {
			return manager.PackageUpdates(ctx, state)
		})
	}
	cancel()
	if err != nil {
		return fmt.Errorf("runtime update plan failed; the selected runtime is unchanged: %w", err)
	}
	if err := plan.Print(output); err != nil {
		return err
	}
	var sourceUpdate *runtimeupdates.DockerfileUpdate
	if updateDockerfile {
		sourceUpdate, err = runtimeupdates.PrepareAgentToolDockerfile(source, recipe, plan.Pins)
		if err != nil {
			return fmt.Errorf("Dockerfile update preparation failed; runtime unchanged: %w", err)
		}
	}
	if dryRun {
		if updateDockerfile {
			for _, argument := range []string{"CODEX_VERSION", "CLAUDE_VERSION", "SKILLS_REVISION", "AGENTS_REVISION"} {
				fmt.Fprintf(output, "Dockerfile: ARG %s=%s\n", argument, plan.Pins.Arguments[argument])
			}
		}
		command := "sdlc runtime update"
		if scoped {
			command += " --agent-tools"
			if updateDockerfile {
				command += " --update-dockerfile --source " + source
			}
		}
		_, err := fmt.Fprintln(output, "Preview complete; no images pulled, pins saved or runtime rebuilt. Apply with "+command+".")
		return err
	}
	if _, err := fmt.Fprintln(output, "Rebuilding a fresh runtime with the selected versions. Login volumes and signing profiles stay in their existing storage."); err != nil {
		return err
	}
	revision, err := runtimeSourceRevision(source, "")
	if err != nil {
		return err
	}
	candidate, err := manager.BuildWithOptions(ctx, source, runtimeimage.BuildOptions{
		SourceRevision: revision,
		Pins:           &plan.Pins, Refresh: true, ExpectedImageID: state.ImageID,
		ValidatePrevious: runtimeSavedWorkGuardFromEnvironment(source),
		ValidateCandidate: func(ctx context.Context, candidate runtimeimage.State) error {
			if scoped {
				// BuildWithOptions verifies the complete pinned inventory, including
				// unchanged direct dependencies. This mode makes no Debian or
				// .NET runtime update promises.
				return nil
			}
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
	if sourceUpdate != nil {
		if err := sourceUpdate.Apply(); err != nil {
			return fmt.Errorf("runtime updated, but source Dockerfile was not written: %w; review the source and retry --agent-tools --update-dockerfile", err)
		}
		fmt.Fprintln(output, "Source Dockerfile agent-tool pins updated. Review and commit this source change to share it with CI.")
	}
	if err := printRuntime(output, candidate); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Updated runtime selected. Checking its installed dependency inventory..."); err != nil {
		return err
	}
	if scoped {
		return runtimeStatus(ctx, manager, checker, true, false, output)
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

// The running keg owns its bundled source. A saved runtime may refer to an older
// keg which Homebrew has already removed; an explicit source always takes priority.
func runtimeSource(source, executable string) (string, error) {
	if source != "" {
		return source, nil
	}
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot identify running executable: %w", err)
		}
	}
	pkg, err := homebrew.Detect(executable)
	if err != nil {
		return "", err
	}
	if pkg != nil {
		return pkg.Source, nil
	}
	return "", nil
}

// Archive provenance is accepted only from the running verified package and only
// when the selected source resolves to that package's own bundle.
func runtimeSourceRevision(source, executable string) (string, error) {
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot identify running executable: %w", err)
		}
	}
	pkg, err := homebrew.Detect(executable)
	if err != nil || pkg == nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	if canonical == pkg.Source {
		return pkg.Revision, nil
	}
	return "", nil
}
