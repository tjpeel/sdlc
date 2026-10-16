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
	"github.com/tjpeel/sdlc/internal/workseries"
)

const runUsage = "Usage: sdlc run --reference REFERENCE --ticket NUMBERED_FILE [--provider codex|claude]\n  [--github-profile NAME] [--input RELATIVE_PATH] [--base main] [--branch BRANCH] [--repo OWNER/REPO]\n  [--model MODEL] [--reasoning LEVEL] [--effort LEVEL] [--review-model MODEL] [--review-effort LEVEL]\n  [--speed normal|fast] [--review-speed normal|fast] [--auto-accept-recommendations]\n  [--runtime NAME] [--docker-tests] [--timeout DURATION] [--dry-run] [--notify off|desktop|bell] [--sound]\nFeature: sdlc run --reference REFERENCE --all [--parallel 1] [--watch] [--alternate-providers] [--dry-run]\nResume: sdlc run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID [--start-at awaiting-feedback|checks|review|feedback [--human-reviewed] | --override-run-limits | --pr-feedback | --pr-feedback-file FILE | --answer-file FILE | --ci-feedback-file FILE | --initialize-ci-repair-budget | --initialize-review-repair-budget 1..3 | --review-repair-limit 1..12 | --extend-session-turn-budget 1..24]\nThe selected implementation provider must be logged in. Independent review uses the opposite provider when logged in.\n--docker-tests enables a privileged, disposable Docker daemon for integration checks."

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
	autoAcceptRecommendations                                                                                                                                                         bool
	startAt                                                                                                                                                                           string
	humanReviewed                                                                                                                                                                     bool
	adoptURL                                                                                                                                                                          string
	adoptGenerated                                                                                                                                                                    bool
	overrideRunLimits                                                                                                                                                                 bool
	prFeedbackFile                                                                                                                                                                    string
	processPRFeedback                                                                                                                                                                 bool
	extendSessionTurnBudget                                                                                                                                                           int
	runtimeName, headroomMode                                                                                                                                                         string
	frozenHeadroom                                                                                                                                                                    *headroom.Config
	reference, ticket, provider, base, branch, repository, model, effort, reviewModel, reviewEffort, reasoning, speed, reviewSpeed, resume, answerFile, ciFeedbackFile, githubProfile string
	inputs                                                                                                                                                                            selectedInputs
	initializeCIRepairBudget                                                                                                                                                          bool
	initializeReviewRepairBudget                                                                                                                                                      int
	reviewRepairLimit                                                                                                                                                                 int
	dockerTests, dryRun                                                                                                                                                               bool
	timeout                                                                                                                                                                           time.Duration
	notifications                                                                                                                                                                     notify.Options
	all, watch, alternate                                                                                                                                                             bool
	terminal                                                                                                                                                                          bool
	parallel                                                                                                                                                                          int
	jsonOutput                                                                                                                                                                        bool
	terminalMode, launchID                                                                                                                                                            string
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
	frozenDaemonMode                      string
	supplied                              map[string]bool
}

func parseRunOptions(args []string) (runOptions, error) {
	var options runOptions
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.startAt, "start-at", "", "PR entry point: awaiting-feedback, checks, review or feedback")
	flags.BoolVar(&options.humanReviewed, "human-reviewed", false, "declare the captured PR head reviewed by a human")
	flags.StringVar(&options.adoptURL, "pr", "", "adopt an existing same-repository PR by URL")
	flags.StringVar(&options.reference, "reference", "", "selected work folder")
	flags.StringVar(&options.ticket, "ticket", "", "exact numbered ticket filename")
	flags.StringVar(&options.githubProfile, "github-profile", "", "registered GitHub account/key pair; defaults to repository selection")
	flags.StringVar(&options.provider, "provider", "codex", "implementation provider")
	flags.StringVar(&options.runtimeName, "runtime", "", "named runtime for one ticket; resume uses the recorded selection")
	flags.StringVar(&options.headroomMode, "headroom", "off", "legacy resume route; new runs require the saved external Headroom proxy")
	flags.StringVar(&options.base, "base", "main", "PR base branch")
	flags.StringVar(&options.branch, "branch", "", "explicit destination branch; defaults to project branch_format")
	flags.StringVar(&options.repository, "repo", "", "GitHub owner/repo; defaults to saved SDLC identity, then origin")
	flags.StringVar(&options.model, "model", "", "implementation lead model")
	flags.StringVar(&options.reasoning, "reasoning", "", "new-run reasoning for both roles: low, medium, high, xhigh or max; role effort flags override")
	flags.StringVar(&options.speed, "speed", "", "implementation native speed: normal or fast")
	flags.StringVar(&options.reviewSpeed, "review-speed", "", "review native speed: normal or fast")
	flags.StringVar(&options.effort, "effort", "", "implementation lead reasoning effort")
	flags.StringVar(&options.reviewModel, "review-model", "", "opposite-provider review lead model")
	flags.StringVar(&options.reviewEffort, "review-effort", "", "override PR-based Claude review effort")
	flags.StringVar(&options.root, "run-root", "", "internal recorded root for resume")
	flags.StringVar(&options.checkpoint, "checkpoint", "", "internal frozen checkpoint for resume")
	flags.StringVar(&options.resume, "resume", "", "recorded run ID or unique prefix of at least three characters")
	flags.StringVar(&options.prFeedbackFile, "pr-feedback-file", "", "private local PR feedback file")
	flags.BoolVar(&options.autoAcceptRecommendations, "auto-accept-recommendations", false, "automatically accept completed independent-review recommendations")
	flags.BoolVar(&options.processPRFeedback, "pr-feedback", false, "assess new or changed human comments on the saved PR")
	flags.BoolVar(&options.overrideRunLimits, "override-run-limits", false, "persistently lift SDLC numeric run allowances on a saved run")
	flags.IntVar(&options.extendSessionTurnBudget, "extend-session-turn-budget", 0, "one-time additional native session turns for a bound saved run (1-24)")
	flags.IntVar(&options.reviewRepairLimit, "review-repair-limit", 0, "increase a saved review repair allowance to at most 12; preserve all used grants")
	flags.IntVar(&options.initializeReviewRepairBudget, "initialize-review-repair-budget", 0, "one-time legacy completed-review handoff allowance (1-3)")
	flags.BoolVar(&options.initializeCIRepairBudget, "initialize-ci-repair-budget", false, "one-time recovery for a legacy published CI-bound hold")
	flags.StringVar(&options.ciFeedbackFile, "ci-feedback-file", "", "bounded supplemental CI context for the original blocked implementation session")
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
	flags.DurationVar(&options.timeout, "timeout", 0, "optional controller deadline (1m-24h; default: none)")
	flags.StringVar(&options.notifications.Mode, "notify", "off", "local notifications: off, desktop or bell")
	flags.BoolVar(&options.notifications.Sound, "sound", false, "desktop notification sound")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if options.initializeCIRepairBudget && (options.resume == "" || options.all || options.answerFile != "") {
		return options, fmt.Errorf("--initialize-ci-repair-budget requires a single --resume without --answer-file")
	}

	if options.initializeReviewRepairBudget != 0 && (options.initializeReviewRepairBudget < 1 || options.initializeReviewRepairBudget > 3 || options.resume == "" || options.all || options.answerFile != "" || options.initializeCIRepairBudget) {
		return options, fmt.Errorf("--initialize-review-repair-budget requires 1-3 and a single --resume without other recovery flags")
	}
	if options.reasoning != "" {
		switch options.reasoning {
		case "low", "medium", "high", "xhigh", "max":
		default:
			return options, fmt.Errorf("reasoning must be low, medium, high, xhigh or max")
		}
		if options.effort == "" {
			options.effort = options.reasoning
		}
		if options.reviewEffort == "" {
			options.reviewEffort = options.reasoning
		}
	}
	for _, speed := range []string{options.speed, options.reviewSpeed} {
		if speed != "" && speed != "normal" && speed != "fast" {
			return options, fmt.Errorf("speed must be normal or fast")
		}
	}
	options.supplied = map[string]bool{}
	flags.Visit(func(f *flag.Flag) { options.supplied[f.Name] = true })
	if options.supplied["start-at"] || options.supplied["human-reviewed"] {
		if options.startAt == "" || !workrun.ValidPRStartAt(options.startAt) || options.all || options.adoptURL == "" && options.resume == "" {
			return options, fmt.Errorf("--start-at requires an adoption or single resume and awaiting-feedback, checks, review or feedback")
		}
		for _, name := range []string{"pr-feedback", "pr-feedback-file", "answer-file", "ci-feedback-file", "override-run-limits", "initialize-ci-repair-budget", "initialize-review-repair-budget", "extend-session-turn-budget", "review-repair-limit"} {
			if options.supplied[name] {
				return options, fmt.Errorf("--start-at cannot combine with --%s", name)
			}
		}
	}
	if options.supplied["pr-feedback-file"] {
		if options.prFeedbackFile == "" || options.resume == "" || options.all {
			return options, fmt.Errorf("--pr-feedback-file requires a nonempty path and single --resume")
		}
		for _, name := range []string{"pr-feedback", "answer-file", "ci-feedback-file", "override-run-limits", "initialize-ci-repair-budget", "initialize-review-repair-budget", "extend-session-turn-budget", "review-repair-limit"} {
			if options.supplied[name] {
				return options, fmt.Errorf("--pr-feedback-file cannot combine with --%s", name)
			}
		}
		options.processPRFeedback = true
	}
	if options.supplied["review-repair-limit"] && (options.reviewRepairLimit < 1 || options.reviewRepairLimit > 12 || options.resume == "" || options.all || options.answerFile != "" || options.initializeCIRepairBudget || options.initializeReviewRepairBudget != 0 || options.extendSessionTurnBudget != 0 || options.overrideRunLimits || options.processPRFeedback || options.ciFeedbackFile != "") {
		return options, fmt.Errorf("--review-repair-limit requires 1-12 and single --resume without other recovery actions")
	}
	if options.supplied["ci-feedback-file"] {
		if options.ciFeedbackFile == "" || options.resume == "" || options.all {
			return options, fmt.Errorf("--ci-feedback-file requires a single --resume and a nonempty path")
		}
		for _, flag := range []string{"answer-file", "pr-feedback", "override-run-limits", "initialize-ci-repair-budget", "initialize-review-repair-budget", "extend-session-turn-budget"} {
			if options.supplied[flag] {
				return options, fmt.Errorf("--ci-feedback-file cannot be combined with another recovery action")
			}
		}
	}

	if options.supplied["override-run-limits"] && (!options.overrideRunLimits || options.resume == "" || options.all || options.supplied["answer-file"] || options.supplied["pr-feedback"] || options.supplied["initialize-ci-repair-budget"] || options.supplied["initialize-review-repair-budget"] || options.supplied["extend-session-turn-budget"]) {
		return options, fmt.Errorf("--override-run-limits requires a single --resume without an answer, PR feedback initiation or budget recovery action")
	}
	if options.supplied["pr-feedback"] && (!options.processPRFeedback || options.resume == "" || options.all || options.answerFile != "" || options.initializeCIRepairBudget || options.supplied["initialize-review-repair-budget"] || options.supplied["extend-session-turn-budget"]) {
		return options, fmt.Errorf("--pr-feedback requires a single --resume without an answer or budget recovery action")
	}
	if options.supplied["extend-session-turn-budget"] && (options.extendSessionTurnBudget < 1 || options.extendSessionTurnBudget > 24 || options.resume == "" || options.all || options.supplied["answer-file"] || options.supplied["initialize-ci-repair-budget"] || options.supplied["initialize-review-repair-budget"]) {
		return options, fmt.Errorf("--extend-session-turn-budget requires 1-24 and a single --resume without other recovery actions")
	}
	if options.supplied["initialize-review-repair-budget"] && options.initializeReviewRepairBudget == 0 {
		return options, fmt.Errorf("--initialize-review-repair-budget must be 1-3")
	}
	if options.adoptURL != "" {
		_, number, err := workrun.ParsePRURL(options.adoptURL)
		if err != nil {
			return options, err
		}
		for _, name := range []string{"all", "resume", "pr-feedback", "base", "branch", "repo", "run-root", "checkpoint", "terminal", "launch-id", "watch", "parallel", "alternate-providers", "answer-file", "ci-feedback-file", "override-run-limits", "extend-session-turn-budget", "initialize-ci-repair-budget", "initialize-review-repair-budget", "review-repair-limit"} {
			if options.supplied[name] {
				return options, fmt.Errorf("PR adoption cannot combine --pr with --%s", name)
			}
		}
		if (options.reference == "") != (options.ticket == "") {
			return options, fmt.Errorf("adoption ticket context requires both --reference and --ticket")
		}
		if options.reference == "" {
			options.reference = fmt.Sprintf("pr-%d", number)
			options.ticket = "01-pr-comments.md"
			options.adoptGenerated = true
		}
	}
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
	if options.supplied["timeout"] && (options.timeout < time.Minute || options.timeout > 24*time.Hour) {
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
			case "auto-accept-recommendations", "start-at", "human-reviewed", "review-repair-limit", "run-root", "checkpoint", "reference", "ticket", "resume", "answer-file", "ci-feedback-file", "pr-feedback-file", "initialize-ci-repair-budget", "initialize-review-repair-budget", "extend-session-turn-budget", "pr-feedback", "override-run-limits", "timeout", "dry-run", "notify", "sound", "json", "terminal", "launch-id":
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
	if options.adoptURL != "" {
		return adoptSelectedCommand(ctx, options, output)
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
	var cancel context.CancelFunc
	if options.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, options.timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
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
	if err := workseries.CheckDependencyAmendment(work.Root, options.reference); err != nil {
		return err
	}
	var featurePreparationOwner *os.File
	if !options.featureOwned && options.resume == "" && !options.dryRun {
		featurePreparationOwner, err = workseries.OwnTicketPreparation(work.Root, options.reference)
		if err != nil {
			return err
		}
		defer func() {
			if featurePreparationOwner != nil {
				featurePreparationOwner.Close()
			}
		}()
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
		if options.overrideRunLimits {
			if err := workrun.ValidateRunLimitOverride(journal); err != nil {
				return err
			}
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
			if err == nil {
				roles, err = workrun.ResolveSpeeds(roles, options.speed, options.reviewSpeed)
			}
			if err != nil {
				return err
			}
		}
		if options.supplied["auto-accept-recommendations"] {
			roles.AutoAcceptRecommendations = options.autoAcceptRecommendations
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
		if !options.dryRun && options.adoptURL == "" {
			if err := workrun.ValidateSourceHistory(ctx, launch.Root); err != nil {
				return err
			}
		}
		if !options.dryRun && len(launch.Config.Checks) == 0 && launch.Config.CheckSelection != "runtime" {
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
		branch := options.branch
		if branch == "" {
			branch, err = project.BranchName(ctx, work.Root, options.reference, ticket, launch.Config.BranchFormat)
		} else {
			err = project.ValidateBranchName(ctx, work.Root, branch)
		}
		if err != nil {
			return err
		}
		journal = workrun.Journal{Version: 1, State: "prepared", Plan: workrun.Plan{RuntimeName: options.runtimeName, GitHubProfile: options.githubProfile, SigningProfile: pair.SigningProfile, Root: work.Root, Reference: options.reference, Ticket: launch.Ticket, SourceSHA: launch.Head, Branch: branch, Base: options.base, Repository: repository, Roles: roles, Checks: launch.Config.Checks, DockerTests: options.dockerTests}}
		budget, err := project.ResolveNugetCacheBudget(launch.Config.NugetCache)
		if err != nil {
			return err
		}
		journal.Plan.NugetCache = &budget
		journal.Plan.Headroom, err = selectRunHeadroom(runtime.Directory, options)
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
		journal.Workspace = filepath.Join(directory, "workspace")
		var plan workrun.Plan
		if options.adoptURL != "" {
			state, statusErr := runtime.Status(ctx)
			if statusErr != nil {
				return statusErr
			}
			identity, identityErr := freezePublicationIdentity(ctx, runtime, work.Root, repository, pair)
			if identityErr != nil {
				return identityErr
			}
			seed := journal.Plan
			seed.PublicationIdentity = identity
			seed.Repository = identity.RepositoryName
			auth := githubauth.New(runtime)
			auth.Profile = pair.GitHubProfile
			publisher := workrun.DockerPublisher{Runtime: runtime, ImageID: state.ImageID, Auth: auth}
			_, number, _ := workrun.ParsePRURL(options.adoptURL)
			adoption, snapshot, adoptErr := publisher.Adopt(ctx, seed, number, directory)
			if adoptErr != nil {
				return adoptErr
			}
			plan, err = workrun.CaptureAdoption(ctx, launch, snapshot, adoption, journal.Workspace, seed.Repository, roles)
			journal.Publication = workrun.Publication{URL: adoption.URL, Number: adoption.Number, BaseSHA: adoption.BaseSHA, HeadSHA: adoption.HeadSHA}
			journal.State = "adopting"
			journal.CIRepairBudget = &workrun.CIRepairBudget{}
			journal.ReviewRepairBudget = &workrun.ReviewRepairBudget{Limit: 12, DefaultLimit: true}
		} else if options.snapshot == nil {
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
		journal.Plan.RuntimeProvenance = workrun.CaptureRuntimeProvenance(state)
		if err := journal.Plan.Headroom.Validate(); err != nil {
			return err
		}
		if options.frozenImage != "" && state.ImageID != options.frozenImage {
			return fmt.Errorf("runtime changed during the feature; restore the recorded runtime")
		}
		if options.frozenSigningImage != "" {
			journal.Plan.SigningImage, journal.Plan.DaemonImage = options.frozenSigningImage, options.frozenDaemonImage
			journal.Plan.DaemonMode = options.frozenDaemonMode
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
		if featurePreparationOwner != nil {
			if err := featurePreparationOwner.Close(); err != nil {
				return err
			}
			featurePreparationOwner = nil
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
	ciFeedback := ""
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
			if workrun.DependencyIsInvalid(directory, &journal) {
				if err := workrun.StoreDeferredAnswer(directory, &journal, answer); err != nil {
					return err
				}
				fmt.Fprintln(output, "Human answer saved. Resume the owning feature to reconcile the dependency base before native work continues.")
				return nil
			}
		} else if options.checkpoint != "" && journal.State == "waiting_for_human" {
			return fmt.Errorf("answer the recorded questions with sdlc answer --run %s", journal.ID)
		}
	}
	if options.ciFeedbackFile != "" {
		probe, err := probeRunController(directory)
		if err != nil {
			return err
		}
		if probe != nil {
			if err := probe.Close(); err != nil {
				return err
			}
		}
		data, err := readHumanAnswer(options.ciFeedbackFile)
		if err != nil {
			return err
		}
		ciFeedback = string(data)
		if err := workrun.ValidateCIFeedbackResume(journal, ciFeedback); err != nil {
			return err
		}
	}
	if options.startAt != "" && options.resume != "" {
		if err := workrun.ValidatePRCheckpointResume(journal); err != nil {
			return err
		}
	}
	localPRFeedback := ""
	if options.prFeedbackFile != "" {
		data, err := readHumanAnswer(options.prFeedbackFile)
		if err != nil {
			return err
		}
		localPRFeedback = string(data)
		if err := workrun.ValidateLocalPRFeedback(localPRFeedback); err != nil {
			return err
		}
	}
	if options.processPRFeedback {
		if err := workrun.ValidatePRFeedbackResume(journal); err != nil {
			return err
		}
	}
	if options.extendSessionTurnBudget != 0 {
		if err := workrun.ValidateSessionTurnExtension(journal, options.extendSessionTurnBudget); err != nil {
			return err
		}
	}
	if options.reviewRepairLimit != 0 {
		if err := workrun.ValidateReviewRepairIncrease(journal, options.reviewRepairLimit); err != nil {
			return err
		}
	}
	if options.initializeReviewRepairBudget != 0 {
		if err := workrun.ValidateReviewRepairInitialization(journal, options.initializeReviewRepairBudget); err != nil {
			return err
		}
	}
	if options.initializeCIRepairBudget {
		if err := workrun.ValidateCIRepairInitialization(journal); err != nil {
			return err
		}
	}

	if options.overrideRunLimits && journal.State == "ready" {
		view := runstatus.View{ID: journal.ID, Root: journal.Plan.Root, Reference: journal.Plan.Reference, Ticket: journal.Plan.Ticket, Directory: directory, UpdatedAt: journal.UpdatedAt}
		release, err := lockInputRun(ctx, view, !options.dryRun)
		if err != nil {
			return err
		}
		defer release()
		current, err := workrun.Load(directory)
		if err != nil {
			return err
		}
		if current.ID != journal.ID || current.Plan.Root != journal.Plan.Root || current.Plan.Reference != journal.Plan.Reference || current.Plan.Ticket != journal.Plan.Ticket || !current.UpdatedAt.Equal(journal.UpdatedAt) {
			return fmt.Errorf("run checkpoint changed; select the run again")
		}
		if options.dryRun {
			if err := workrun.ValidateStableRunLimitOverride(ctx, directory, current); err != nil {
				return err
			}
			return printRunLimitOverridePlan(output, current, options.jsonOutput, true, ctx, "", false, 0)
		}
		previous := current.UpdatedAt
		if err := workrun.GrantStableRunLimitOverride(ctx, directory, &current); err != nil {
			return err
		}
		if !current.UpdatedAt.Equal(previous) {
			if err := runstatus.New(runtime.Directory).Register(directory, current); err != nil {
				return err
			}
		}
		fmt.Fprintf(output, "Run %s: numerical allowances overridden at its saved ready checkpoint. No work started.\n", current.ID)
		return nil
	}
	if options.dryRun && options.startAt != "" {
		return json.NewEncoder(output).Encode(map[string]any{"action": "pr_checkpoint", "start_at": options.startAt, "human_reviewed": options.humanReviewed, "run": journal.ID, "head": journal.Publication.HeadSHA, "offline": true, "remote_verified": false})
	}
	if options.dryRun {
		if options.resume != "" {
			if options.reviewRepairLimit != 0 {
				return printReviewRepairIncreasePlan(output, journal, options.reviewRepairLimit)
			}
			if ciFeedback != "" {
				return printCIFeedbackResumePlan(output, journal, ciFeedback, options.jsonOutput)
			}
			if options.overrideRunLimits || journal.RunLimitOverride != nil {
				return printRunLimitOverridePlan(output, journal, options.jsonOutput, options.overrideRunLimits, ctx, answer, options.processPRFeedback, options.extendSessionTurnBudget)
			}
			if options.processPRFeedback {
				return printPRFeedbackResumePlan(output, journal, options.jsonOutput)
			}
			return printResumePlan(ctx, output, journal, answer, options.jsonOutput, options.extendSessionTurnBudget)
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
	checker := workrun.DockerChecker{Runtime: runtime, ImageID: journal.ImageID, DockerTests: journal.Plan.DockerTests, DaemonImage: journal.Plan.DaemonImage, DaemonMode: journal.Plan.DaemonMode, UseDefaultDaemonImage: journal.Plan.DaemonImage == "", NugetCache: journal.Plan.NugetCache}
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
	publisher := workrun.DockerPublisher{Runtime: runtime, ImageID: journal.ImageID, Auth: githubManager, CheckInputDirectory: filepath.Join(directory, "check-inputs"), SigningKey: resolver.Resolve, ValidatePair: func() error { _, err := runSigningProfile(runtime, journal.Plan); return err }}
	if err := workrun.ValidateDependencyContext(ctx, directory, journal.Plan, publisher); err != nil {
		if answer != "" && errors.Is(err, workrun.ErrDependencyInvalid) {
			if err := workrun.StoreDeferredAnswer(directory, &journal, answer); err != nil {
				return err
			}
			fmt.Fprintln(output, "Human answer saved. Resume the owning feature to reconcile the dependency base before native work continues.")
			return nil
		}
		return err
	}
	var recommendationPolicy *bool
	if options.supplied["auto-accept-recommendations"] {
		recommendationPolicy = &options.autoAcceptRecommendations
	}
	runner := workrun.Runner{AutoAcceptRecommendationsOverride: recommendationPolicy, ValidateDependencies: func(ctx context.Context, directory string, plan workrun.Plan) error {
		return workrun.ValidateDependencyContext(ctx, directory, plan, publisher)
	}, StartAt: options.startAt, HumanReviewed: options.humanReviewed, CIFeedback: ciFeedback, OverrideRunLimits: options.overrideRunLimits, ProcessPRFeedback: options.processPRFeedback, LocalPRFeedback: localPRFeedback, ExtendSessionTurnBudget: options.extendSessionTurnBudget, InitializeReviewRepairBudget: options.initializeReviewRepairBudget, ReviewRepairLimit: options.reviewRepairLimit, InitializeCIRepairBudget: options.initializeCIRepairBudget, Provider: workrun.NativeProvider{Manager: manager, ImageID: journal.ImageID}, Checker: checker, Publisher: publisher, CompanionCloner: publisher, Repository: workrun.DockerRepository{Runtime: runtime, ImageID: journal.ImageID}, Output: io.Discard, ProgressOutput: output, Instructions: journal.Instructions, ReviewWorkspace: workrun.PrepareReview}
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
	startingAttempt := journal.Attempt
	err = runner.Run(ctx, directory, &journal, answer)
	printPRCheckpointResult(output, journal)
	printCompletedRunUsage(output, directory, journal, startingAttempt)
	if errors.Is(err, workrun.ErrStopped) || journal.State == "waiting_for_human" || journal.State == "blocked" || journal.State == "failed" {
		if reportErr := printRunAttention(output, journal); reportErr != nil {
			err = errors.Join(err, reportErr)
		}
	}
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
	if plan.DockerTests && daemonImage != "" {
		plan.DaemonMode = workrun.SharedTestDaemon
	}
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
		selected.DaemonMode = workrun.SharedTestDaemon
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

func printReviewRepairIncreasePlan(output io.Writer, j workrun.Journal, limit int) error {
	return json.NewEncoder(output).Encode(struct {
		Mode           string    `json:"mode"`
		Offline        bool      `json:"offline"`
		RunID          string    `json:"run_id"`
		Checkpoint     time.Time `json:"checkpoint_updated_at"`
		OriginalLimit  int       `json:"original_review_repair_limit"`
		RequestedLimit int       `json:"requested_review_repair_limit"`
		Used           int       `json:"used_review_repairs"`
		Attempt        int       `json:"attempt"`
		Rounds         int       `json:"rounds"`
	}{"resume", true, j.ID, j.UpdatedAt, j.ReviewRepairBudget.Limit, limit, j.ReviewRepairBudget.Used, j.Attempt, j.Rounds})
}
