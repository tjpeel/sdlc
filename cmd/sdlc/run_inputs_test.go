package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func capturedAttentionFixture(t *testing.T) (workrun.Journal, string, string) {
	t.Helper()
	j, marker := attentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	launch, err := project.LaunchFrozen(context.Background(), j.Plan.Root, j.Plan.Reference, filepath.Base(j.Plan.Ticket), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workrun.Capture(context.Background(), launch, j.Workspace, "TASK-1-selected", "main", "example/repository", workrun.DefaultModels().Codex)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan = plan
	j.SessionID = "native-session"
	j.Rounds = 2
	j.Evidence = workrun.CheckEvidence{Passed: true, Tree: "old-tree"}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	path := ".sdlc/work/TASK-1/specs/source.md"
	full := filepath.Join(j.Plan.Root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# Source specification\nReturn a stable order.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return j, marker, path
}
func TestInputsCommandOfflineListPreviewAndAttachment(t *testing.T) {
	j, marker, path := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	ctx := context.Background()
	before, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	var output bytes.Buffer
	if err := inputsCommand(ctx, []string{"--run", j.ID, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), j.Plan.Ticket) {
		t.Fatal(output.String())
	}
	output.Reset()
	if err := inputsCommand(ctx, []string{"--run", j.ID[:8], "--add", path, "--dry-run", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), path) {
		t.Fatal(output.String())
	}
	after, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed checkpoint")
	}
	if _, err := os.Lstat(filepath.Join(j.Workspace, path)); !os.IsNotExist(err) {
		t.Fatal("preview materialized input")
	}
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	output.Reset()
	if err := inputsCommand(ctx, []string{"--run", j.ID, "--add", path}, &output); err != nil {
		t.Fatal(err)
	}
	saved, err := workrun.Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != j.State || saved.SessionID != j.SessionID || saved.Rounds != j.Rounds || saved.Evidence.Passed || len(saved.InputRecoveries) != 1 {
		t.Fatal("attachment changed execution checkpoint incorrectly")
	}
	if !strings.Contains(output.String(), "sdlc answer --run "+j.ID) {
		t.Fatal(output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline input command reached connected provider")
	}
}
func TestInputsDryRunRefusesOwnershipFailure(t *testing.T) {
	j, _, path := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var output bytes.Buffer
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--json"}, &output); err != nil {
		t.Fatal("read-only listing blocked by controller ownership", err)
	}
	output.Reset()
	err = inputsCommand(context.Background(), []string{"--run", j.ID, "--add", path, "--dry-run"}, &output)
	if !errors.Is(err, filelock.ErrBusy) {
		t.Fatal("preview bypassed active controller", err)
	}
}
func TestSeriesRunInputsAcceptsOnlyAuditedExtras(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	var inputs []workrun.Input
	for _, path := range []string{"ticket.md", "spec.md"} {
		data := []byte("frozen " + path)
		if err := os.WriteFile(filepath.Join(root, path), data, 0600); err != nil {
			t.Fatal(err)
		}
		hashes[path] = fmt.Sprintf("%x", sha256.Sum256(data))
		inputs = append(inputs, workrun.Input{Path: path, SHA256: hashes[path]})
	}
	j := workrun.Journal{Workspace: root, Plan: workrun.Plan{Root: root, Inputs: inputs}, InputRecoveries: []workrun.InputRecovery{{PreviousCheckpoint: time.Now().UTC(), AddedAt: time.Now().UTC(), Inputs: []workrun.Input{inputs[1]}}}}
	originals := []string{"ticket.md"}
	if err := validateSeriesRunInputs(context.Background(), j, originals, hashes); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"unaudited", "removed", "replaced", "duplicate", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			copy := j
			copy.Plan.Inputs = append([]workrun.Input{}, j.Plan.Inputs...)
			switch scenario {
			case "unaudited":
				copy.InputRecoveries = nil
			case "removed":
				copy.Plan.Inputs = copy.Plan.Inputs[1:]
			case "replaced":
				copy.Plan.Inputs[0].SHA256 = strings.Repeat("a", 64)
			case "duplicate":
				copy.Plan.Inputs = append(copy.Plan.Inputs, copy.Plan.Inputs[1])
			case "pending":
				copy.PendingInputs = &workrun.PendingInputRecovery{}
			}
			if err := validateSeriesRunInputs(context.Background(), copy, originals, hashes); err == nil {
				t.Fatal("invalid adoption accepted")
			}
		})
	}
	if !reflect.DeepEqual(originals, []string{"ticket.md"}) {
		t.Fatal("helper mutated feature inputs")
	}
}
func TestInputCommandShellPathUsesLiteralSingleQuotes(t *testing.T) {
	path := ".sdlc/work/TASK-1/specs/source$(fake)`fake`'name.md"
	quoted := quoteInputPath(path)
	if quoted != "'.sdlc/work/TASK-1/specs/source$(fake)`fake`'\"'\"'name.md'" {
		t.Fatal(quoted)
	}
}
func TestProbeRunControllerPreservesFilesystemCause(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "run.lock")
	if err := os.Symlink("missing", path); err != nil {
		t.Skip(err)
	}
	_, err := probeRunController(directory)
	if err == nil || errors.Is(err, filelock.ErrBusy) {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-parent", "run.lock")
	_, err = filelock.Acquire(missing)
	if !errors.Is(err, os.ErrNotExist) || errors.Is(err, filelock.ErrBusy) {
		t.Fatal("filesystem error was masked", err)
	}
}

func TestPendingRecoveryBlocksOrdinaryAnswersAndResume(t *testing.T) {
	j, _, path := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	stage, err := os.MkdirTemp(directory, "input-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("# Recorded specification\n")
	if err := os.WriteFile(filepath.Join(stage, "0"), data, 0400); err != nil {
		t.Fatal(err)
	}
	j.PendingInputs = &workrun.PendingInputRecovery{InputRecovery: workrun.InputRecovery{PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: []workrun.Input{{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}}, Stage: filepath.Base(stage)}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	for _, answer := range []bool{true, false} {
		_, err := resolveRunAction(context.Background(), j.ID, answer)
		if err == nil || !strings.Contains(err.Error(), "input recovery is unfinished") {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add", path}, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRunAction(context.Background(), j.ID, true); err != nil {
		t.Fatal("answer remained blocked after transaction completion", err)
	}
}

func TestProbeRunControllerRetainsPermissionFailure(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0000); err != nil {
		t.Skip(err)
	}
	defer os.Chmod(directory, 0700)
	_, err := probeRunController(directory)
	if err == nil {
		t.Skip("host identity bypasses directory permissions")
	}
	if !errors.Is(err, os.ErrPermission) || errors.Is(err, filelock.ErrBusy) {
		t.Fatal("permission failure was masked as controller contention", err)
	}
}

func TestInputsPendingListShowsEscapedCompletionWithoutMutation(t *testing.T) {
	j, marker, _ := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	stage, err := os.MkdirTemp(directory, "input-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{".sdlc/work/TASK-1/specs/source document.md", ".sdlc/work/TASK-1/specs/author's source.md"}
	var pending []workrun.Input
	for i, path := range paths {
		data := []byte(fmt.Sprintf("# Specification %d\n", i))
		if err := os.WriteFile(filepath.Join(stage, fmt.Sprint(i)), data, 0400); err != nil {
			t.Fatal(err)
		}
		pending = append(pending, workrun.Input{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	j.PendingInputs = &workrun.PendingInputRecovery{InputRecovery: workrun.InputRecovery{PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: pending}, Stage: filepath.Base(stage)}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := inputsCommand(context.Background(), []string{"--run", j.ID}, &output); err != nil {
		t.Fatal(err)
	}
	expected := "sdlc inputs --run " + j.ID + " --add '.sdlc/work/TASK-1/specs/source document.md' --add '.sdlc/work/TASK-1/specs/author'\"'\"'s source.md'"
	if !strings.Contains(output.String(), expected) {
		t.Fatalf("missing escaped completion command: %s", output.String())
	}
	for _, in := range pending {
		if !strings.Contains(output.String(), in.SHA256+"  "+in.Path) {
			t.Fatalf("missing pending input/hash: %s", output.String())
		}
		if _, err := os.Lstat(filepath.Join(j.Workspace, in.Path)); !os.IsNotExist(err) {
			t.Fatal("listing materialized pending input")
		}
	}
	after, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("listing changed pending transaction")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("listing reached connected provider")
	}
}

func TestInputsCommandPrivateCheckCaptureAndOwnership(t *testing.T) {
	j, marker, _ := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	if err := os.WriteFile(filepath.Join(j.Plan.Root, ".env"), []byte("TEST_MODE=offline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--dry-run"}, &output)
	lock.Close()
	if !errors.Is(err, filelock.ErrBusy) {
		t.Fatal("check capture bypassed ownership", err)
	}
	before, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--dry-run", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed journal")
	}
	if !strings.Contains(output.String(), `"scope":"checks"`) || strings.Contains(output.String(), "TEST_MODE") {
		t.Fatal("scope missing or check contents exposed", output.String())
	}
	output.Reset()
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	saved, err := workrun.Load(directory)
	if err != nil || len(saved.Plan.CheckInputs) != len(j.Plan.CheckInputs)+1 {
		t.Fatalf("check capture failed: %v; inputs=%+v; output=%s", err, saved.Plan.CheckInputs, output.String())
	}
	if _, err := os.Stat(filepath.Join(j.Workspace, ".env")); !os.IsNotExist(err) {
		t.Fatal("check file entered provider workspace")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("capture invoked provider")
	}
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--add", "fixture.md"}, &output); err == nil {
		t.Fatal("mixed scopes admitted")
	}
}

func TestSeriesCheckInputsPermitOnlyScopedAuditedAppends(t *testing.T) {
	j, _, _ := capturedAttentionFixture(t)
	directory := filepath.Dir(j.Workspace)
	originals := make([]string, len(j.Plan.CheckInputs))
	hashes := map[string]string{}
	for i, in := range j.Plan.CheckInputs {
		originals[i] = in.Path
		hashes[in.Path] = in.SHA256
	}
	if err := os.WriteFile(filepath.Join(j.Plan.Root, ".env"), []byte("TEST_MODE=offline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workrun.AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false); err != nil {
		t.Fatal(err)
	}
	if err := validateSeriesCheckInputs(j, directory, originals, hashes); err != nil {
		t.Fatal("audited check append rejected", err)
	}
	for _, scenario := range []string{"missing-audit", "original-hash", "missing-private-bytes"} {
		t.Run(scenario, func(t *testing.T) {
			copy := j
			copy.Plan.CheckInputs = append([]workrun.Input(nil), j.Plan.CheckInputs...)
			switch scenario {
			case "missing-audit":
				copy.InputRecoveries = nil
			case "original-hash":
				copy.Plan.CheckInputs[0].SHA256 = strings.Repeat("a", 64)
			case "missing-private-bytes":
				if err := os.Remove(filepath.Join(directory, "check-inputs", ".env")); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateSeriesCheckInputs(copy, directory, originals, hashes); err == nil {
				t.Fatal("unaudited or changed frozen input admitted")
			}
		})
	}
}

func featureCheckRecoveryFixture(t *testing.T, configInput string) (*seriesDriver, workrun.Journal, string) {
	t.Helper()
	driver, result := featureAdoptionFixture(t, "blocked")
	directory, j, err := driver.loadRun("01-selected.md", result)
	if err != nil {
		t.Fatal(err)
	}
	j.ResumeState, j.StopReason = "checking", "outstanding formatter numeric hold"
	if configInput != "" {
		data, err := os.ReadFile(filepath.Join(j.Plan.Root, project.ConfigPath))
		if err != nil {
			t.Fatal(err)
		}
		in := workrun.Input{Path: project.ConfigPath, SHA256: driver.settings.InputHashes[project.ConfigPath]}
		if configInput == "requirement" {
			driver.settings.Inputs = append(driver.settings.Inputs, project.ConfigPath)
			j.Plan.Inputs = append(j.Plan.Inputs, in)
			full := filepath.Join(j.Workspace, project.ConfigPath)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, data, 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			driver.settings.Config.InputFiles = append(driver.settings.Config.InputFiles, project.ConfigPath)
			j.Plan.CheckInputs = append(j.Plan.CheckInputs, in)
		}
	}
	for _, in := range j.Plan.CheckInputs {
		data, err := os.ReadFile(filepath.Join(j.Plan.Root, in.Path))
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(directory, "check-inputs", in.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	seriesDirectory, err := workseries.Directory(j.Plan.Root, j.Plan.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: driver.plan, Results: map[string]workseries.Result{"01-selected.md": result}, Settings: settings}
	if err := workseries.Save(seriesDirectory, &state); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Acquire(filepath.Join(seriesDirectory, "series.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	marker := forbidConnectedRunCommands(t, j.Plan.Root)
	if err := os.WriteFile(filepath.Join(j.Plan.Root, ".env"), []byte("TEST_MODE=offline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return driver, j, marker
}

func TestFeatureCheckInputCaptureUsesFrozenConfiguration(t *testing.T) {
	driver, j, marker := featureCheckRecoveryFixture(t, "")
	directory := filepath.Dir(j.Workspace)
	if err := os.WriteFile(filepath.Join(j.Plan.Root, project.ConfigPath), []byte(`{"version":1,"checks":[["changed","check"]],"input_files":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := driver.checkInputs(); err == nil {
		t.Fatal("feature coordinator accepted config drift")
	}
	before, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	var output bytes.Buffer
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--dry-run", "--json"}, &output); err != nil {
		t.Fatal("check-only preview rejected metadata drift", err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed checkpoint")
	}
	if _, err := os.Stat(filepath.Join(directory, "check-inputs", ".env")); !os.IsNotExist(err) {
		t.Fatal("preview materialized private input")
	}
	output.Reset()
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add", "spec.md", "--dry-run"}, &output); err == nil {
		t.Fatal("provider supplement ignored config drift")
	}
	if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env", "--json"}, &output); err != nil {
		t.Fatal("check-only capture rejected metadata drift", err)
	}
	saved, err := workrun.Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Plan.Checks, j.Plan.Checks) || len(saved.Plan.CheckInputs) != len(j.Plan.CheckInputs)+1 || saved.State != j.State || saved.ResumeState != j.ResumeState || saved.StopReason != j.StopReason || saved.SessionID != j.SessionID || saved.Attempt != j.Attempt || saved.Rounds != j.Rounds {
		t.Fatal("capture adopted mutable config or changed frozen execution checkpoint")
	}
	if _, err := os.Stat(filepath.Join(j.Workspace, ".env")); !os.IsNotExist(err) {
		t.Fatal("check capture entered provider workspace")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline check capture invoked provider")
	}
}

func TestFeatureCheckCaptureStillRejectsChangedFrozenInputsAndSettings(t *testing.T) {
	for _, scenario := range []string{"ticket", "check-file", "plan-checks", "config-requirement", "config-check"} {
		t.Run(scenario, func(t *testing.T) {
			configInput := ""
			if scenario == "config-requirement" {
				configInput = "requirement"
			}
			if scenario == "config-check" {
				configInput = "check"
			}
			_, j, _ := featureCheckRecoveryFixture(t, configInput)
			directory := filepath.Dir(j.Workspace)
			if err := os.WriteFile(filepath.Join(j.Plan.Root, project.ConfigPath), []byte(`{"version":1,"checks":[["changed","check"]],"input_files":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "ticket":
				if err := os.WriteFile(filepath.Join(j.Plan.Root, j.Plan.Ticket), []byte("Changed ticket"), 0600); err != nil {
					t.Fatal(err)
				}
			case "check-file":
				if err := os.WriteFile(filepath.Join(j.Plan.Root, j.Plan.CheckInputs[0].Path), []byte("Changed check fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "plan-checks":
				j.Plan.Checks = [][]string{{"changed", "check"}}
				if err := workrun.Save(directory, &j); err != nil {
					t.Fatal(err)
				}
				if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
			var output bytes.Buffer
			if err := inputsCommand(context.Background(), []string{"--run", j.ID, "--add-check-input", ".env"}, &output); err == nil {
				t.Fatal("changed frozen input or setting admitted")
			}
			after, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("rejected capture changed checkpoint")
			}
			if _, err := os.Stat(filepath.Join(directory, "check-inputs", ".env")); !os.IsNotExist(err) {
				t.Fatal("rejected capture materialized private input")
			}
		})
	}
}
