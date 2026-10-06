package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
