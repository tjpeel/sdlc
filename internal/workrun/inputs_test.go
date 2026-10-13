package workrun

import (
	"context"
	"errors"
	"fmt"
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

func TestAttachCheckInputsPreservesHoldAndPrivateScope(t *testing.T) {
	directory, j, _ := recoveryFixture(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "checking", configuredCheckRepairBound
	j.PendingRole = ""
	j.Evidence.Log = "checks-previous.log"
	request := VerificationRequest{ID: "offline", Purpose: "Check fixture", Commands: [][]string{{"test", "fixture"}}}
	j.Verification = []VerificationResult{{Request: request, Key: strings.Repeat("a", 64), Passed: true, Log: "verification-previous.log"}}
	before := j
	sourceWrite(t, j.Plan.Root, ".env", "TEST_MODE=offline\n")
	preview, err := AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, true)
	if err != nil || len(preview) != 1 || !reflect.DeepEqual(before, j) {
		t.Fatal("dry run changed checkpoint", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "check-inputs")); !os.IsNotExist(err) {
		t.Fatal("dry run created check directory")
	}
	added, err := AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false)
	if err != nil || len(added) != 1 {
		t.Fatal(added, err)
	}
	if j.State != before.State || j.ResumeState != before.ResumeState || j.StopReason != before.StopReason || j.SessionID != before.SessionID || j.Rounds != before.Rounds || j.Attempt != before.Attempt || !reflect.DeepEqual(j.Plan.Inputs, before.Plan.Inputs) {
		t.Fatal("capture changed checkpoint")
	}
	if len(j.Plan.CheckInputs) != 1 || j.Plan.CheckInputs[0].SHA256 != inputHash([]byte("TEST_MODE=offline\n")) || j.InputRecoveries[0].Scope != "checks" || !j.InputRecoveries[0].PreviousCheckpoint.Equal(before.UpdatedAt) {
		t.Fatal("check audit mismatch")
	}
	if j.Evidence.Passed || j.Verification[0].Passed || j.Verification[0].Key != "" || j.Evidence.Log != "checks-previous.log" || j.Verification[0].Log != "verification-previous.log" {
		t.Fatal("stale evidence retained or historical log lost")
	}
	if _, err := os.Stat(filepath.Join(j.Workspace, ".env")); !os.IsNotExist(err) {
		t.Fatal("check-only input entered workspace")
	}
	if err := VerifyCapturedInputs(filepath.Join(directory, "check-inputs"), j.Plan.CheckInputs); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(directory)
	if err != nil || !reflect.DeepEqual(saved.Plan.CheckInputs, j.Plan.CheckInputs) {
		t.Fatal("check capture did not round trip", err)
	}
	if again, err := AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false); err != nil || len(again) != 0 {
		t.Fatal("identical retry changed capture", err)
	}
	sourceWrite(t, j.Plan.Root, ".env", "TEST_MODE=changed\n")
	if _, err := AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false); err == nil {
		t.Fatal("frozen hash replaced")
	}
}

func TestCheckInputPendingRecoveryUsesImmutableScopedBytes(t *testing.T) {
	for _, scenario := range []string{"complete", "partial", "conflict", "wrong-scope"} {
		t.Run(scenario, func(t *testing.T) {
			directory, j, _ := recoveryFixture(t)
			data := []byte("TEST_MODE=offline\n")
			stage, err := os.MkdirTemp(directory, "input-recovery-")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeSynced(filepath.Join(stage, "0"), data, 0400); err != nil {
				t.Fatal(err)
			}
			j.PendingInputs = &PendingInputRecovery{InputRecovery: InputRecovery{Scope: "checks", PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: []Input{{".env", inputHash(data)}}}, Stage: filepath.Base(stage)}
			if err := Save(directory, &j); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, j.Plan.Root, ".env", "TEST_MODE=changed\n")
			if scenario == "partial" || scenario == "conflict" {
				body := string(data)
				if scenario == "conflict" {
					body = "conflict"
				}
				sourceWrite(t, filepath.Join(directory, "check-inputs"), ".env", body)
			}
			var attachErr error
			if scenario == "wrong-scope" {
				_, attachErr = AttachInputs(context.Background(), directory, &j, []string{".env"}, false)
			} else {
				_, attachErr = AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false)
			}
			if scenario == "conflict" || scenario == "wrong-scope" {
				if attachErr == nil || j.PendingInputs == nil {
					t.Fatal("unsafe recovery admitted")
				}
				return
			}
			if attachErr != nil {
				t.Fatal(attachErr)
			}
			got, err := os.ReadFile(filepath.Join(directory, "check-inputs", ".env"))
			if err != nil || string(got) != string(data) {
				t.Fatal("recovery recaptured source", err)
			}
			if _, err := os.Stat(filepath.Join(j.Workspace, ".env")); !os.IsNotExist(err) {
				t.Fatal("recovery leaked into workspace")
			}
		})
	}
}

func TestCheckInputsRejectUnsafeConflictingAndProviderPromotion(t *testing.T) {
	for _, scenario := range []string{"symlink", "directory-symlink", "conflict", "credential", "system", "over-budget", "provider", "prefix", "promotion"} {
		t.Run(scenario, func(t *testing.T) {
			directory, j, _ := recoveryFixture(t)
			path := "check-fixture.txt"
			sourceWrite(t, j.Plan.Root, path, "offline fixture\n")
			switch scenario {
			case "symlink":
				os.Remove(filepath.Join(j.Plan.Root, path))
				if err := os.Symlink("README.md", filepath.Join(j.Plan.Root, path)); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Symlink(j.Workspace, filepath.Join(directory, "check-inputs")); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				sourceWrite(t, filepath.Join(directory, "check-inputs"), path, "existing")
			case "credential":
				path = ".ssh/fake-key"
				sourceWrite(t, j.Plan.Root, path, "disposable")
			case "system":
				path = ".git/config"
			case "over-budget":
				sourceWrite(t, j.Plan.Root, path, strings.Repeat("x", maximumInputBytes))
			case "provider":
				path = j.Plan.Inputs[0].Path
			case "prefix":
				path = filepath.Dir(j.Plan.Inputs[0].Path)
			case "promotion":
				if _, err := AttachCheckInputs(context.Background(), directory, &j, []string{path}, false); err != nil {
					t.Fatal(err)
				}
				if _, err := AttachInputs(context.Background(), directory, &j, []string{path}, false); err == nil {
					t.Fatal("private check data promoted")
				}
				return
			}
			before := j.UpdatedAt
			if _, err := AttachCheckInputs(context.Background(), directory, &j, []string{path}, false); err == nil {
				t.Fatal("unsafe capture admitted")
			}
			if !j.UpdatedAt.Equal(before) || j.PendingInputs != nil {
				t.Fatal("rejected capture changed journal")
			}
		})
	}
}

func TestNewCheckInputInvalidatesCurrentCandidateAndReviewExposure(t *testing.T) {
	directory, j, _ := recoveryFixture(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "checking", configuredCheckRepairBound
	j.PendingRole = ""
	j.Outcome = testOutcome("implemented")
	j.SessionID = testNative
	request := VerificationRequest{ID: "offline", Purpose: "Check fixture", Commands: [][]string{{"test", "fixture"}}}
	j.Verification = []VerificationResult{{Request: request, Key: verificationKey(&j, request, testHead, testTree), Head: testHead, Tree: testTree, Passed: true, Log: "previous-verification.log"}}
	j.Evidence = CheckEvidence{Passed: true, Key: verificationKey(&j, VerificationRequest{}, testHead, testTree), Head: testHead, Tree: testTree, Log: "previous-check.log"}
	prepRequest := sourcePatchRequest()
	prepRequest.ID = "formatter-example-1"
	j.FormatterPreparation = []VerificationResult{{Request: prepRequest, Key: verificationKey(&j, prepRequest, testHead, testTree), Head: testHead, Tree: testTree, Diagnostic: "Previous formatter failure"}}
	if err := Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	overrideGrant(&j, "verification_requests", 16)
	grant := *j.RunLimitOverride
	sourceWrite(t, j.Plan.Root, ".env", "TEST_MODE=offline\n")
	if _, err := AttachCheckInputs(context.Background(), directory, &j, []string{".env"}, false); err != nil {
		t.Fatal(err)
	}
	if len(j.FormatterPreparation) != 0 || !reflect.DeepEqual(grant, *j.RunLimitOverride) {
		t.Fatal("formatter cache or grant changed incorrectly")
	}
	checker := &supplementaryChecker{}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &fakeRepository{})
	runner.Checker = checker
	completed := errors.New("offline checks complete")
	runner.OnState = func(saved Journal) error {
		if saved.State == "implementing" && checker.calls == 1 && len(checker.requests) == 1 {
			return completed
		}
		return nil
	}
	if err := runner.Run(context.Background(), directory, &j, ""); !errors.Is(err, completed) {
		t.Fatal("verification did not rerun", err)
	}
	if checker.calls != 1 || len(checker.requests) != 1 || !j.Evidence.Passed || !j.Verification[0].Passed || j.Verification[0].Key != verificationKey(&j, request, testHead, testTree) {
		t.Fatal("stale candidate evidence reused")
	}
	j.Publication = Publication{HeadSHA: j.Plan.StartingSHA}
	if _, err := SafeGit(context.Background(), j.Workspace, "clone", "--bare", "--no-local", j.Workspace, filepath.Join(directory, "publication.git")); err != nil {
		t.Fatal(err)
	}
	review, err := PrepareReview(context.Background(), j, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(review, ".env")); !os.IsNotExist(err) {
		t.Fatal("private check capture entered reviewer source")
	}
}

func TestCheckInputSelectionCannotInvalidateStructuredMachineRequests(t *testing.T) {
	for _, scenario := range []string{"source-patch", "baseline", "pending-outcome", "prefix"} {
		t.Run(scenario, func(t *testing.T) {
			directory, j, _ := recoveryFixture(t)
			path := "fixture/input.txt"
			sourceWrite(t, j.Plan.Root, path, "offline fixture")
			request := VerificationRequest{ID: "offline", Purpose: "Check fixture", Commands: [][]string{{"test", "fixture"}}}
			selected := path
			if scenario == "baseline" {
				request.Baseline = &VerificationBaseline{Revision: j.Plan.StartingSHA, Paths: []string{path}, ExpectedExitCode: 1, FailureContains: []string{"fixture"}}
			} else {
				if scenario == "prefix" {
					selected = "fixture"
				}
				request.SourcePatch = &SourcePatchRequest{Paths: []string{selected}}
			}
			if scenario == "pending-outcome" {
				j.Outcome.VerificationRequests = []VerificationRequest{request}
			} else {
				j.Verification = []VerificationResult{{Request: request}}
			}
			if err := Save(directory, &j); err != nil {
				t.Fatal(err)
			}
			before := j.UpdatedAt
			if _, err := AttachCheckInputs(context.Background(), directory, &j, []string{path}, false); err == nil {
				t.Fatal("check selection invalidated retained request")
			}
			if j.PendingInputs != nil || !j.UpdatedAt.Equal(before) {
				t.Fatal("rejected request overlap started a transaction")
			}
			if _, err := os.Stat(filepath.Join(directory, "check-inputs", path)); !os.IsNotExist(err) {
				t.Fatal("rejected selection materialized bytes")
			}
		})
	}
}

func TestLegacyInitialCheckInputBoundaryRemainsLoadable(t *testing.T) {
	directory, j, providerPath := recoveryFixture(t)
	checkRoot := filepath.Join(directory, "check-inputs")
	data := []byte("offline fixture\n")
	for i := 0; i < 256; i++ {
		path := fmt.Sprintf("fixtures/check-%03d.txt", i)
		sourceWrite(t, j.Plan.Root, path, string(data))
		sourceWrite(t, checkRoot, path, string(data))
		j.Plan.CheckInputs = append(j.Plan.CheckInputs, Input{path, inputHash(data)})
	}
	if err := Save(directory, &j); err != nil {
		t.Fatal("legacy initial check manifest rejected", err)
	}
	saved, err := Load(directory)
	if err != nil || len(saved.Plan.CheckInputs) != 256 || len(saved.Plan.Inputs) != 1 {
		t.Fatal("legacy 256 check inputs plus ticket cannot load", err)
	}
	before := saved.UpdatedAt
	sourceWrite(t, saved.Plan.Root, "extra-check.txt", "offline extra")
	if _, err := AttachCheckInputs(context.Background(), directory, &saved, []string{"extra-check.txt"}, false); err == nil {
		t.Fatal("check manifest exceeded its own 256-input bound")
	}
	if saved.PendingInputs != nil || !saved.UpdatedAt.Equal(before) {
		t.Fatal("rejected check addition changed checkpoint")
	}
	if _, err := AttachInputs(context.Background(), directory, &saved, []string{providerPath}, false); err != nil {
		t.Fatal("check boundary incorrectly blocked separate provider manifest", err)
	}
	if len(saved.Plan.Inputs) != 2 || len(saved.Plan.CheckInputs) != 256 {
		t.Fatal("separate destination bounds changed")
	}
}
