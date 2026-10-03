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

	"github.com/tjpeel/sdlc/internal/instructions"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const runUsage = "Usage: sdlc run --reference REFERENCE --ticket NUMBERED_FILE [--provider codex|claude]\n  [--input RELATIVE_PATH] [--base main] [--branch BRANCH] [--repo OWNER/REPO]\n  [--model MODEL] [--effort LEVEL] [--review-model MODEL] [--review-effort LEVEL]\n  [--docker-tests] [--timeout 2h] [--dry-run]\nResume: sdlc run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID [--answer-file FILE]\nThe selected implementation provider must be logged in. Independent review uses the opposite provider when logged in.\n--docker-tests enables a privileged, disposable Docker daemon for integration checks."

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
	reference, ticket, provider, base, branch, repository, model, effort, reviewModel, reviewEffort, resume, answerFile string
	inputs                                                                                                              selectedInputs
	dockerTests, dryRun                                                                                                 bool
	timeout                                                                                                             time.Duration
}

func parseRunOptions(args []string) (runOptions, error) {
	var options runOptions
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.reference, "reference", "", "selected work folder")
	flags.StringVar(&options.ticket, "ticket", "", "exact numbered ticket filename")
	flags.StringVar(&options.provider, "provider", "codex", "implementation provider")
	flags.StringVar(&options.base, "base", "main", "PR base branch")
	flags.StringVar(&options.branch, "branch", "", "destination branch; defaults to a unique ticket branch")
	flags.StringVar(&options.repository, "repo", "", "GitHub owner/repo; defaults to origin")
	flags.StringVar(&options.model, "model", "", "implementation lead model")
	flags.StringVar(&options.effort, "effort", "", "implementation lead reasoning effort")
	flags.StringVar(&options.reviewModel, "review-model", "", "opposite-provider review lead model")
	flags.StringVar(&options.reviewEffort, "review-effort", "", "review lead reasoning effort")
	flags.StringVar(&options.resume, "resume", "", "recorded run ID")
	flags.StringVar(&options.answerFile, "answer-file", "", "human answer to recorded questions")
	flags.Var(&options.inputs, "input", "exact additional requirements input; repeatable")
	flags.BoolVar(&options.dockerTests, "docker-tests", false, "enable privileged Docker integration-test daemon")
	flags.BoolVar(&options.dryRun, "dry-run", false, "print selection without Docker, login checks or execution")
	flags.DurationVar(&options.timeout, "timeout", 2*time.Hour, "maximum duration of this controller invocation")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if options.reference == "" || options.ticket == "" || flags.NArg() != 0 {
		return options, fmt.Errorf("run requires --reference REFERENCE and --ticket NUMBERED_FILE")
	}
	if options.provider != "codex" && options.provider != "claude" {
		return options, fmt.Errorf("provider must be codex or claude")
	}
	if options.timeout < time.Minute || options.timeout > 24*time.Hour {
		return options, fmt.Errorf("timeout must be between 1m and 24h")
	}
	if options.resume != "" {
		invalid := ""
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "reference", "ticket", "resume", "answer-file", "timeout", "dry-run":
			default:
				invalid = f.Name
			}
		})
		if invalid != "" {
			return options, fmt.Errorf("resume preserves recorded settings; cannot change --%s", invalid)
		}
	} else if options.answerFile != "" {
		return options, fmt.Errorf("--answer-file requires --resume")
	}
	return options, nil
}

func runCommand(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseRunOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(output, runUsage)
		return err
	}
	if err != nil {
		return err
	}
	current, err := os.Getwd()
	if err != nil {
		return err
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
	runtime, err := runtimeimage.New(output, io.Discard)
	if err != nil {
		return err
	}
	var journal workrun.Journal
	var directory string
	if options.resume != "" {
		directory, err = workrun.RunDirectory(work.Root, options.reference, ticket, options.resume, false)
		if err != nil {
			return err
		}
		journal, err = workrun.Load(directory)
		if err != nil {
			return err
		}
		if journal.Plan.Root != work.Root || journal.Plan.Reference != options.reference || filepath.Base(journal.Plan.Ticket) != ticket {
			return fmt.Errorf("run journal does not match selected work")
		}
	} else {
		defaults, err := workrun.LoadModels(runtime.Directory)
		if err != nil {
			return err
		}
		roles, err := workrun.ResolveModels(defaults, options.provider, options.model, options.effort, options.reviewModel, options.reviewEffort)
		if err != nil {
			return err
		}
		launch, err := project.Launch(ctx, current, options.reference, ticket, options.inputs)
		if err != nil {
			return err
		}
		repository := options.repository
		if repository == "" {
			repository, err = originRepository(ctx, work.Root)
			if err != nil {
				return err
			}
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(repository) {
			return fmt.Errorf("--repo must name a GitHub OWNER/REPO")
		}
		journal = workrun.Journal{Version: 1, State: "prepared", Plan: workrun.Plan{Root: work.Root, Reference: options.reference, Ticket: launch.Ticket, SourceSHA: launch.Head, Base: options.base, Repository: repository, Roles: roles, Checks: launch.Config.Checks, DockerTests: options.dockerTests}}
		if options.dryRun {
			return printRunPlan(output, journal, true)
		}
		if inCI() {
			return fmt.Errorf("account-authenticated ticket runs are supported here only as local single-user CLI jobs; CI requires a separately supported authentication route")
		}
		provider := providerauth.New(runtime)
		for index, role := range []workrun.Model{roles.Implementation, roles.Review} {
			status, err := provider.Status(ctx, role.Provider)
			if err != nil {
				return err
			}
			if index == 0 && status != "stored" {
				return fmt.Errorf("selected implementer needs login: sdlc auth login --provider %s", role.Provider)
			}
			if index == 1 && status != "stored" {
				fmt.Fprintf(output, "Independent review will wait for %s authentication after delivery.\n", role.Provider)
			}
		}
		id, err := workrun.NewID()
		if err != nil {
			return err
		}
		journal.ID = id
		directory, err = workrun.RunDirectory(work.Root, options.reference, ticket, id, true)
		if err != nil {
			return err
		}
		branch := options.branch
		if branch == "" {
			branch = "work/" + branchPart(options.reference) + "/" + branchPart(strings.TrimSuffix(ticket, ".md")) + "-" + id[:8]
		}
		journal.Workspace = filepath.Join(directory, "workspace")
		plan, err := workrun.Capture(ctx, launch, journal.Workspace, branch, options.base, repository, roles)
		if err != nil {
			return err
		}
		plan.DockerTests = options.dockerTests
		journal.Plan = plan
		shared, err := (instructions.Manager{Directory: runtime.Directory}).Show()
		if err != nil {
			return err
		}
		journal.Instructions = string(shared)
		state, err := runtime.Status(ctx)
		if err != nil {
			return err
		}
		journal.ImageID = state.ImageID
		if err := workrun.Save(directory, &journal); err != nil {
			return err
		}
	}
	if options.dryRun {
		return printRunPlan(output, journal, true)
	}
	if inCI() {
		return fmt.Errorf("account-authenticated ticket runs require a local single-user CLI job")
	}
	if err := printRunPlan(output, journal, false); err != nil {
		return err
	}
	answer := ""
	if options.answerFile != "" {
		data, err := readHumanAnswer(options.answerFile)
		if err != nil {
			return err
		}
		answer = string(data)
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	checker := workrun.DockerChecker{Runtime: runtime, ImageID: journal.ImageID, DockerTests: journal.Plan.DockerTests}
	if len(journal.Plan.CheckInputs) > 0 {
		checker.InputDirectory = filepath.Join(directory, "check-inputs")
	}
	runner := workrun.Runner{Provider: workrun.NativeProvider{Manager: providerauth.New(runtime), ImageID: journal.ImageID}, Checker: checker, Publisher: workrun.GitHubPublisher{}, Repository: workrun.DockerRepository{Runtime: runtime, ImageID: journal.ImageID}, Output: output, Instructions: journal.Instructions, ReviewWorkspace: workrun.PrepareReview}
	registry := runstatus.New(runtime.Directory)
	var tracker *runstatus.Tracker
	runner.OnStart = func(snapshot workrun.Journal) error {
		var err error
		tracker, err = registry.Begin(directory, snapshot)
		return err
	}
	runner.OnState = func(snapshot workrun.Journal) error { return tracker.Update(snapshot) }
	runner.OnOutput = func(data []byte) { tracker.Activity(data) }
	runner.OnNativeOutput = func(data []byte) { tracker.NativeEvent(data) }
	runner.OnFinish = func() error { return tracker.Close() }
	err = runner.Run(ctx, directory, &journal, answer)
	fmt.Fprintf(output, "Private run state: %q\nResume: sdlc run --reference %q --ticket %q --resume %s\n", directory, options.reference, ticket, journal.ID)
	return err
}

func printRunPlan(output io.Writer, journal workrun.Journal, dry bool) error {
	if dry {
		fmt.Fprintln(output, "Offline plan; authentication and model access have not been checked.")
	}
	return json.NewEncoder(output).Encode(struct {
		ID    string       `json:"run_id,omitempty"`
		State string       `json:"state"`
		Plan  workrun.Plan `json:"plan"`
	}{journal.ID, journal.State, journal.Plan})
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
	if err != nil || len(data) == 0 || len(data) > 64*1024 || !utf8.Valid(data) {
		return nil, fmt.Errorf("cannot read a bounded nonempty UTF-8 human answer")
	}
	return data, nil
}
