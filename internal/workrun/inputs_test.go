package workrun

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T) (string, Journal, string) {
	t.Helper()
	root, launch := sourceFixture(t)
	directory := filepath.Join(realTemp(t), "abcdef0123456789abcdef01")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := Capture(context.Background(), launch, filepath.Join(directory, "workspace"), "ticket-work", "main", "example/repository", DefaultModels().Codex)
	if err != nil {
		t.Fatal(err)
	}
	j := Journal{Version: 1, ID: filepath.Base(directory), Plan: plan, Workspace: filepath.Join(directory, "workspace"), State: "waiting_for_human", PendingRole: "implementation", SessionID: "native-session", Rounds: 2, Attempt: 3, Evidence: CheckEvidence{Passed: true, Tree: "old-tree"}, Outcome: Outcome{Questions: []string{"Please supply the linked specification"}}}
	if err := Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	path := ".sdlc/work/JOB:1/specs/source.md"
	sourceWrite(t, root, path, "# Specification\nChoose deterministic ordering.\n")
	return directory, j, path
}
func TestAttachInputsDryThenAttachPreservesNativeState(t *testing.T) {
	directory, j, path := recoveryFixture(t)
	ctx := context.Background()
	before := j
	preview, err := AttachInputs(ctx, directory, &j, []string{path}, true)
	if err != nil || len(preview) != 1 {
		t.Fatal(preview, err)
	}
	if !reflect.DeepEqual(before, j) {
		t.Fatal("preview changed journal")
	}
	if _, err := os.Lstat(filepath.Join(j.Workspace, path)); !os.IsNotExist(err) {
		t.Fatal("preview wrote input")
	}
	originalExcludes, err := os.ReadFile(filepath.Join(j.Workspace, ".git/info/exclude"))
	if err != nil {
		t.Fatal(err)
	}
	added, err := AttachInputs(ctx, directory, &j, []string{path}, false)
	if err != nil || len(added) != 1 {
		t.Fatal(added, err)
	}
	if j.State != before.State || j.SessionID != before.SessionID || j.Rounds != before.Rounds || j.Attempt != before.Attempt || j.Plan.StartingSHA != before.Plan.StartingSHA || j.Plan.SourceSHA != before.Plan.SourceSHA || j.PendingInputs != nil || j.Evidence.Passed || j.Evidence.Tree != "" {
		t.Fatal("attachment changed frozen execution state or retained stale evidence")
	}
	if len(j.InputRecoveries) != 1 || !j.InputRecoveries[0].PreviousCheckpoint.Equal(before.UpdatedAt) {
		t.Fatal("missing audit")
	}
	after, err := os.ReadFile(filepath.Join(j.Workspace, ".git/info/exclude"))
	if err != nil || string(after[:len(originalExcludes)]) != string(originalExcludes) {
		t.Fatal("existing exclusions lost", err)
	}
	if err := VerifyCapturedInputs(j.Workspace, j.Plan.Inputs); err != nil {
		t.Fatal(err)
	}
	// PrepareReview copies every frozen input, including the supplement.
	j.Publication.HeadSHA = j.Plan.StartingSHA
	if _, err := isolatedGit(ctx, directory, "clone", "--bare", "--no-local", j.Workspace, filepath.Join(directory, "publication.git")); err != nil {
		t.Fatal(err)
	}
	review, err := PrepareReview(ctx, j, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCapturedInputs(review, j.Plan.Inputs); err != nil {
		t.Fatal("review missed attached input", err)
	}
	j.Publication = Publication{}
	again, err := AttachInputs(ctx, directory, &j, []string{path}, false)
	if err != nil || len(again) != 0 || len(j.InputRecoveries) != 1 {
		t.Fatal("identical repeated attachment was not a no-op", err)
	}
}
func TestAttachInputsRefusesUnsafeConflictingAndChangedOriginal(t *testing.T) {
	for _, scenario := range []string{"conflict", "tracked", "original", "symlink", "credential", "logs", "traversal"} {
		t.Run(scenario, func(t *testing.T) {
			directory, j, path := recoveryFixture(t)
			before := j.UpdatedAt
			switch scenario {
			case "conflict":
				sourceWrite(t, j.Workspace, path, "worker output")
			case "tracked":
				path = "README.md"
			case "original":
				sourceWrite(t, j.Plan.Root, j.Plan.Ticket, "edited ticket")
			case "symlink":
				target := filepath.Join(j.Plan.Root, path)
				os.Remove(target)
				if err := os.Symlink("../../../tickets/1-first.md", target); err != nil {
					t.Skip(err)
				}
			case "credential":
				path = ".secrets/token"
				sourceWrite(t, j.Plan.Root, path, "fake disposable text")
			case "logs":
				path = ".sdlc/work/JOB:1/runs/private.log"
				sourceWrite(t, j.Plan.Root, path, "output")
			case "traversal":
				path = "../source.md"
			}
			if _, err := AttachInputs(context.Background(), directory, &j, []string{path}, false); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if !j.UpdatedAt.Equal(before) || j.PendingInputs != nil {
				t.Fatal("failed preflight changed checkpoint")
			}
		})
	}
}
func TestPendingInputsRecoverOnlySavedBytesWithoutOverwrite(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "match", true: "conflict"}[conflict], func(t *testing.T) {
			directory, j, path := recoveryFixture(t)
			data := []byte("immutable specification\n")
			stage, err := os.MkdirTemp(directory, "input-recovery-")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeSynced(filepath.Join(stage, "0"), data, 0400); err != nil {
				t.Fatal(err)
			}
			j.PendingInputs = &PendingInputRecovery{InputRecovery: InputRecovery{PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: []Input{{path, inputHash(data)}}}, Stage: filepath.Base(stage)}
			if err := Save(directory, &j); err != nil {
				t.Fatal(err)
			}
			materialized := string(data)
			if conflict {
				materialized = "worker owns this output"
			}
			sourceWrite(t, j.Workspace, path, materialized)
			sourceWrite(t, j.Plan.Root, path, "source changed after staging")
			_, err = AttachInputs(context.Background(), directory, &j, []string{path}, false)
			if conflict {
				if err == nil || j.PendingInputs == nil {
					t.Fatal("conflicting output was adopted")
				}
				got, _ := os.ReadFile(filepath.Join(j.Workspace, path))
				if string(got) != materialized {
					t.Fatal("worker output overwritten")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if j.PendingInputs != nil || len(j.InputRecoveries) != 1 {
				t.Fatal("transaction did not complete")
			}
			got, _ := os.ReadFile(filepath.Join(j.Workspace, path))
			if string(got) != string(data) {
				t.Fatal("recovery recaptured host bytes")
			}
		})
	}
}
func TestInputRecoveryStageGate(t *testing.T) {
	_, j, _ := recoveryFixture(t)
	for _, state := range []string{"implementing", "prepared", "reviewing", "ready"} {
		copy := j
		copy.State = state
		if CanAttachInputs(copy) == nil {
			t.Fatal(state)
		}
	}
	j.Publication.Number = 1
	if CanAttachInputs(j) == nil {
		t.Fatal("published supplement accepted")
	}
}

func TestInputRecoveryCombinedLimitAndLargeIdenticalNoop(t *testing.T) {
	directory, j, path := recoveryFixture(t)
	sourceWrite(t, j.Plan.Root, path, strings.Repeat("x", 9*1024*1024))
	if _, err := AttachInputs(context.Background(), directory, &j, []string{path}, false); err != nil {
		t.Fatal(err)
	}
	checkpoint := j.UpdatedAt
	if added, err := AttachInputs(context.Background(), directory, &j, []string{path}, false); err != nil || len(added) != 0 {
		t.Fatal("large identical recorded input was not a no-op", err)
	}
	second := ".sdlc/work/JOB:1/specs/second.md"
	sourceWrite(t, j.Plan.Root, second, strings.Repeat("x", 8*1024*1024))
	if _, err := AttachInputs(context.Background(), directory, &j, []string{second}, false); err == nil {
		t.Fatal("combined inputs exceeded 16 MiB")
	}
	if !j.UpdatedAt.Equal(checkpoint) || j.PendingInputs != nil {
		t.Fatal("failed size preflight changed checkpoint")
	}
}

func TestRunnerRefusesPendingInputsBeforeExecution(t *testing.T) {
	directory, j := testRun(t)
	j.PendingInputs = &PendingInputRecovery{}
	p := &fakeProvider{}
	c := &fakeChecker{}
	pub := &fakePublisher{}
	repo := &fakeRepository{}
	runner := fakeRunner(p, c, pub, repo)
	started := false
	runner.OnStart = func(Journal) error { started = true; return nil }
	err := runner.Run(context.Background(), directory, j, "answer")
	if err == nil || !strings.Contains(err.Error(), "input recovery is unfinished") || started || len(p.calls) > 0 || pub.calls > 0 {
		t.Fatal("unfinished inputs reached execution boundary", err)
	}
}

func TestInterruptedMaterializationTempsStayPrivate(t *testing.T) {
	directory, j, _ := recoveryFixture(t)
	path := "local-requirements.md"
	data := []byte("# Private input\nUse the documented response shape.\n")
	stage, err := os.MkdirTemp(directory, "input-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSynced(filepath.Join(stage, "0"), data, 0400); err != nil {
		t.Fatal(err)
	}
	// A crash can leave a materialization inode. It stays outside the checkout,
	// including when an explicit supplementary document is outside .sdlc/work.
	if err := writeSynced(filepath.Join(stage, ".materialize-interrupted"), data, 0600); err != nil {
		t.Fatal(err)
	}
	j.PendingInputs = &PendingInputRecovery{InputRecovery: InputRecovery{PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: []Input{{path, inputHash(data)}}}, Stage: filepath.Base(stage)}
	if err := Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if _, err := AttachInputs(context.Background(), directory, &j, []string{path}, false); err != nil {
		t.Fatal(err)
	}
	untracked, err := SafeGit(context.Background(), j.Workspace, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		t.Fatal(err)
	}
	if untracked != "" {
		t.Fatalf("recovery leaked publishable private files: %q", untracked)
	}
	if _, err := os.Stat(filepath.Join(stage, ".materialize-interrupted")); err != nil {
		t.Fatal("private crash fixture disappeared unexpectedly", err)
	}
}

func TestAttachInputsPromotesSafeCheckDocumentOnly(t *testing.T) {
	root, launch := sourceFixture(t)
	path := "check-only.md"
	sourceWrite(t, root, path, "# Check specification\nUse the documented fixture.\n")
	sourceWrite(t, root, ".env", "TEST_MODE=offline\n")
	launch.Config.InputFiles = []string{path, ".env"}
	directory := filepath.Join(realTemp(t), "abcdef0123456789abcdef02")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := Capture(context.Background(), launch, filepath.Join(directory, "workspace"), "ticket-work", "main", "example/repository", DefaultModels().Codex)
	if err != nil {
		t.Fatal(err)
	}
	j := Journal{Version: 1, ID: filepath.Base(directory), Plan: plan, Workspace: filepath.Join(directory, "workspace"), State: "waiting_for_human", PendingRole: "implementation", Outcome: Outcome{Questions: []string{"Please supply the check specification"}}}
	if err := Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	checks := append([]Input{}, j.Plan.CheckInputs...)
	if _, err := os.Lstat(filepath.Join(j.Workspace, path)); !os.IsNotExist(err) {
		t.Fatal("check-only input initially provider-visible")
	}
	added, err := AttachInputs(context.Background(), directory, &j, []string{path}, false)
	if err != nil || len(added) != 1 {
		t.Fatal("safe check document was not promoted", err)
	}
	if len(j.InputRecoveries) != 1 || !reflect.DeepEqual(checks, j.Plan.CheckInputs) {
		t.Fatal("promotion changed original check manifest")
	}
	if err := VerifyCapturedInputs(j.Workspace, []Input{added[0]}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCapturedInputs(filepath.Join(directory, "check-inputs"), checks); err != nil {
		t.Fatal("promotion changed immutable check bytes", err)
	}
	if _, err := AttachInputs(context.Background(), directory, &j, []string{".env"}, false); err == nil {
		t.Fatal("check-only environment file became provider input")
	}
}
