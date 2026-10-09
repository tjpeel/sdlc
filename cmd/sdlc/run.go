package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/instructions"
	"github.com/tjpeel/sdlc/internal/notify"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
	"github.com/tjpeel/sdlc/internal/signing"
	"github.com/tjpeel/sdlc/internal/terminallaunch"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const runUsage = "Usage: sdlc run --reference REFERENCE --ticket NUMBERED_FILE [--provider codex|claude]\n  [--github-profile NAME] [--input RELATIVE_PATH] [--base main] [--branch BRANCH] [--repo OWNER/REPO]\n  [--model MODEL] [--effort LEVEL] [--review-model MODEL] [--review-effort LEVEL]\n  [--runtime NAME] [--docker-tests] [--timeout 2h] [--dry-run] [--notify off|desktop|bell] [--sound]\nFeature: sdlc run --reference REFERENCE --all [--parallel 1] [--watch] [--alternate-providers] [--dry-run]\nResume: sdlc run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID [--answer-file FILE]\nThe selected implementation provider must be logged in. Independent review uses the opposite provider when logged in.\n--docker-tests enables a privileged, disposable Docker daemon for integration checks."

type selectedInputs []string

func (inputs *selectedInputs) String() string { return strings.Join(*inputs, ",") }
func (inputs *selectedInputs) Set(value string) error {
	if value == "" {
		return fmt.Errorf("input path cannot be empty")
	}
	*inputs = append(*inputs, value)
	return nil
}

type runOptions struct {
	runtimeName, headroomMode                                                                                                          string
	frozenHeadroom                                                                                                                     *headroom.Config
	reference, ticket, provider, base, branch, repository, model, effort, reviewModel, reviewEffort, resume, answerFile, githubProfile string
	inputs                                                                                                                             selectedInputs
	dockerTests, dryRun                                                                                                                bool
	timeout                                                                                                                            time.Duration
	notifications                                                                                                                      notify.Options
	all, watch, alternate                                                                                                              bool
	terminal                                                                                                                           bool
	parallel                                                                                                                           int
	jsonOutput                                                                                                                         bool
	terminalMode, launchID                                                                                                             string
	// Internal resume selections recover recorded paths and freeze the checkpoint.
	root, runID, checkpoint               string
	featureOwned                          bool
	snapshot                              *workrun.BranchSnapshot
	frozenImage                           string
	frozenIdentity                        *workrun.PublicationIdentity
	frozenRoles                           *workrun.Roles
	frozenConfig                          *project.Config
	frozenInstructions                    *string
	frozenInputs                          bool
	frozenInputHashes                     map[string]string
	frozenSigningImage, frozenDaemonImage string
	supplied                              map[string]bool
}

func parseRunOptions(args []string) (runOptions, error) {
	var options runOptions
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.reference, "reference", "", "selected work folder")
	flags.StringVar(&options.ticket, "ticket", "", "exact numbered ticket filename")
	flags.StringVar(&options.githubProfile, "github-profile", "", "registered GitHub account/key pair; defaults to repository selection")
	flags.StringVar(&options.provider, "provider", "codex", "implementation provider")
	flags.StringVar(&options.runtimeName, "runtime", "", "named runtime for one ticket; resume uses the recorded selection")
	flags.StringVar(&options.headroomMode, "headroom", "off", "provider route: off, passthrough or optimize (opt-in local Headroom)")
	flags.StringVar(&options.base, "base", "main", "PR base branch")
	flags.StringVar(&options.branch, "branch", "", "destination branch; defaults to a unique ticket branch")
	flags.StringVar(&options.repository, "repo", "", "GitHub owner/repo; defaults to saved SDLC identity, then origin")
	flags.StringVar(&options.model, "model", "", "implementation lead model")
	flags.StringVar(&options.effort, "effort", "", "implementation lead reasoning effort")
	flags.StringVar(&options.reviewModel, "review-model", "", "opposite-provider review lead model")
	flags.StringVar(&options.reviewEffort, "review-effort", "", "review lead reasoning effort")
	flags.StringVar(&options.root, "run-root", "", "internal recorded root for resume")
	flags.StringVar(&options.checkpoint, "checkpoint", "", "internal frozen checkpoint for resume")
	flags.StringVar(&options.resume, "resume", "", "recorded run ID or unique prefix of at least three characters")
	flags.StringVar(&options.answerFile, "answer-file", "", "human answer to recorded questions")
	flags.Var(&options.inputs, "input", "exact additional requirements input; repeatable")
	flags.BoolVar(&options.dockerTests, "docker-tests", false, "override project docker_tests; enable privileged Docker integration-test daemon")
	flags.BoolVar(&options.dryRun, "dry-run", false, "print selection without Docker, login checks or execution")
	flags.BoolVar(&options.jsonOutput, "json", false, "structured offline plan or terminal launch receipt")
	flags.StringVar(&options.terminalMode, "terminal", "", "background: open an independent run terminal without activation")
	flags.StringVar(&options.launchID, "launch-id", "", "idempotency key for a background terminal launch")
	flags.BoolVar(&options.all, "all", false, "schedule every pending ticket in the selected work folder")
	flags.IntVar(&options.parallel, "parallel", 1, "feature only: maximum concurrent ticket controllers (1-8)")
	flags.BoolVar(&options.watch, "watch", false, "feature only: watch human merges and reconcile remaining PRs")
	flags.BoolVar(&options.alternate, "alternate-providers", false, "feature only: alternate implementation providers in ticket order")
	flags.DurationVar(&options.timeout, "timeout", 2*time.Hour, "maximum duration of this controller invocation")
	flags.StringVar(&options.notifications.Mode, "notify", "off", "local notifications: off, desktop or bell")
	flags.BoolVar(&options.notifications.Sound, "sound", false, "desktop notification sound")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	options.supplied = map[string]bool{}
	flags.Visit(func(f *flag.Flag) { options.supplied[f.Name] = true })
	if options.reference == "" || flags.NArg() != 0 || !options.all && options.ticket == "" {
		return options, fmt.Errorf("run requires --reference REFERENCE and either --ticket NUMBERED_FILE or --all")
	}
	if options.all && (options.ticket != "" || options.branch != "" || options.resume != "" || options.answerFile != "") {
		return options, fmt.Errorf("--all owns ticket selection and branches; resume the feature by repeating its command, or answer a selected run separately")
	}
	if options.all && options.runtimeName != "" {
		return options, fmt.Errorf("--runtime selects one ticket runtime; use --ticket without --all")
	}
	if options.supplied["runtime"] && options.runtimeName == "" {
		return options, fmt.Errorf("--runtime cannot be empty")
	}
	if err := runtimeimage.ValidateName(options.runtimeName); err != nil {
		return options, err
	}
	if options.parallel < 1 || options.parallel > 8 {
		return options, fmt.Errorf("parallel must be between 1 and 8")
	}
	invalidFeatureFlag := false
	flags.Visit(func(f *flag.Flag) {
		if !options.all && (f.Name == "parallel" || f.Name == "watch" || f.Name == "alternate-providers") {
			invalidFeatureFlag = true
		}
	})
	if invalidFeatureFlag {
		return options, fmt.Errorf("--parallel, --watch and --alternate-providers require --all")
	}
	if options.alternate && (options.model != "" || options.reviewModel != "") {
		return options, fmt.Errorf("alternating providers use their configured model defaults; model overrides require one implementation provider")
	}
	if options.provider != "codex" && options.provider != "claude" {
		return options, fmt.Errorf("provider must be codex or claude")
	}
	if _, err := headroom.Selection(options.headroomMode); err != nil {
		return options, err
	}
	explicitEmptyProfile := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "github-profile" && f.Value.String() == "" {
			explicitEmptyProfile = true
		}
	})
	if explicitEmptyProfile {
		return options, fmt.Errorf("--github-profile cannot be empty")
	}
	if err := githubauth.ValidateProfile(options.githubProfile); err != nil {
		return options, err
	}
	if options.timeout < time.Minute || options.timeout > 24*time.Hour {
		return options, fmt.Errorf("timeout must be between 1m and 24h")
	}
	if err := options.notifications.Validate(); err != nil {
		return options, err
	}
	if options.terminalMode != "" && options.terminalMode != "background" {
		return options, fmt.Errorf("--terminal must be background")
	}
	if options.supplied["terminal"] && options.terminalMode == "" {
		return options, fmt.Errorf("--terminal cannot be empty")
	}
	if options.dryRun && options.terminalMode != "" {
		return options, fmt.Errorf("--dry-run and --terminal cannot be combined")
	}
	if options.jsonOutput && !options.dryRun && options.terminalMode == "" {
		return options, fmt.Errorf("run --json requires --dry-run or --terminal background")
	}
	if options.supplied["launch-id"] && (options.launchID == "" || options.terminalMode == "") {
		return options, fmt.Errorf("--launch-id requires --terminal background and a nonempty UUID")
	}
	if options.resume != "" {
		if options.supplied["run-root"] && (options.root == "" || !filepath.IsAbs(options.root) || filepath.Clean(options.root) != options.root) {
			return options, fmt.Errorf("--run-root requires an absolute recorded root")
		}
		if options.supplied["checkpoint"] {
			if _, err := time.Parse(time.RFC3339Nano, options.checkpoint); err != nil {
				return options, fmt.Errorf("--checkpoint requires a recorded timestamp")
			}
		}
		invalid := ""
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "run-root", "checkpoint", "reference", "ticket", "resume", "answer-file", "timeout", "dry-run", "notify", "sound", "json", "terminal", "launch-id":
			default:
				invalid = f.Name
			}
		})
		if invalid != "" {
			return options, fmt.Errorf("resume preserves recorded settings; cannot change --%s", invalid)
		}
	} else if options.root != "" || options.checkpoint != "" {
		return options, fmt.Errorf("--run-root and --checkpoint require --resume")
	} else if options.answerFile != "" {
		return options, fmt.Errorf("--answer-file requires --resume")
	}
	return options, nil
}

func runCommand(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseRunOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(output, runUsage+"\nOffline JSON plan: --dry-run --json\nIndependent iTerm2 controller: --terminal background [--launch-id UUID] [--json]; configure with sdlc terminal setup.")
		return err
	}
	if err != nil {
		return err
	}
	if options.terminalMode != "" {
		return launchRunCommand(ctx, args, options, output)
	}
	if options.all {
		return runSeriesCommand(ctx, options, output)
	}
	return runSelectedCommand(ctx, options, output)
}

func runSelectedCommand(ctx context.Context, options runOptions, output io.Writer) (resultErr error) {
	if options.notifications.Mode == "bell" && !options.terminal && !dashboardTerminal(output) {
		return fmt.Errorf("bell notifications require a terminal; use desktop for unattended runs")
	}
	sender, err := notify.New(options.notifications, output)
	if err != nil {
		return err
	}
	observe := runNotifications(ctx, output, sender)
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	current := options.root
	if current == "" {
		current, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	work, err := project.InspectWork(ctx, current, options.reference)
	if err != nil {
		return err
	}
	ticket := ""
	for _, path := range work.Tickets {
		if options.ticket == filepath.Base(path) || options.ticket == path {
			ticket = filepath.Base(path)
		}
	}
	if ticket == "" {
		return fmt.Errorf("select an exact numbered ticket filename from sdlc work")
	}
	runtime, err := runtimeimage.NewNamed(output, io.Discard, options.runtimeName)
	if err != nil {
		return err
	}
	var runtimeLease *os.File
	defer func() {
		if runtimeLease != nil {
			runtimeLease.Close()
		}
	}()
	var journal workrun.Journal
	var directory string
	var preparationOwner *os.File
	defer func() {
		if preparationOwner == nil {
			return
		}
		defer preparationOwner.Close()
		if resultErr != nil {
			if _, err := os.Lstat(filepath.Join(directory, "journal.json")); os.IsNotExist(err) {
				failure := runstatus.PreparationFailure{Version: 1, ID: journal.ID, Root: work.Root, Reference: options.reference, Ticket: ticket, Roles: journal.Plan.Roles, FailedAt: time.Now().UTC(), Reason: resultErr.Error(), FeatureOwned: options.featureOwned}
				if err := runstatus.New(runtime.Directory).RegisterPreparationFailure(directory, failure, preparationOwner); err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("cannot report preparation failure: %w", err))
				} else if err := observeLaunch(ctx, terminallaunch.RunIdentity{Reference: options.reference, Directory: directory, RunIDs: []string{failure.ID}}); err != nil {
					resultErr = errors.Join(resultErr, err)
				}
			}
		}
	}()
	if options.runID != "" && options.resume == "" {
		if candidate, inspectErr := workrun.RunDirectory(work.Root, options.reference, ticket, options.runID, false); inspectErr == nil {
			if _, inspectErr := os.Lstat(filepath.Join(candidate, "journal.json")); inspectErr == nil {
				options.resume = options.runID
			}
		}
	}
	if options.resume != "" {
		directory, err = workrun.ResolveRunDirectory(work.Root, options.reference, ticket, options.resume)
		if err != nil {
			return err
		}
		options.resume = filepath.Base(directory)
		journal, err = workrun.Load(directory)
		if err != nil {
			return err
		}
		if journal.Plan.Root != work.Root || journal.Plan.Reference != options.reference || filepath.Base(journal.Plan.Ticket) != ticket {
			return fmt.Errorf("run journal does not match selected work")
		}
		runtime, err = runtimeimage.NewNamed(output, io.Discard, journal.Plan.RuntimeName)
		if err != nil {
			return err
		}
	} else {
		var roles workrun.Roles
		if options.frozenRoles != nil {
			roles = *options.frozenRoles
		} else {
			defaults, err := workrun.LoadModels(runtime.Directory)
			if err != nil {
				return err
			}
			roles, err = workrun.ResolveModels(defaults, options.provider, options.model, options.effort, options.reviewModel, options.reviewEffort)
			if err != nil {
				return err
			}
		}
		launchInput := project.Launch
		if options.frozenInputs {
			launchInput = project.LaunchFrozen
		}
		launch, err := launchInput(ctx, current, options.reference, ticket, options.inputs)
		if err != nil {
			return err
		}
		// Feature controllers supply their already-resolved frozen selection.
		// Only a fresh standalone run inherits the current project default.
		if options.frozenConfig == nil && !options.supplied["docker-tests"] {
			options.dockerTests = launch.Config.DockerTests
		}
		if !options.dryRun {
			if err := workrun.ValidateSourceHistory(ctx, launch.Root); err != nil {
				return err
			}
		}
		if !options.dryRun && len(launch.Config.Checks) == 0 {
			return fmt.Errorf("no verification checks configured: set checks in .sdlc/project.json before starting a provider run; inspect sdlc onboard status or preview with --dry-run")
		}
		if options.frozenConfig != nil && !sameSeriesConfig(launch.Config, *options.frozenConfig) {
			return fmt.Errorf("project checks or check inputs changed during the feature; restore the frozen configuration")
		}
		repository, err := resolveRepository(ctx, runtime.Directory, work.Root, options.repository)
		if err != nil {
			return err
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(repository) {
			return fmt.Errorf("--repo must name a GitHub OWNER/REPO")
		}
		pair, selectionErr := githubprofile.Select(runtime.Directory, work.Root, repository, options.githubProfile)
		if selectionErr != nil {
			// An offline plan remains useful before onboarding. Unsafe or changed
			// metadata and ambiguous choices always fail, including dry runs.
			if !options.dryRun || !(errors.Is(selectionErr, githubprofile.ErrSelectionRequired) || os.IsNotExist(selectionErr)) {
				return selectionErr
			}
		} else {
			options.githubProfile = pair.GitHubProfile
			profile, err := signingProfile(runtime, pair.SigningProfile)
			if err != nil {
				return err
			}
			if err := pair.CheckSigning(profile); err != nil {
				return err
			}
		}
		journal = workrun.Journal{Version: 1, State: "prepared", Plan: workrun.Plan{RuntimeName: options.runtimeName, GitHubProfile: options.githubProfile, SigningProfile: pair.SigningProfile, Root: work.Root, Reference: options.reference, Ticket: launch.Ticket, SourceSHA: launch.Head, Branch: options.branch, Base: options.base, Repository: repository, Roles: roles, Checks: launch.Config.Checks, DockerTests: options.dockerTests}}
		journal.Plan.Headroom, err = headroom.Selection(options.headroomMode)
		if err != nil {
			return err
		}
		if options.frozenHeadroom != nil {
			journal.Plan.Headroom = *options.frozenHeadroom
		}
		if options.dryRun {
			// Selection is known before capture; content hashes are not. Keep
			// the offline plan useful without reading requirement bodies.
			for _, path := range launch.Inputs {
				journal.Plan.Inputs = append(journal.Plan.Inputs, workrun.Input{Path: path})
			}
			for _, path := range launch.Config.InputFiles {
				journal.Plan.CheckInputs = append(journal.Plan.CheckInputs, workrun.Input{Path: path})
			}
			return printSelectedPlan(output, journal, options.jsonOutput)
		}
		if inCI() {
			return fmt.Errorf("account-authenticated ticket runs are supported here only as local single-user CLI jobs; CI requires a separately supported authentication route")
		}
		runtimeLease, err = leaseRuntimeRun(ctx, runtime.Directory, output)
		if err != nil {
			return err
		}
		id, err := workrun.NewID()
		if err != nil {
			return err
		}
		journal.ID = id
		if options.runID != "" {
			id = options.runID
			journal.ID = id
		}
		directory, err = workrun.RunDirectory(work.Root, options.reference, ticket, id, true)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(directory, "preparation.json")); err == nil {
			expected := runstatus.PreparationFailure{ID: id, Root: work.Root, Reference: options.reference, Ticket: ticket, Roles: roles}
			if !options.featureOwned {
				return fmt.Errorf("failed preparation requires its original feature controller or a new ticket run")
			}
			preparationOwner, err = runstatus.ValidatePreparationRetry(directory, expected)
		} else if os.IsNotExist(err) {
			preparationOwner, err = runstatus.OwnPreparation(directory)
		} else {
			return err
		}
		if err != nil {
			return err
		}
		branch := options.branch
		if branch == "" {
			branch = "work/" + branchPart(options.reference) + "/" + branchPart(strings.TrimSuffix(ticket, ".md")) + "-" + id[:8]
		}
		journal.Workspace = filepath.Join(directory, "workspace")
		var plan workrun.Plan
		if options.snapshot == nil {
			plan, err = workrun.Capture(ctx, launch, journal.Workspace, branch, options.base, repository, roles)
		} else {
			plan, err = workrun.CaptureSnapshot(ctx, launch, *options.snapshot, journal.Workspace, branch, repository, roles)
		}
		if err != nil {
			return err
		}
		if options.frozenInputHashes != nil {
			if err := matchSeriesCapturedInputs(plan, options.frozenInputHashes); err != nil {
				return err
			}
		}
		plan.GitHubProfile = options.githubProfile
		plan.SigningProfile = pair.SigningProfile
		plan.RuntimeName = options.runtimeName
		plan.DockerTests = options.dockerTests
		journal.Plan = plan
		var shared []byte
		if options.frozenInstructions != nil {
			shared = []byte(*options.frozenInstructions)
		} else {
			shared, err = (instructions.Manager{Directory: runtime.Directory}).Show()
			if err != nil {
				return err
			}
		}
		journal.Instructions = string(shared)
		state, err := runtime.Status(ctx)
		if err != nil {
			return err
		}
		journal.ImageID = state.ImageID
		if options.frozenHeadroom == nil {
			journal.Plan.Headroom, err = headroom.Resolve(ctx, runtime.Docker, options.headroomMode)
			if err != nil {
				return err
			}
		} else if err := journal.Plan.Headroom.Validate(); err != nil {
			return err
		}
		if options.frozenImage != "" && state.ImageID != options.frozenImage {
			return fmt.Errorf("runtime changed during the feature; restore the recorded runtime")
		}
		if options.frozenSigningImage != "" {
			journal.Plan.SigningImage, journal.Plan.DaemonImage = options.frozenSigningImage, options.frozenDaemonImage
			if err := journal.Plan.ValidateSidecarImages(); err != nil {
				return err
			}
		} else if err := freezeRuntimeImages(ctx, &journal.Plan, state, runtime, runtimeupdates.ResolveDefaultDaemonImage); err != nil {
			return err
		}
		identity, err := freezePublicationIdentity(ctx, runtime, work.Root, journal.Plan.Repository, pair)
		if err != nil {
			return err
		}
		if options.frozenIdentity != nil && *identity != *options.frozenIdentity {
			return fmt.Errorf("publication account, repository or signing identity changed during the feature")
		}
		journal.Plan.PublicationIdentity = identity
		journal.Plan.Repository = identity.RepositoryName
		if err := workrun.Save(directory, &journal); err != nil {
			return err
		}
		if err := runstatus.New(runtime.Directory).Register(directory, journal); err != nil {
			return err
		}
		if err := preparationOwner.Close(); err != nil {
			return err
		}
		preparationOwner = nil
	}
	answer := ""
	if options.resume != "" {
		if options.checkpoint != "" && options.checkpoint != journal.UpdatedAt.Format(time.RFC3339Nano) {
			return fmt.Errorf("run checkpoint changed; review the current questions again")
		}
		if options.answerFile != "" {
			if journal.State != "waiting_for_human" || len(journal.Outcome.Questions) == 0 {
				return fmt.Errorf("this run has no pending human question")
			}
			data, err := readHumanAnswer(options.answerFile)
			if err != nil {
				return err
			}
			answer = string(data)
		} else if options.checkpoint != "" && journal.State == "waiting_for_human" {
			return fmt.Errorf("answer the recorded questions with sdlc answer --run %s", journal.ID)
		}
	}
	if options.dryRun {
		if options.resume != "" {
			return printResumePlan(output, journal, answer, options.jsonOutput)
		}
		return printSelectedPlan(output, journal, options.jsonOutput)
	}
	if err := observeLaunch(ctx, terminallaunch.RunIdentity{Reference: options.reference, Directory: directory, RunIDs: []string{journal.ID}}); err != nil {
		return err
	}
	if inCI() {
		return fmt.Errorf("account-authenticated ticket runs require a local single-user CLI job")
	}
	if journal.Plan.PublicationIdentity == nil {
		return fmt.Errorf("this legacy run has no frozen Docker publication identity; start a new run")
	}
	if err := journal.Plan.Headroom.Validate(); err != nil {
		return err
	}
	if runtimeLease == nil {
		runtimeLease, err = leaseRuntimeRun(ctx, runtime.Directory, output)
		if err != nil {
			return err
		}
	}
	profile, err := runSigningProfile(runtime, journal.Plan)
	if err != nil {
		return err
	}
	if options.resume != "" {
		if err := resumeGitHubPreflight(ctx, runtime, journal); err != nil {
			return err
		}
	}
	if err := printRunPlan(output, journal, false); err != nil {
		return err
	}
	checker := workrun.DockerChecker{Runtime: runtime, ImageID: journal.ImageID, DockerTests: journal.Plan.DockerTests, DaemonImage: journal.Plan.DaemonImage, UseDefaultDaemonImage: journal.Plan.DaemonImage == ""}
	if len(journal.Plan.CheckInputs) > 0 {
		checker.InputDirectory = filepath.Join(directory, "check-inputs")
	}
	registry := runstatus.New(runtime.Directory)
	var tracker *runstatus.Tracker
	manager := providerauth.New(runtime)
	manager.OnWait = func(provider, reason string) {
		if tracker != nil {
			_ = tracker.Wait(provider, reason)
		}
		queueMessage(output, provider, reason)
	}
	manager.OnAcquired = func(provider string) {
		if tracker != nil {
			_ = tracker.ClearWait(provider)
		}
	}
	githubManager := githubauth.New(runtime)
	githubManager.Profile = journal.Plan.GitHubProfile
	githubManager.OnWait = manager.OnWait
	githubManager.OnAcquired = manager.OnAcquired
	resolver := signing.Resolver{Profile: profile, Image: journal.Plan.SigningImage}
	runner := workrun.Runner{Provider: workrun.NativeProvider{Manager: manager, ImageID: journal.ImageID}, Checker: checker, Publisher: workrun.DockerPublisher{Runtime: runtime, ImageID: journal.ImageID, Auth: githubManager, SigningKey: resolver.Resolve, ValidatePair: func() error { _, err := runSigningProfile(runtime, journal.Plan); return err }}, Repository: workrun.DockerRepository{Runtime: runtime, ImageID: journal.ImageID}, Output: io.Discard, ProgressOutput: output, Instructions: journal.Instructions, ReviewWorkspace: workrun.PrepareReview}
	runner.OnStart = func(snapshot workrun.Journal) error {
		var err error
		tracker, err = registry.Begin(directory, snapshot)
		if err == nil {
			// Resume may resolve an old question or failure immediately. Seed an
			// active baseline and alert only if the controller stops there again.
			observe(runNotificationBaseline(snapshot), false)
		}
		return err
	}
	runner.OnState = func(snapshot workrun.Journal) error {
		if err := tracker.Update(snapshot); err != nil {
			return err
		}
		observe(snapshot, false)
		return nil
	}
	runner.OnOutput = func(data []byte) { tracker.Activity(data) }
	runner.OnNativeOutput = func(data []byte) { tracker.NativeEvent(data) }
	runner.OnFinish = func() error {
		err := tracker.Close()
		observe(journal, true)
		return err
	}
	err = runner.Run(ctx, directory, &journal, answer)
	fmt.Fprintf(output, "Private run state: %q\nResume: sdlc run --reference %q --ticket %q --resume %s\n", directory, options.reference, ticket, journal.ID)
	return err
}

// Keep the selected runtime available across the controller's phase boundaries,
// including checks and publication after a provider releases its own lease.
func leaseRuntimeRun(ctx context.Context, directory string, output io.Writer) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("cannot open SDLC state directory: %w", err)
	}
	lease, err := filelock.AcquireContext(ctx, filepath.Join(directory, "runtime-build.lock"), filelock.Shared, func() {
		queueMessage(output, "runtime", "runtime_busy")
	})
	if err != nil {
		return nil, fmt.Errorf("runtime lease unavailable: %w", err)
	}
	return lease, nil
}

func freezeSidecarImages(plan *workrun.Plan, state runtimeimage.State) error {
	signingImage, daemonImage := runtimepins.DefaultSigningImage, ""
	if state.DependencyPins != nil {
		if err := runtimepins.ValidateSigningImage(state.DependencyPins.SigningImage); err != nil {
			return err
		}
		if err := runtimepins.ValidateDaemonImage(state.DependencyPins.DaemonImage); err != nil {
			return err
		}
		signingImage = state.DependencyPins.SigningImage
		daemonImage = state.DependencyPins.DaemonImage
	}
	plan.SigningImage, plan.DaemonImage = signingImage, daemonImage
	return plan.ValidateSidecarImages()
}

func freezeRuntimeImages(ctx context.Context, plan *workrun.Plan, state runtimeimage.State, runtime runtimeimage.Manager, resolveDaemon func(context.Context) (string, error)) error {
	selected := *plan
	if err := freezeSidecarImages(&selected, state); err != nil {
		return err
	}
	if selected.DockerTests && selected.DaemonImage == "" {
		image, err := resolveDaemon(ctx)
		if err != nil {
			return fmt.Errorf("cannot freeze repository check daemon: %w", err)
		}
		if err := runtimepins.ValidateDaemonImage(image); err != nil {
			return err
		}
		if err := runtime.PullPublic(ctx, image); err != nil {
			return fmt.Errorf("cannot prepare pinned repository check daemon: %w", err)
		}
		selected.DaemonImage = image
	}
	*plan = selected
	return nil
}

func freezePublicationIdentity(ctx context.Context, runtime runtimeimage.Manager, root, repository string, pair githubprofile.Pair) (result *workrun.PublicationIdentity, resultErr error) {
	name := pair.GitHubProfile
	current, err := githubprofile.Load(runtime.Directory, name)
	if err != nil {
		return nil, fmt.Errorf("register the account/key pairing with sdlc github pair before launching work: %w", err)
	}
	if current != pair {
		return nil, fmt.Errorf("account/key pairing changed after repository selection; retry after inspecting github status")
	}
	profile, err := signingProfile(runtime, pair.SigningProfile)
	if err != nil {
		return nil, fmt.Errorf("configure a private signing profile before launching work: %w", err)
	}
	if err := pair.CheckSigning(profile); err != nil {
		return nil, err
	}
	manager := githubauth.New(runtime)
	manager.Profile = name
	session, err := manager.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if session.Close() != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("GitHub preflight cleanup failed"))
		}
	}()
	account, err := session.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if account.ID != pair.AccountID || account.Login != pair.Login {
		return nil, fmt.Errorf("GitHub login differs from the paired account; restore it or deliberately pair --replace")
	}
	if err := registeredSigningKey(ctx, session, account, pair.PublicKey); err != nil {
		return nil, err
	}
	repositoryIdentity, err := session.Repository(ctx, repository)
	if err != nil {
		return nil, err
	}
	current, err = githubprofile.Load(runtime.Directory, name)
	if err != nil || current != pair {
		return nil, fmt.Errorf("account/key pairing changed during preflight")
	}
	values := map[string]string{}
	for _, name := range []string{"user.name", "user.email"} {
		command := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", name)
		for _, entry := range os.Environ() {
			key := strings.SplitN(entry, "=", 2)[0]
			if !strings.HasPrefix(key, "GIT_") && key != "GH_TOKEN" && key != "GITHUB_TOKEN" && key != "SSH_AUTH_SOCK" {
				command.Env = append(command.Env, entry)
			}
		}
		data, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("set project Git name and email before launching work")
		}
		values[name] = strings.TrimSpace(string(data))
	}
	identity := &workrun.PublicationIdentity{GitHubProfile: name, RepositoryID: repositoryIdentity.ID, RepositoryName: repositoryIdentity.Name, GitHubVolume: session.Volume, ProfileID: profile.ID, GitHubID: account.ID, GitHubLogin: account.Login, GitName: values["user.name"], GitEmail: values["user.email"], SSHPublicKey: profile.PublicKey, SSHFingerprint: profile.Fingerprint}
	return identity, identity.Validate()
}

func printRunPlan(output io.Writer, journal workrun.Journal, dry bool) error {
	if dry {
		notice := "Offline plan; authentication and model access have not been checked."
		if journal.Plan.SigningProfile == "" && journal.Plan.PublicationIdentity == nil {
			notice += " GitHub account/key selection is unconfigured; complete sdlc github pair and github use before execution."
		}
		fmt.Fprintln(output, notice)
	}
	return json.NewEncoder(output).Encode(struct {
		ID    string       `json:"run_id,omitempty"`
		State string       `json:"state"`
		Plan  workrun.Plan `json:"plan"`
	}{journal.ID, journal.State, journal.Plan})
}

func printSelectedPlan(output io.Writer, journal workrun.Journal, structured bool) error {
	if !structured {
		return printRunPlan(output, journal, true)
	}
	return json.NewEncoder(output).Encode(struct {
		Version int          `json:"version"`
		Mode    string       `json:"mode"`
		Offline bool         `json:"offline"`
		RunID   string       `json:"run_id,omitempty"`
		Plan    workrun.Plan `json:"plan"`
		Notice  string       `json:"notice"`
	}{1, "ticket", true, journal.ID, journal.Plan, "Offline plan; authentication and model access have not been checked. Content hashes and source capture are established during execution."})
}

func branchPart(value string) string {
	result := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(value), "-")
	result = strings.Trim(result, "-")
	if len(result) > 64 {
		result = strings.TrimRight(result[:64], "-")
	}
	if result == "" {
		return "work"
	}
	return result
}

func inCI() bool {
	for _, key := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "TF_BUILD", "BUILD_BUILDID"} {
		value := os.Getenv(key)
		if value != "" && value != "false" && value != "0" {
			return true
		}
	}
	return false
}

func originRepository(ctx context.Context, root string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "-c", "core.fsmonitor=false", "remote", "get-url", "--all", "origin")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.SplitN(entry, "=", 2)[0], "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	data, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("cannot select origin; supply --repo OWNER/REPO")
	}
	addresses := strings.Fields(string(data))
	if len(addresses) != 1 {
		return "", fmt.Errorf("origin must have one GitHub URL; supply --repo explicitly")
	}
	address := addresses[0]
	path := ""
	colon := strings.IndexByte(address, ':')
	if colon > 0 && address[:colon] == "git"+"@"+"github.com" {
		path = address[colon+1:]
	} else {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Hostname() != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") {
			return "", fmt.Errorf("origin must be a GitHub URL without embedded credentials; supply --repo explicitly")
		}
		path = strings.TrimPrefix(parsed.Path, "/")
	}
	return strings.TrimSuffix(path, ".git"), nil
}

func readHumanAnswer(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return nil, fmt.Errorf("answer must be a regular UTF-8 text file of at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("human answer changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || strings.TrimSpace(string(data)) == "" || len(data) > 64*1024 || !utf8.Valid(data) {
		return nil, fmt.Errorf("cannot read a bounded nonempty UTF-8 human answer")
	}
	return data, nil
}
