package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestFeatureAdoptsOnlyOwnedMetadataPreparationFailure(t *testing.T) {
	for _, change := range []string{"metadata only", "busy", "workspace", "models", "standalone", "corrupt journal"} {
		t.Run(change, func(t *testing.T) {
			driver, result := featureAdoptionFixture(t, "blocked")
			directory, _, err := driver.loadRun("01-selected.md", result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
				t.Fatal(err)
			}
			owner, err := runstatus.OwnPreparation(directory)
			if err != nil {
				t.Fatal(err)
			}
			p := driver.preparationIdentity("01-selected.md", result.RunID)
			p.Version, p.FeatureOwned, p.FailedAt, p.Reason = 1, true, time.Now().UTC(), "public capture failure"
			if change == "models" {
				p.Roles = workrun.DefaultModels().Claude
			}
			if change == "standalone" {
				p.FeatureOwned = false
			}
			registry := runstatus.New(os.Getenv("SDLC_STATE_DIR"))
			if err := registry.RegisterPreparationFailure(directory, p, owner); err != nil {
				owner.Close()
				t.Fatal(err)
			}
			if change == "busy" {
				defer owner.Close()
			} else {
				owner.Close()
			}
			if change == "workspace" {
				if err := os.Mkdir(filepath.Join(directory, "workspace"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if change == "corrupt journal" {
				if err := os.WriteFile(filepath.Join(directory, "journal.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result.State, result.StopReason = "blocked", p.Reason
			checkpoint := workseries.State{Version: 1, Plan: driver.plan, Results: map[string]workseries.Result{"01-selected.md": result}}
			err = driver.adoptRuns(&checkpoint)
			if change != "metadata only" {
				if err == nil {
					t.Fatalf("unsafe %s preparation reset", change)
				}
				if checkpoint.Results["01-selected.md"].State != "blocked" {
					t.Fatal("failed adoption changed blocked state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := checkpoint.Results["01-selected.md"]
			if got.State != "prepared" || got.RunID != result.RunID || got.StopReason != p.Reason || got.Directory != "" {
				t.Fatalf("retry identity/reason changed: %+v", got)
			}
			if persisted, err := runstatus.LoadPreparation(directory); err != nil || persisted.Reason != p.Reason {
				t.Fatal("adoption erased original failure", err)
			}
			checkpoint.Results = map[string]workseries.Result{}
			if err := driver.adoptRuns(&checkpoint); err != nil {
				t.Fatal(err)
			}
			if checkpoint.Results["01-selected.md"].RunID != result.RunID {
				t.Fatal("unrecorded failure adopted with different ID")
			}
		})
	}
}

type preparationRetargetDriver struct {
	controller *seriesDriver
	target     workseries.Target
	executed   []workseries.Result
	reconciled int
}

func (d *preparationRetargetDriver) Base(context.Context, string) (workseries.Target, error) {
	return d.target, nil
}

func (d *preparationRetargetDriver) Execute(_ context.Context, _ workseries.Ticket, target workseries.Target, previous *workseries.Result) (workseries.Result, error) {
	result := *previous
	result.State, result.Base, result.BaseSHA = "ready", target.Base, target.SHA
	result.HeadSHA = strings.Repeat("b", 40)
	d.executed = append(d.executed, result)
	return result, nil
}

func (d *preparationRetargetDriver) Observe(_ context.Context, result workseries.Result) (workseries.Observation, error) {
	return workseries.Observation{State: "OPEN", Base: result.Base, BaseSHA: result.BaseSHA, HeadSHA: result.HeadSHA}, nil
}

func (d *preparationRetargetDriver) Reconcile(ctx context.Context, ticket workseries.Ticket, target workseries.Target, result workseries.Result) (workseries.Result, error) {
	d.reconciled++
	return d.controller.Reconcile(ctx, ticket, target, result)
}

func TestFeaturePreparationRetryUsesMovedBaseWithoutReconciliation(t *testing.T) {
	controller, result := featureAdoptionFixture(t, "blocked")
	directory, journal, err := controller.loadRun("01-selected.md", result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
		t.Fatal(err)
	}
	owner, err := runstatus.OwnPreparation(directory)
	if err != nil {
		t.Fatal(err)
	}
	p := controller.preparationIdentity("01-selected.md", result.RunID)
	p.Version, p.FeatureOwned, p.FailedAt, p.Reason = 1, true, time.Now().UTC(), "public capture failure"
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).RegisterPreparationFailure(directory, p, owner); err != nil {
		owner.Close()
		t.Fatal(err)
	}
	owner.Close()
	result.State, result.StopReason, result.Base, result.BaseSHA = "blocked", p.Reason, journal.Plan.Base, journal.Plan.BaseSHA
	state := workseries.State{Version: 1, Plan: controller.plan, Results: map[string]workseries.Result{
		"01-selected.md": result,
		"02-other.md":    {RunID: strings.Repeat("2", 24), State: "merged", MergeSHA: journal.Plan.BaseSHA},
	}}
	if err := controller.adoptRuns(&state); err != nil {
		t.Fatal(err)
	}
	seriesDirectory, err := workseries.Directory(controller.plan.Root, controller.plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	boundary := &preparationRetargetDriver{controller: controller, target: workseries.Target{Base: "main", SHA: strings.Repeat("c", 40)}}
	err = (workseries.Runner{Driver: boundary}).Run(context.Background(), seriesDirectory, &state)
	if err != nil || boundary.reconciled != 0 || len(boundary.executed) != 1 {
		t.Fatalf("metadata-only retry attempted execution reconciliation: %v; reconciled %d; executed %+v", err, boundary.reconciled, boundary.executed)
	}
	got := state.Results["01-selected.md"]
	if got.RunID != result.RunID || got.BaseSHA != boundary.target.SHA || got.Base != boundary.target.Base || got.State != "ready" {
		t.Fatalf("retry lost identity or refreshed target: %+v", got)
	}
}

func TestFeatureStreamsKeepConcurrentTicketLinesSeparate(t *testing.T) {
	var output bytes.Buffer
	shared := &seriesWriter{w: &output}
	var tasks sync.WaitGroup
	for _, ticket := range []string{"01-one.md", "02-two.md"} {
		tasks.Add(1)
		go func(ticket string) {
			defer tasks.Done()
			writer := &seriesTicketWriter{output: shared, ticket: ticket}
			for i := 0; i < 20; i++ {
				_, _ = writer.Write([]byte(`{"message":`))
				_, _ = writer.Write([]byte(`"event"}` + "\n"))
			}
			writer.Flush()
		}(ticket)
	}
	tasks.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 40 {
		t.Fatalf("lost lines: %d", len(lines))
	}
	for _, line := range lines {
		if line != `[01-one.md] {"message":"event"}` && line != `[02-two.md] {"message":"event"}` {
			t.Fatalf("spliced concurrent output: %q", line)
		}
	}
}

func TestFeatureOptionBoundaries(t *testing.T) {
	valid := [][]string{
		{"--reference", "TASK-1", "--all"},
		{"--reference", "TASK-1", "--all", "--parallel", "2", "--watch", "--alternate-providers"},
		{"--reference", "TASK-1", "--all", "--provider", "claude", "--docker-tests", "--dry-run"},
	}
	for _, args := range valid {
		options, err := parseRunOptions(args)
		if err != nil || !options.all || options.parallel < 1 {
			t.Fatalf("valid feature rejected: %v: %v", args, err)
		}
	}
	invalid := [][]string{
		{"--reference", "TASK-1", "--all", "--ticket", "01-selected.md"},
		{"--reference", "TASK-1", "--all", "--branch", "manual"},
		{"--reference", "TASK-1", "--all", "--resume", strings.Repeat("a", 24)},
		{"--reference", "TASK-1", "--all", "--parallel", "0"},
		{"--reference", "TASK-1", "--all", "--parallel", "9"},
		{"--reference", "TASK-1", "--all", "--alternate-providers", "--model", "example"},
		append(runArgs(), "--watch"), append(runArgs(), "--parallel", "1"),
	}
	for _, args := range invalid {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
}

func featureAdoptionFixture(t *testing.T, state string) (*seriesDriver, workseries.Result) {
	t.Helper()
	root := runGitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workseries.Discover(context.Background(), root, "TASK-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := project.Launch(context.Background(), root, "TASK-1", "01-selected.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := &workrun.PublicationIdentity{GitHubProfile: "example", RepositoryID: 1, RepositoryName: "example/project", ProfileID: "example", GitHubID: 1, GitHubLogin: "example", GitName: "Example User", GitEmail: "example@example.invalid", SSHPublicKey: "ssh-ed25519 AAAA", SSHFingerprint: "SHA256:example"}
	settings := seriesSettings{Provider: "codex", Version: 1, Repository: "example/project", GitHubProfile: "example", SigningProfile: "key", Identity: identity, ImageID: "sha256:" + strings.Repeat("a", 64), Roles: map[string]workrun.Roles{}, InputHashes: map[string]string{}, Config: launch.Config, SigningImage: runtimepins.DefaultSigningImage, Instructions: "frozen instructions"}
	for _, ticket := range plan.Tickets {
		settings.Roles[ticket.File] = workrun.DefaultModels().Codex
		path := ".sdlc/work/TASK-1/tickets/" + ticket.File
		hash, err := seriesInputHash(root, path)
		if err != nil {
			t.Fatal(err)
		}
		settings.InputHashes[path] = hash
	}
	for _, path := range append([]string{project.ConfigPath}, launch.Config.InputFiles...) {
		hash, err := seriesInputHash(root, path)
		if err != nil {
			t.Fatal(err)
		}
		settings.InputHashes[path] = hash
	}
	result := workseries.Result{RunID: strings.Repeat("1", 24)}
	directory, err := workrun.RunDirectory(root, "TASK-1", "01-selected.md", result.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	ticket := ".sdlc/work/TASK-1/tickets/01-selected.md"
	journal := workrun.Journal{Version: 1, ID: result.RunID, State: state, SessionID: "retained-original-session", Attempt: 9, Rounds: 2, ImageID: settings.ImageID, Instructions: settings.Instructions, Workspace: filepath.Join(directory, "workspace"), Plan: workrun.Plan{Root: root, Reference: "TASK-1", Ticket: ticket, SourceSHA: launch.Head, StartingSHA: launch.Head, Base: "main", BaseSHA: launch.Head, Branch: "work/example", Repository: settings.Repository, GitHubProfile: settings.GitHubProfile, SigningProfile: settings.SigningProfile, PublicationIdentity: identity, Roles: settings.Roles["01-selected.md"], Checks: settings.Config.Checks, Inputs: []workrun.Input{{Path: ticket, SHA256: settings.InputHashes[ticket]}}, CheckInputs: []workrun.Input{{Path: "README.md", SHA256: settings.InputHashes["README.md"]}}, SigningImage: settings.SigningImage}}
	if err := workrun.Save(directory, &journal); err != nil {
		t.Fatal(err)
	}
	return &seriesDriver{plan: plan, settings: settings, output: io.Discard}, result
}

func TestFeatureAdoptsActiveAndAttentionStagesWithoutResettingSession(t *testing.T) {
	for _, stage := range []string{"implementing", "checking", "publishing", "ci", "reviewing", "awaiting_reviewer", "waiting_for_human", "ready", "blocked"} {
		t.Run(stage, func(t *testing.T) {
			driver, result := featureAdoptionFixture(t, stage)
			checkpoint := workseries.State{Version: 1, Plan: driver.plan, Results: map[string]workseries.Result{}}
			if err := driver.adoptRuns(&checkpoint); err != nil {
				t.Fatal(err)
			}
			adopted := checkpoint.Results["01-selected.md"]
			if adopted.RunID != result.RunID || adopted.State != stage {
				t.Fatalf("wrong adoption: %+v", adopted)
			}
			_, journal, err := driver.loadRun("01-selected.md", adopted)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Attempt != 9 || journal.Rounds != 2 || journal.SessionID != "retained-original-session" {
				t.Fatal("adoption reset provider state or limits")
			}
			directory, err := workseries.Directory(driver.plan.Root, driver.plan.Reference, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := workseries.Save(directory, &checkpoint); err != nil {
				t.Fatal("adopted state cannot be checkpointed", err)
			}
		})
	}
}

func TestFeatureAdoptionRejectsChangedPairAndMissingInput(t *testing.T) {
	for _, change := range []string{"pair", "input", "models", "instructions"} {
		t.Run(change, func(t *testing.T) {
			driver, result := featureAdoptionFixture(t, "ready")
			directory, journal, err := driver.loadRun("01-selected.md", result)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pair":
				journal.Plan.SigningProfile = "other"
			case "input":
				journal.Plan.Inputs = nil
			case "models":
				journal.Plan.Roles = workrun.DefaultModels().Claude
			case "instructions":
				journal.Instructions = "changed"
			}
			if err := workrun.Save(directory, &journal); err != nil {
				t.Fatal(err)
			}
			if _, _, err := driver.loadRun("01-selected.md", result); err == nil {
				t.Fatal("incompatible run adopted")
			}
		})
	}
}

func TestFeatureRetainsReconciliationBundleInPrivateRun(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(temporary, []byte("disposable snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := retainSeriesSnapshot(directory, workrun.BranchSnapshot{Branch: "main", SHA: strings.Repeat("a", 40), Bundle: temporary})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("temporary copy retained")
	}
	if err := validateRetainedSeriesSnapshot(directory, snapshot); err != nil {
		t.Fatal(err)
	}
	// A process may stop after retaining the bundle but before saving pending
	// reconciliation. Repeating preparation must adopt that safe exact path.
	fresh := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(fresh, []byte("disposable fresh export"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := retainSeriesSnapshot(directory, workrun.BranchSnapshot{Branch: snapshot.Branch, SHA: snapshot.SHA, Bundle: fresh})
	if err != nil || recovered.Bundle != snapshot.Bundle {
		t.Fatalf("prepared snapshot could not be recovered: %+v %v", recovered, err)
	}
	if err := validateRetainedSeriesSnapshot(t.TempDir(), snapshot); err == nil {
		t.Fatal("outside bundle accepted")
	}
	if err := os.Chmod(snapshot.Bundle, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRetainedSeriesSnapshot(directory, snapshot); err == nil {
		t.Fatal("public bundle accepted")
	}
}

type featureCapabilityDocker struct {
	capabilities string
	calls        [][]string
}

func (d *featureCapabilityDocker) Run(context.Context, ...string) error {
	return fmt.Errorf("unexpected mutation")
}
func (d *featureCapabilityDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	d.calls = append(d.calls, append([]string{}, args...))
	switch args[0] {
	case "context":
		return []byte(`"unix:///tmp/fake.sock"`), nil
	case "info":
		return []byte(`{"id":"fake-engine","os":"linux"}`), nil
	case "image":
		return []byte("sha256:" + strings.Repeat("a", 64)), nil
	case "run":
		return []byte(d.capabilities), nil
	}
	return nil, fmt.Errorf("unexpected operation")
}
func TestFeatureCapabilitiesAreCheckedWithoutCredentialMounts(t *testing.T) {
	for _, capabilities := range []string{`{"version":2,"actions":["branch","snapshot","observe","restack"]}`, `{"version":1,"actions":["publish"]}`} {
		t.Run(capabilities, func(t *testing.T) {
			directory := t.TempDir()
			image := "sha256:" + strings.Repeat("a", 64)
			data, _ := json.Marshal(runtimeimage.State{Version: 1, ImageID: image, Engine: "fake-engine"})
			if err := os.WriteFile(filepath.Join(directory, "runtime.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			docker := &featureCapabilityDocker{capabilities: capabilities}
			manager := runtimeimage.Manager{Directory: directory, Docker: docker}
			err := checkSeriesRuntime(context.Background(), manager, image)
			if strings.Contains(capabilities, `"version":2`) != (err == nil) {
				t.Fatalf("incorrect capability decision: %v", err)
			}
			for _, args := range docker.calls {
				if args[0] == "run" {
					joined := strings.Join(args, " ")
					if !strings.Contains(joined, "--network none") || !strings.Contains(joined, "--read-only") || strings.Contains(joined, "--mount") || strings.Contains(joined, "--volume") {
						t.Fatalf("capability probe exposed credentials: %v", args)
					}
				}
			}
		})
	}
}

func TestFeatureMergedDependenciesMustExistInSnapshot(t *testing.T) {
	root := runGitFixture(t)
	ctx := context.Background()
	head, err := seriesGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head = strings.TrimSpace(head)
	if _, err := seriesGit(ctx, root, "update-ref", "refs/sdlc/snapshot", head); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(parent, "snapshot.bundle")
	if _, err := seriesGit(ctx, root, "bundle", "create", bundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	snapshot := workrun.BranchSnapshot{Branch: "main", SHA: head, Bundle: bundle}
	if err := checkSeriesAncestry(ctx, snapshot, []string{head}, parent); err != nil {
		t.Fatal("merged revision in snapshot was rejected", err)
	}
	if err := checkSeriesAncestry(ctx, snapshot, []string{strings.Repeat("b", 40)}, parent); err == nil {
		t.Fatal("missing merged dependency accepted")
	}
}

func TestFeatureDryRunIsOfflineAndDoesNotCreateCheckpoints(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	path := filepath.Join(root, ".sdlc/work/TASK-1/plan.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"tickets":{"02-other.md":{"depends_on":["01-selected.md"],"touches":["src/api"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Ticket bytes are deliberately irrelevant to an offline plan.
	if err := os.WriteFile(filepath.Join(root, ".sdlc/work/TASK-1/tickets/01-selected.md"), []byte{0xff, 0x00}, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runCommand(context.Background(), []string{"--reference", "TASK-1", "--all", "--parallel", "2", "--dry-run"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"01-selected.md", "02-other.md", "Maximum concurrent tickets: 2", "Offline feature plan"} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("missing %q: %s", fragment, output.String())
		}
	}
	for _, path := range []string{marker, os.Getenv("SDLC_STATE_DIR"), filepath.Join(root, ".sdlc/work/TASK-1/series")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote or connected: %s", path)
		}
	}
}

func TestFeatureInvalidGraphAndCorruptCheckpointStayOffline(t *testing.T) {
	for _, invalid := range []string{"cycle", "corrupt", "base"} {
		t.Run(invalid, func(t *testing.T) {
			root := runGitFixture(t)
			marker := forbidConnectedRunCommands(t, root)
			args := []string{"--reference", "TASK-1", "--all", "--dry-run"}
			var checkpoint string
			if invalid == "cycle" {
				if err := os.WriteFile(filepath.Join(root, ".sdlc/work/TASK-1/plan.json"), []byte(`{"version":1,"tickets":{"01-selected.md":{"depends_on":["02-other.md"]},"02-other.md":{"depends_on":["01-selected.md"]}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else if invalid == "corrupt" {
				directory, err := workseries.Directory(root, "TASK-1", true)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint = filepath.Join(directory, "journal.json")
				if err := os.WriteFile(checkpoint, []byte("damaged state\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				args = append(args, "--base", "--unsafe")
			}
			if err := runCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid feature accepted")
			}
			if checkpoint != "" {
				data, _ := os.ReadFile(checkpoint)
				if string(data) != "damaged state\n" {
					t.Fatal("damaged checkpoint overwritten")
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("invalid feature connected")
			}
		})
	}
}

func TestFeatureResumePreservesExplicitSettings(t *testing.T) {
	settings := seriesSettings{Provider: "claude", Alternate: true, Repository: "example/project", GitHubProfile: "work", Inputs: []string{"spec.md"}, DockerTests: true, Roles: map[string]workrun.Roles{"01-selected.md": workrun.DefaultModels().Claude}}
	options, err := parseRunOptions([]string{"--reference", "TASK-1", "--all"})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSeriesOptions(options, settings); err != nil {
		t.Fatal("defaults must reload saved settings", err)
	}
	for _, flags := range [][]string{{"--provider", "codex"}, {"--repo", "other/project"}, {"--github-profile", "other"}, {"--input", "other.md"}, {"--docker-tests=false"}} {
		options, err := parseRunOptions(append([]string{"--reference", "TASK-1", "--all"}, flags...))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSeriesOptions(options, settings); err == nil {
			t.Fatalf("explicit change accepted: %v", flags)
		}
	}
}

func TestFeatureFrozenInputsRejectLinksAndChangedContent(t *testing.T) {
	root := runGitFixture(t)
	hash, err := seriesInputHash(root, "spec.md")
	if err != nil {
		t.Fatal(err)
	}
	driver := seriesDriver{plan: workseries.Plan{Root: root}, settings: seriesSettings{InputHashes: map[string]string{"spec.md": hash}}}
	if err := driver.checkInputs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spec.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := driver.checkInputs(); err == nil {
		t.Fatal("changed requirements accepted")
	}
	if err := os.Symlink("spec.md", filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"linked.md", "../spec.md", ".git/config", "/spec.md", "spec.md/child"} {
		if _, err := seriesInputHash(root, path); err == nil {
			t.Fatalf("unsafe input accepted %s", path)
		}
	}
}

func TestFeatureRequiresPublishedCleanBaseline(t *testing.T) {
	root := runGitFixture(t)
	head, err := seriesGit(context.Background(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	settings := seriesSettings{InputHashes: map[string]string{"spec.md": "recorded"}}
	if err := requireSeriesBaseline(context.Background(), root, strings.TrimSpace(head), settings); err != nil {
		t.Fatal(err)
	}
	if err := requireSeriesBaseline(context.Background(), root, strings.Repeat("a", 40), settings); err == nil {
		t.Fatal("unpublished baseline accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireSeriesBaseline(context.Background(), root, strings.TrimSpace(head), settings); err == nil {
		t.Fatal("dirty baseline accepted")
	}
}

func TestFeatureInputSetsRequireEveryFrozenSelection(t *testing.T) {
	if err := matchSeriesCapturedInputs(workrun.Plan{Inputs: []workrun.Input{{Path: "ticket.md", SHA256: "changed"}}}, map[string]string{"ticket.md": "frozen"}); err == nil {
		t.Fatal("changed capture reached provider execution")
	}
	if sameSeriesInputPaths([]workrun.Input{{Path: "ticket.md"}}, []string{"ticket.md", "requirements.md"}) {
		t.Fatal("missing requirement accepted")
	}
	if sameSeriesInputPaths([]workrun.Input{{Path: "ticket.md"}, {Path: "ticket.md"}}, []string{"ticket.md", "requirements.md"}) {
		t.Fatal("duplicate replaces requirement")
	}
	if !sameSeriesInputPaths([]workrun.Input{{Path: "requirements.md"}, {Path: "ticket.md"}}, []string{"ticket.md", "requirements.md", "ticket.md"}) {
		t.Fatal("equivalent selection rejected")
	}
	driver := seriesDriver{plan: workseries.Plan{Reference: "TASK-1", Tickets: []workseries.Ticket{{File: "01-selected.md"}}}, settings: seriesSettings{Provider: "codex", Roles: map[string]workrun.Roles{"01-selected.md": workrun.DefaultModels().Codex}, Config: project.Config{}, InputHashes: map[string]string{}}}
	if err := driver.validateSettings(); err == nil {
		t.Fatal("absent hashes accepted")
	}
}
