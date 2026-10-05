package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/instructions"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

// Settings are private controller state. They contain metadata and hashes, never
// credential contents, and are frozen once for all of a feature's ticket runs.
type seriesSettings struct {
	Version                   int
	Provider                  string
	Alternate                 bool
	Repository                string
	GitHubProfile             string
	SigningProfile            string
	ImageID                   string
	Identity                  *workrun.PublicationIdentity
	Roles                     map[string]workrun.Roles
	Inputs                    []string
	InputHashes               map[string]string
	Config                    project.Config
	DockerTests               bool
	Instructions              string
	SigningImage, DaemonImage string
}

type seriesWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Native event files keep their original bytes. Host output gets a ticket label
// and complete lines so concurrent sessions cannot splice JSON fragments.
type seriesTicketWriter struct {
	mu      sync.Mutex
	output  io.Writer
	ticket  string
	pending []byte
}

func (w *seriesTicketWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	for len(w.pending) > 0 {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 && len(w.pending) < 64*1024 {
			break
		}
		if end < 0 {
			end = 64*1024 - 1
		}
		line := append([]byte("["+w.ticket+"] "), w.pending[:end+1]...)
		if line[len(line)-1] != '\n' {
			line = append(line, '\n')
		}
		if _, err := w.output.Write(line); err != nil {
			return 0, err
		}
		w.pending = w.pending[end+1:]
	}
	return len(data), nil
}

func (w *seriesTicketWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		_, _ = fmt.Fprintf(w.output, "[%s] %s\n", w.ticket, w.pending)
		w.pending = nil
	}
}

func (w *seriesWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}

func sameSeriesConfig(a, b project.Config) bool { return reflect.DeepEqual(a, b) }

func runSeriesCommand(ctx context.Context, options runOptions, output io.Writer) error {
	current, err := os.Getwd()
	if err != nil {
		return err
	}
	work, err := project.InspectWork(ctx, current, options.reference)
	if err != nil {
		return err
	}
	var state workseries.State
	directory, directoryErr := workseries.Directory(work.Root, options.reference, false)
	if directoryErr == nil {
		if _, err := os.Lstat(filepath.Join(directory, "journal.json")); err == nil {
			state, err = workseries.Load(directory)
			if err != nil {
				return err
			}
			if !options.supplied["base"] {
				options.base = state.Plan.Base
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else {
		// Only an absent series directory is a new feature. Never hide a
		// symlink or damaged private directory as a missing checkpoint.
		if _, err := os.Lstat(filepath.Join(work.Root, ".sdlc", "work", options.reference, "series")); err == nil || !os.IsNotExist(err) {
			return directoryErr
		}
	}
	plan, err := workseries.Discover(ctx, work.Root, options.reference, options.base)
	if err != nil {
		return err
	}
	if _, err := seriesGit(ctx, plan.Root, "check-ref-format", "--branch", plan.Base); err != nil {
		return fmt.Errorf("feature base must be a valid branch name")
	}
	if state.Version != 0 && (state.Plan.Root != plan.Root || state.Plan.Reference != plan.Reference || state.Plan.DefinitionSHA != plan.DefinitionSHA) {
		return fmt.Errorf("feature plan changed; restore the saved plan or use a new work reference")
	}
	workseries.PrintPlan(output, plan)
	if options.dryRun {
		fmt.Fprintf(output, "Maximum concurrent tickets: %d; watch human merges: %t.\n", options.parallel, options.watch)
		for _, ticket := range plan.Tickets {
			if previous, ok := state.Results[ticket.File]; ok {
				fmt.Fprintf(output, "Saved ticket %s: %s\n", ticket.File, previous.State)
			}
		}
		fmt.Fprintln(output, "Offline feature plan only; ticket content, account access and live PR state are checked during execution.")
		return nil
	}
	if inCI() {
		return fmt.Errorf("account-authenticated features require a local single-user CLI job")
	}
	if state.Version == 0 {
		state = workseries.State{Version: 1, Plan: plan, Results: map[string]workseries.Result{}}
	}
	directory, err = workseries.Directory(work.Root, options.reference, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	writer := &seriesWriter{w: output}
	options.terminal = dashboardTerminal(output)
	runtime, err := runtimeimage.New(writer, io.Discard)
	if err != nil {
		return err
	}
	lease, err := leaseRuntimeRun(ctx, runtime.Directory, writer)
	if err != nil {
		return err
	}
	defer lease.Close()
	driver := &seriesDriver{runtime: runtime, options: options, directory: directory, output: writer, plan: plan}
	runner := workseries.Runner{Driver: driver, Output: writer, Parallel: options.parallel, Watch: options.watch, PollInterval: 15 * time.Second}
	runner.Initialize = func(ctx context.Context, checkpoint *workseries.State) error {
		if checkpoint.Plan.DefinitionSHA != plan.DefinitionSHA || checkpoint.Plan.Root != plan.Root {
			return fmt.Errorf("feature checkpoint changed while acquiring ownership")
		}
		if len(checkpoint.Settings) == 0 {
			settings, err := freezeSeriesSettings(ctx, runtime, plan, options)
			if err != nil {
				return err
			}
			driver.settings = settings
			checkpoint.Settings, err = json.Marshal(settings)
			if err != nil {
				return err
			}
			base, err := driver.Base(ctx, plan.Base)
			if err != nil {
				return err
			}
			if err := requireSeriesBaseline(ctx, plan.Root, base.SHA, settings); err != nil {
				return err
			}
		} else {
			settings, err := loadSeriesSettings(checkpoint.Settings)
			if err != nil {
				return err
			}
			if err := checkSeriesOptions(options, settings); err != nil {
				return err
			}
			driver.settings = settings
			if err := driver.validateSettings(); err != nil {
				return err
			}
			if err := checkSeriesRuntime(ctx, runtime, settings.ImageID); err != nil {
				return err
			}
		}
		if err := driver.checkInputs(); err != nil {
			return err
		}
		return driver.adoptRuns(checkpoint)
	}
	err = runner.Run(ctx, directory, &state)
	fmt.Fprintf(output, "Private feature state: %q\nResume feature: sdlc run --reference %q --all --parallel %d\n", directory, options.reference, options.parallel)
	for _, ticket := range plan.Tickets {
		result := state.Results[ticket.File]
		status := result.State
		if status == "" {
			status = "waiting for dependencies"
		}
		fmt.Fprintf(output, "  %s: %s %s\n", ticket.File, status, result.URL)
	}
	return err
}

func freezeSeriesSettings(ctx context.Context, runtime runtimeimage.Manager, plan workseries.Plan, options runOptions) (seriesSettings, error) {
	var settings seriesSettings
	state, err := runtime.Status(ctx)
	if err != nil {
		return settings, err
	}
	if err := checkSeriesRuntime(ctx, runtime, state.ImageID); err != nil {
		return settings, err
	}
	repository, err := resolveRepository(ctx, runtime.Directory, plan.Root, options.repository)
	if err != nil {
		return settings, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(repository) {
		return settings, fmt.Errorf("feature requires a GitHub OWNER/REPO")
	}
	pair, err := githubprofile.Select(runtime.Directory, plan.Root, repository, options.githubProfile)
	if err != nil {
		return settings, err
	}
	identity, err := freezePublicationIdentity(ctx, runtime, plan.Root, repository, pair)
	if err != nil {
		return settings, err
	}
	launch, err := project.Launch(ctx, plan.Root, plan.Reference, plan.Tickets[0].File, options.inputs)
	if err != nil {
		return settings, err
	}
	models, err := workrun.LoadModels(runtime.Directory)
	if err != nil {
		return settings, err
	}
	settings = seriesSettings{Version: 1, Provider: options.provider, Alternate: options.alternate, Repository: identity.RepositoryName, GitHubProfile: pair.GitHubProfile, SigningProfile: pair.SigningProfile, ImageID: state.ImageID, Identity: identity, Roles: map[string]workrun.Roles{}, Inputs: options.inputs, Config: launch.Config, DockerTests: options.dockerTests, InputHashes: map[string]string{}}
	shared, err := (instructions.Manager{Directory: runtime.Directory}).Show()
	if err != nil {
		return seriesSettings{}, err
	}
	settings.Instructions = string(shared)
	sidecars := workrun.Plan{DockerTests: options.dockerTests}
	if err := freezeRuntimeImages(ctx, &sidecars, state, runtime, runtimeupdates.ResolveDefaultDaemonImage); err != nil {
		return seriesSettings{}, err
	}
	settings.SigningImage, settings.DaemonImage = sidecars.SigningImage, sidecars.DaemonImage
	for i, ticket := range plan.Tickets {
		provider := options.provider
		if options.alternate && i%2 == 1 {
			if provider == "codex" {
				provider = "claude"
			} else {
				provider = "codex"
			}
		}
		roles, err := workrun.ResolveModels(models, provider, options.model, options.effort, options.reviewModel, options.reviewEffort)
		if err != nil {
			return seriesSettings{}, err
		}
		settings.Roles[ticket.File] = roles
	}
	paths := append([]string{project.ConfigPath}, options.inputs...)
	paths = append(paths, launch.Config.InputFiles...)
	for _, ticket := range plan.Tickets {
		paths = append(paths, ".sdlc/work/"+plan.Reference+"/tickets/"+ticket.File)
	}
	for _, path := range paths {
		hash, err := seriesInputHash(plan.Root, path)
		if err != nil {
			return seriesSettings{}, err
		}
		settings.InputHashes[path] = hash
	}
	return settings, nil
}

func checkSeriesRuntime(ctx context.Context, runtime runtimeimage.Manager, image string) error {
	state, err := runtime.Status(ctx)
	if err != nil || state.ImageID != image {
		return fmt.Errorf("feature requires its recorded runtime; restore that image before resuming")
	}
	data, err := runtime.Docker.Output(ctx, "run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "32", "--memory", "128m", "--log-driver", "none", "--entrypoint", "/usr/local/bin/sdlc-publisher", image, "--capabilities")
	var capabilities struct {
		Version int
		Actions []string
	}
	if err != nil || json.Unmarshal(data, &capabilities) != nil || capabilities.Version != 2 {
		return fmt.Errorf("runtime lacks feature orchestration support; rebuild with sdlc runtime build")
	}
	for _, required := range []string{"branch", "snapshot", "observe", "restack"} {
		found := false
		for _, action := range capabilities.Actions {
			found = found || action == required
		}
		if !found {
			return fmt.Errorf("runtime lacks %s support; rebuild with sdlc runtime build", required)
		}
	}
	return nil
}

func loadSeriesSettings(data []byte) (seriesSettings, error) {
	var settings seriesSettings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&settings) != nil || decoder.Decode(new(any)) != io.EOF || settings.Version != 1 || settings.Identity == nil || settings.Identity.Validate() != nil || settings.ImageID == "" || len(settings.Roles) == 0 || len(settings.InputHashes) == 0 || settings.Repository != settings.Identity.RepositoryName || settings.GitHubProfile != settings.Identity.GitHubProfile {
		return settings, fmt.Errorf("invalid private feature settings")
	}
	for _, roles := range settings.Roles {
		if workrun.ValidateModel(roles.Implementation) != nil || workrun.ValidateModel(roles.Review) != nil || roles.Implementation.Provider == roles.Review.Provider {
			return settings, fmt.Errorf("invalid private feature models")
		}
	}
	if settings.SigningImage == "" || (settings.DockerTests && settings.DaemonImage == "") || (workrun.Plan{SigningImage: settings.SigningImage, DaemonImage: settings.DaemonImage}).ValidateSidecarImages() != nil {
		return settings, fmt.Errorf("invalid private feature sidecar images")
	}
	return settings, nil
}

func checkSeriesOptions(options runOptions, settings seriesSettings) error {
	values := map[string]bool{
		"provider": options.provider == settings.Provider, "alternate-providers": options.alternate == settings.Alternate,
		"repo": strings.EqualFold(options.repository, settings.Repository), "github-profile": options.githubProfile == settings.GitHubProfile,
		"input": reflect.DeepEqual([]string(options.inputs), settings.Inputs), "docker-tests": options.dockerTests == settings.DockerTests,
	}
	for flag, matches := range values {
		if options.supplied[flag] && !matches {
			return fmt.Errorf("feature resume preserves recorded --%s", flag)
		}
	}
	for _, roles := range settings.Roles {
		if options.supplied["model"] && options.model != roles.Implementation.Name || options.supplied["effort"] && options.effort != roles.Implementation.Effort || options.supplied["review-model"] && options.reviewModel != roles.Review.Name || options.supplied["review-effort"] && options.reviewEffort != roles.Review.Effort {
			return fmt.Errorf("feature resume preserves recorded models and reasoning")
		}
	}
	return nil
}

// This helper is used only for host source or newly created controller metadata,
// never for a repository which a provider has been allowed to modify.
func seriesGit(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "core.pager=cat"}, args...)...)
	command.Dir = directory
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1"}
	data, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("cannot inspect feature source with Git")
	}
	return string(data), nil
}

func requireSeriesBaseline(ctx context.Context, root, baseSHA string, settings seriesSettings) error {
	head, err := seriesGit(ctx, root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != baseSHA {
		return fmt.Errorf("new features start from the current published integration revision; update your checkout first")
	}
	status, err := seriesGit(ctx, root, "diff", "HEAD", "--name-only", "--no-ext-diff", "--no-textconv", "--")
	if err != nil || status != "" {
		return fmt.Errorf("commit project changes before starting a feature")
	}
	others, err := seriesGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	allowed := map[string]bool{project.ConfigPath: true}
	for path := range settings.InputHashes {
		allowed[path] = true
	}
	for _, path := range strings.Split(others, "\x00") {
		if path != "" && !allowed[path] {
			return fmt.Errorf("commit untracked project source before starting a feature")
		}
	}
	return nil
}

func seriesInputHash(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00\r\n") {
		return "", fmt.Errorf("invalid feature input path")
	}
	path := root
	parts := strings.Split(relative, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("invalid feature input path")
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("feature inputs must remain regular files without filesystem links")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	before, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(opened, before) || !opened.Mode().IsRegular() {
		return "", fmt.Errorf("feature input changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 16*1024*1024+1))
	if err != nil || n > 16*1024*1024 {
		return "", fmt.Errorf("feature input exceeds the run input limit")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || after.Size() != n || !opened.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("feature input changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type seriesDriver struct {
	runtime   runtimeimage.Manager
	options   runOptions
	settings  seriesSettings
	plan      workseries.Plan
	directory string
	output    io.Writer
}

func (d *seriesDriver) validateSettings() error {
	if len(d.settings.Roles) != len(d.plan.Tickets) || (d.settings.Provider != "codex" && d.settings.Provider != "claude") {
		return fmt.Errorf("private feature models do not match its tickets")
	}
	expected := map[string]bool{project.ConfigPath: true}
	for _, path := range append(append([]string{}, d.settings.Inputs...), d.settings.Config.InputFiles...) {
		expected[path] = true
	}
	for _, ticket := range d.plan.Tickets {
		if _, ok := d.settings.Roles[ticket.File]; !ok {
			return fmt.Errorf("private feature lacks a recorded ticket model")
		}
		expected[".sdlc/work/"+d.plan.Reference+"/tickets/"+ticket.File] = true
	}
	if len(expected) != len(d.settings.InputHashes) {
		return fmt.Errorf("private feature requirement set changed")
	}
	for path := range expected {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(d.settings.InputHashes[path]) {
			return fmt.Errorf("private feature lacks a valid requirement hash")
		}
	}
	return nil
}

func (d *seriesDriver) checkInputs() error {
	for path, expected := range d.settings.InputHashes {
		actual, err := seriesInputHash(d.plan.Root, path)
		if err != nil || actual != expected {
			return fmt.Errorf("feature requirement or check input changed; restore frozen inputs before resuming")
		}
	}
	return nil
}

func (d *seriesDriver) publicationPlan() workrun.Plan {
	return workrun.Plan{Root: d.plan.Root, Reference: d.plan.Reference, Repository: d.settings.Repository, GitHubProfile: d.settings.GitHubProfile, SigningProfile: d.settings.SigningProfile, PublicationIdentity: d.settings.Identity}
}

func (d *seriesDriver) publisher(plan workrun.Plan) workrun.DockerPublisher {
	manager := githubauth.New(d.runtime)
	manager.Profile = d.settings.GitHubProfile
	return workrun.DockerPublisher{Runtime: d.runtime, ImageID: d.settings.ImageID, Auth: manager, ValidatePair: func() error { _, err := runSigningProfile(d.runtime, plan); return err }}
}

func (d *seriesDriver) Base(ctx context.Context, branch string) (workseries.Target, error) {
	remote, err := d.publisher(d.publicationPlan()).Branch(ctx, d.publicationPlan(), branch)
	return workseries.Target{Base: remote.Branch, SHA: remote.SHA}, err
}

func (d *seriesDriver) snapshot(ctx context.Context, target workseries.Target, parent string) (workrun.BranchSnapshot, func(), error) {
	request, err := os.MkdirTemp(parent, ".feature-source-")
	if err != nil {
		return workrun.BranchSnapshot{}, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(request) }
	snapshot, err := d.publisher(d.publicationPlan()).Snapshot(ctx, d.publicationPlan(), target.Base, target.SHA, request)
	if err == nil {
		err = checkSeriesAncestry(ctx, snapshot, target.Ancestors, request)
	}
	if err != nil {
		cleanup()
		return workrun.BranchSnapshot{}, func() {}, err
	}
	return snapshot, cleanup, nil
}

func checkSeriesAncestry(ctx context.Context, snapshot workrun.BranchSnapshot, ancestors []string, parent string) error {
	if len(ancestors) == 0 {
		return nil
	}
	private, err := os.MkdirTemp(parent, ".ancestry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	if _, err := seriesGit(ctx, private, "init", "-b", "baseline"); err != nil {
		return err
	}
	if _, err := workrun.SafeGit(ctx, private, "fetch", "--no-tags", snapshot.Bundle, "refs/sdlc/snapshot"); err != nil {
		return err
	}
	for _, sha := range ancestors {
		if !regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(sha) {
			return fmt.Errorf("invalid merged dependency revision")
		}
		if _, err := workrun.SafeGit(ctx, private, "merge-base", "--is-ancestor", sha, snapshot.SHA); err != nil {
			return fmt.Errorf("integration branch no longer includes a merged dependency")
		}
	}
	return nil
}

func (d *seriesDriver) loadRun(ticket string, result workseries.Result) (string, workrun.Journal, error) {
	directory, err := workrun.RunDirectory(d.plan.Root, d.plan.Reference, ticket, result.RunID, false)
	if err != nil {
		return "", workrun.Journal{}, err
	}
	if result.Directory != "" && result.Directory != directory {
		return "", workrun.Journal{}, fmt.Errorf("feature run directory changed")
	}
	journal, err := workrun.Load(directory)
	if err != nil {
		return "", journal, err
	}
	roles, exists := d.settings.Roles[ticket]
	if !exists || journal.Plan.Root != d.plan.Root || journal.Plan.Reference != d.plan.Reference || filepath.Base(journal.Plan.Ticket) != ticket || journal.ImageID != d.settings.ImageID || journal.Plan.PublicationIdentity == nil || *journal.Plan.PublicationIdentity != *d.settings.Identity || journal.Plan.Roles != roles || journal.Plan.Repository != d.settings.Repository || journal.Plan.GitHubProfile != d.settings.GitHubProfile || journal.Plan.SigningProfile != d.settings.SigningProfile || journal.Plan.SigningImage != d.settings.SigningImage || journal.Plan.DaemonImage != d.settings.DaemonImage || journal.Instructions != d.settings.Instructions || !reflect.DeepEqual(journal.Plan.Checks, d.settings.Config.Checks) || journal.Plan.DockerTests != d.settings.DockerTests {
		return "", journal, fmt.Errorf("saved ticket run differs from frozen feature settings")
	}
	for _, input := range append(append([]workrun.Input{}, journal.Plan.Inputs...), journal.Plan.CheckInputs...) {
		if expected, ok := d.settings.InputHashes[input.Path]; !ok || input.SHA256 != expected {
			return "", journal, fmt.Errorf("saved ticket requirements differ from the feature")
		}
	}
	expectedInputs := append([]string{".sdlc/work/" + d.plan.Reference + "/tickets/" + ticket}, d.settings.Inputs...)
	if !sameSeriesInputPaths(journal.Plan.Inputs, expectedInputs) || !sameSeriesInputPaths(journal.Plan.CheckInputs, d.settings.Config.InputFiles) {
		return "", journal, fmt.Errorf("saved ticket input selection differs from the feature")
	}
	return directory, journal, nil
}

func matchSeriesCapturedInputs(plan workrun.Plan, hashes map[string]string) error {
	for _, input := range append(append([]workrun.Input{}, plan.Inputs...), plan.CheckInputs...) {
		if expected, ok := hashes[input.Path]; !ok || expected != input.SHA256 {
			return fmt.Errorf("feature input changed during source capture; restore frozen requirements before execution")
		}
	}
	return nil
}

func sameSeriesInputPaths(inputs []workrun.Input, paths []string) bool {
	expected := map[string]bool{}
	for _, path := range paths {
		expected[path] = true
	}
	if len(inputs) != len(expected) {
		return false
	}
	for _, input := range inputs {
		if !expected[input.Path] {
			return false
		}
		delete(expected, input.Path)
	}
	return len(expected) == 0
}

func resultForRun(directory string, journal workrun.Journal) workseries.Result {
	return workseries.Result{RunID: journal.ID, Directory: directory, State: journal.State, Branch: journal.Plan.Branch, Base: journal.Plan.Base, BaseSHA: journal.Plan.BaseSHA, HeadSHA: journal.Publication.HeadSHA, URL: journal.Publication.URL, PRNumber: journal.Publication.Number, StopReason: journal.StopReason}
}

func (d *seriesDriver) Execute(ctx context.Context, ticket workseries.Ticket, target workseries.Target, previous *workseries.Result) (workseries.Result, error) {
	if previous == nil || previous.RunID == "" {
		return workseries.Result{}, fmt.Errorf("feature must checkpoint a run identity before launching")
	}
	if err := d.checkInputs(); err != nil {
		return *previous, err
	}
	options := d.options
	options.all, options.watch, options.alternate = false, false, false
	options.root, options.reference, options.ticket, options.runID = d.plan.Root, d.plan.Reference, ticket.File, previous.RunID
	options.base, options.branch, options.repository, options.githubProfile = target.Base, "", d.settings.Repository, d.settings.GitHubProfile
	options.inputs, options.dockerTests = d.settings.Inputs, d.settings.DockerTests
	roles := d.settings.Roles[ticket.File]
	options.provider = roles.Implementation.Provider
	options.frozenRoles, options.frozenConfig = &roles, &d.settings.Config
	options.frozenImage, options.frozenIdentity = d.settings.ImageID, d.settings.Identity
	options.frozenInstructions = &d.settings.Instructions
	options.frozenInputHashes = d.settings.InputHashes
	options.frozenSigningImage, options.frozenDaemonImage = d.settings.SigningImage, d.settings.DaemonImage
	directory, inspectErr := workrun.RunDirectory(d.plan.Root, d.plan.Reference, ticket.File, previous.RunID, false)
	if inspectErr == nil {
		if _, err := os.Lstat(filepath.Join(directory, "journal.json")); err == nil {
			if _, _, err := d.loadRun(ticket.File, *previous); err != nil {
				return *previous, err
			}
			loaded, err := workrun.Load(directory)
			if err != nil || loaded.Reconciliation != nil {
				return *previous, fmt.Errorf("pending reconciliation must finish before a provider resumes")
			}
			options.resume = previous.RunID
		} else if !os.IsNotExist(err) {
			return *previous, err
		}
	}
	if options.resume == "" {
		// Source capture and identity freezing happen before the first journal
		// save. An interrupted preparation has not started a provider. Refuse
		// ambiguous leftovers rather than guessing whether work was executed.
		if inspectErr == nil {
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				return *previous, fmt.Errorf("incomplete ticket preparation retained; inspect the private run before retrying")
			}
		}
		snapshot, cleanup, err := d.snapshot(ctx, target, d.directory)
		if err != nil {
			return *previous, err
		}
		defer cleanup()
		options.snapshot = &snapshot
	}
	fmt.Fprintf(d.output, "Feature ticket %s; implementation provider %s.\n", ticket.File, options.provider)
	stream := &seriesTicketWriter{output: d.output, ticket: ticket.File}
	defer stream.Flush()
	runErr := runSelectedCommand(ctx, options, stream)
	directory, journal, loadErr := d.loadRun(ticket.File, *previous)
	if loadErr != nil {
		return *previous, errors.Join(runErr, loadErr)
	}
	return resultForRun(directory, journal), runErr
}

func (d *seriesDriver) Observe(ctx context.Context, result workseries.Result) (workseries.Observation, error) {
	ticket := filepath.Base(filepath.Dir(result.Directory)) + ".md"
	directory, journal, err := d.loadRun(ticket, result)
	_ = directory
	if err != nil {
		return workseries.Observation{}, err
	}
	if journal.Publication.HeadSHA != result.HeadSHA || journal.Plan.Base != result.Base || journal.Publication.Number != result.PRNumber {
		return workseries.Observation{}, fmt.Errorf("ticket checkpoint changed outside the feature controller; restart the feature to reload it")
	}
	remote, err := d.publisher(journal.Plan).Observe(ctx, journal.Plan, journal.Publication)
	observation := workseries.Observation{State: remote.State, Base: remote.Base, BaseSHA: remote.BaseSHA, HeadSHA: remote.HeadSHA, MergeSHA: remote.MergeSHA}
	if err == nil && remote.State == "OPEN" && remote.Base == journal.Plan.Base && remote.BaseSHA == journal.Publication.BaseSHA && remote.HeadSHA == journal.Publication.HeadSHA {
		checks, checkErr := d.publisher(journal.Plan).Checks(ctx, journal.Plan, journal.Publication)
		observation.CI, observation.Details = checks.Status, checks.Details
		if checkErr != nil {
			observation.CI, observation.Details = "unverified", "current published CI could not be verified"
		}
	}
	return observation, err
}

func (d *seriesDriver) Reconcile(ctx context.Context, ticket workseries.Ticket, target workseries.Target, previous workseries.Result) (workseries.Result, error) {
	if err := d.checkInputs(); err != nil {
		return previous, err
	}
	directory, journal, err := d.loadRun(ticket.File, previous)
	if err != nil {
		return previous, err
	}
	lockPath := filepath.Join(directory, "run.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return previous, fmt.Errorf("ticket lock must be a regular file")
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return previous, fmt.Errorf("ticket is owned by another controller")
	}
	defer lock.Close()
	current, err := workrun.Load(directory)
	if err != nil || current.UpdatedAt != journal.UpdatedAt {
		return previous, fmt.Errorf("ticket checkpoint changed before reconciliation")
	}
	var observed workrun.RemotePR
	if journal.Publication.Number > 0 {
		remote, err := d.publisher(journal.Plan).Observe(ctx, journal.Plan, journal.Publication)
		if err != nil || remote.State != "OPEN" || remote.HeadSHA != journal.Publication.HeadSHA {
			return previous, fmt.Errorf("PR changed or closed before reconciliation")
		}
		if remote.Base != journal.Plan.Base && remote.Base != target.Base {
			return previous, fmt.Errorf("PR base was changed outside the feature")
		}
		observed = remote
	}
	if journal.Reconciliation == nil && journal.Plan.Base == target.Base && journal.Plan.BaseSHA == target.SHA {
		journal.State, journal.ResumeState, journal.StopReason = "ci", "", ""
		journal.Feedback = previous.StopReason
		if err := workrun.Save(directory, &journal); err != nil {
			return previous, err
		}
		if err := lock.Close(); err != nil {
			return previous, err
		}
		updated := resultForRun(directory, journal)
		return d.Execute(ctx, ticket, target, &updated)
	}
	if journal.Reconciliation == nil {
		snapshot, cleanup, err := d.snapshot(ctx, target, directory)
		if err != nil {
			return previous, err
		}
		defer cleanup()
		snapshot, err = retainSeriesSnapshot(directory, snapshot)
		if err != nil {
			return previous, err
		}
		if journal.Publication.Number > 0 {
			journal.Plan.Restack = &workrun.RestackBoundary{Base: observed.Base, BaseSHA: observed.BaseSHA, HeadSHA: observed.HeadSHA, Number: journal.Publication.Number}
		}
		if err := workrun.BeginReconciliation(&journal, snapshot); err != nil {
			return previous, err
		}
		if err := workrun.Save(directory, &journal); err != nil {
			return previous, err
		}
	}
	recorded := journal.Reconciliation.Snapshot
	if err := validateRetainedSeriesSnapshot(directory, recorded); err != nil {
		return previous, err
	}
	rebase, err := workrun.ReplayReconciliation(ctx, workrun.DockerRepository{Runtime: d.runtime, ImageID: d.settings.ImageID}, &journal)
	if err != nil {
		return previous, err
	}
	if err := workrun.Save(directory, &journal); err != nil {
		return previous, err
	}
	journal.Plan.Base, journal.Plan.BaseSHA = recorded.Branch, recorded.SHA
	journal.Plan.SourceSHA, journal.Plan.StartingSHA = recorded.SHA, recorded.SHA
	journal.Evidence = workrun.CheckEvidence{}
	journal.CI = workrun.CIResult{}
	journal.Outcome = workrun.Outcome{}
	journal.State, journal.ResumeState, journal.StopReason = "implementing", "", ""
	journal.Feedback = "The controller moved this ticket onto published base " + recorded.Branch + " at " + recorded.SHA + ". Retain this original implementation session and ticket scope. Previous tests and review no longer establish readiness. Request fresh isolated checks, complete local review, and return current PR metadata."
	if rebase.Conflict {
		journal.Feedback += " Resolve the unfinished Git rebase in this workspace, preserving only this ticket's changes. Conflicting paths: " + strings.Join(rebase.Paths, ", ") + ". Finish the rebase and commit before requesting checks; ask a human for product decisions."
	}
	journal.Reconciliation = nil
	if err := workrun.Save(directory, &journal); err != nil {
		return previous, err
	}
	_ = os.Remove(recorded.Bundle)
	// Release before the normal runner acquires this same lock and registers
	// its heartbeat. The saved native SessionID is deliberately retained.
	if err := lock.Close(); err != nil {
		return previous, err
	}
	updated := resultForRun(directory, journal)
	if recorded.Branch != target.Base || recorded.SHA != target.SHA {
		if rebase.Conflict {
			return updated, fmt.Errorf("base moved again during conflict recovery; resolve the recorded rebase before retrying")
		}
		return d.Reconcile(ctx, ticket, target, updated)
	}
	return d.Execute(ctx, ticket, target, &updated)
}

func retainSeriesSnapshot(directory string, snapshot workrun.BranchSnapshot) (workrun.BranchSnapshot, error) {
	parent := filepath.Join(directory, "reconciliation-source")
	if info, err := os.Lstat(parent); os.IsNotExist(err) {
		if err := os.Mkdir(parent, 0700); err != nil {
			return snapshot, err
		}
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return snapshot, fmt.Errorf("reconciliation source must be a private real directory")
	}
	path := filepath.Join(parent, snapshot.SHA+".bundle")
	if _, err := os.Lstat(path); err == nil {
		// No worker has been changed before the pending checkpoint is saved.
		// Reuse a private prepared bundle after a crash in that small interval;
		// the isolated rebaser verifies its exact Git revision before mutation.
		snapshot.Bundle = path
		return snapshot, validateRetainedSeriesSnapshot(directory, snapshot)
	} else if !os.IsNotExist(err) {
		return snapshot, fmt.Errorf("cannot inspect retained reconciliation source")
	}
	if err := os.Rename(snapshot.Bundle, path); err != nil {
		return snapshot, err
	}
	snapshot.Bundle = path
	return snapshot, validateRetainedSeriesSnapshot(directory, snapshot)
}

func validateRetainedSeriesSnapshot(directory string, snapshot workrun.BranchSnapshot) error {
	if !regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(snapshot.SHA) || snapshot.Bundle != filepath.Join(directory, "reconciliation-source", snapshot.SHA+".bundle") {
		return fmt.Errorf("reconciliation source points outside its private run")
	}
	parent, err := os.Lstat(filepath.Dir(snapshot.Bundle))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("reconciliation source directory is unsafe")
	}
	info, err := os.Lstat(snapshot.Bundle)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > 2*1024*1024*1024 {
		return fmt.Errorf("reconciliation source is not a private bounded bundle")
	}
	return nil
}

func (d *seriesDriver) adoptRuns(state *workseries.State) error {
	for _, ticket := range d.plan.Tickets {
		previous, recorded := state.Results[ticket.File]
		if !recorded {
			parent := filepath.Join(d.plan.Root, ".sdlc", "work", d.plan.Reference, "runs", strings.TrimSuffix(ticket.File, ".md"))
			entries, err := os.ReadDir(parent)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			matches := []workseries.Result{}
			for _, entry := range entries {
				if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(entry.Name()) {
					continue
				}
				candidate := workseries.Result{RunID: entry.Name()}
				directory, journal, err := d.loadRun(ticket.File, candidate)
				if err != nil {
					return fmt.Errorf("existing ticket run cannot be safely adopted: %w", err)
				}
				matches = append(matches, resultForRun(directory, journal))
			}
			if len(matches) > 1 {
				return fmt.Errorf("multiple existing runs for %s; resolve the ambiguity before launching a feature", ticket.File)
			}
			if len(matches) == 0 {
				continue
			}
			state.Results[ticket.File] = matches[0]
			continue
		}
		if previous.RunID == "" {
			continue
		}
		directory, journal, err := d.loadRun(ticket.File, previous)
		if err != nil {
			// A checkpointed launch may precede preparation. Never replace a
			// damaged existing journal or adopt a different native session.
			path := filepath.Join(d.plan.Root, ".sdlc", "work", d.plan.Reference, "runs", strings.TrimSuffix(ticket.File, ".md"), previous.RunID, "journal.json")
			if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
				continue
			}
			return err
		}
		refreshed := resultForRun(directory, journal)
		if previous.State == "merged" {
			refreshed.State = "merged"
			refreshed.MergeSHA = previous.MergeSHA
		}
		state.Results[ticket.File] = refreshed
	}
	return nil
}
