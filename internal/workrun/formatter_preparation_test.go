package workrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/tjpeel/sdlc/internal/runprogress"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func formatterGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.invalid", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.invalid")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}
func formatterRepository(t *testing.T, count int) (*Journal, Revision) {
	t.Helper()
	root := t.TempDir()
	formatterGit(t, root, "init", "-q")
	os.MkdirAll(filepath.Join(root, ".config"), 0700)
	os.WriteFile(filepath.Join(root, ".config/dotnet-tools.json"), []byte(`{"version":1,"tools":{"csharpier":{"version":"1.3.0","commands":["csharpier"]}}}`), 0600)
	os.WriteFile(filepath.Join(root, "Unchanged.cs"), []byte("baseline"), 0600)
	formatterGit(t, root, "add", ".")
	formatterGit(t, root, "commit", "-qm", "Baseline")
	base := formatterGit(t, root, "rev-parse", "HEAD")
	for i := 0; i < count; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("Added%02d.cs", i)), []byte("new source"), 0600)
	}
	os.WriteFile(filepath.Join(root, "-option.cs"), []byte("new source"), 0600)
	formatterGit(t, root, "add", ".")
	formatterGit(t, root, "commit", "-qm", "Candidate")
	revision := Revision{Head: formatterGit(t, root, "rev-parse", "HEAD"), Tree: formatterGit(t, root, "rev-parse", "HEAD^{tree}"), Clean: true}
	return &Journal{Workspace: root, Plan: Plan{SourceSHA: base, StartingSHA: base, Checks: [][]string{{"legacy", "suite"}}}}, revision
}
func TestFormatterPreparationCommittedChangedSelectionAndRootBatches(t *testing.T) {
	journal, revision := formatterRepository(t, 17)
	requests, err := preparationRequests(context.Background(), journal, revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(requests[0].SourcePatch.Paths) != 16 || len(requests[1].SourcePatch.Paths) != 2 {
		t.Fatalf("incorrect batches: %+v", requests)
	}
	for _, request := range requests {
		if !reflect.DeepEqual(request.Commands[0], []string{"dotnet", "tool", "restore"}) || !reflect.DeepEqual(request.Commands[1][:3], []string{"dotnet", "csharpier", "format"}) {
			t.Fatalf("unexpected commands: %+v", request)
		}
		for i, path := range request.SourcePatch.Paths {
			if path == "Unchanged.cs" || request.Commands[1][i+3] != "./"+path {
				t.Fatalf("unsafe source argv: %+v", request)
			}
		}
	}
	if requests[0].SourcePatch.Paths[0] != "-option.cs" {
		t.Fatal("leading-dash source not covered")
	}
	if !reflect.DeepEqual(journal.Plan.Checks, [][]string{{"legacy", "suite"}}) {
		t.Fatal("frozen suite changed")
	}
	journal.Plan.Inputs = []Input{{Path: requests[0].SourcePatch.Paths[0]}}
	if _, err := preparationRequests(context.Background(), journal, revision); err == nil {
		t.Fatal("frozen input selected for correction")
	}
}
func TestFormatterPreparationNoPolicyAndInvalidMetadata(t *testing.T) {
	journal, revision := formatterRepository(t, 0)
	os.WriteFile(filepath.Join(journal.Workspace, ".config/dotnet-tools.json"), []byte(`{"version":1,"tools":{"csharpier":{"version":"2.0.0","commands":["csharpier"]}}}`), 0600)
	// Uncommitted metadata does not change discovery.
	if requests, err := preparationRequests(context.Background(), journal, revision); err != nil || len(requests) != 1 {
		t.Fatalf("read uncommitted metadata: %v", err)
	}
	formatterGit(t, journal.Workspace, "add", ".")
	formatterGit(t, journal.Workspace, "commit", "-qm", "Unsupported formatter")
	revision.Head = formatterGit(t, journal.Workspace, "rev-parse", "HEAD")
	revision.Tree = formatterGit(t, journal.Workspace, "rev-parse", "HEAD^{tree}")
	if _, err := preparationRequests(context.Background(), journal, revision); err == nil {
		t.Fatal("unsupported formatter invented commands")
	}
	os.WriteFile(filepath.Join(journal.Workspace, ".config/dotnet-tools.json"), nil, 0600)
	formatterGit(t, journal.Workspace, "add", ".")
	formatterGit(t, journal.Workspace, "commit", "-qm", "Empty formatter metadata")
	revision.Head = formatterGit(t, journal.Workspace, "rev-parse", "HEAD")
	if _, err := preparationRequests(context.Background(), journal, revision); err == nil {
		t.Fatal("empty committed formatter metadata silently bypassed discovery")
	}
	os.Remove(filepath.Join(journal.Workspace, ".config/dotnet-tools.json"))
	formatterGit(t, journal.Workspace, "add", ".")
	formatterGit(t, journal.Workspace, "commit", "-qm", "No formatter")
	revision.Head = formatterGit(t, journal.Workspace, "rev-parse", "HEAD")
	if requests, err := preparationRequests(context.Background(), journal, revision); err != nil || len(requests) != 0 {
		t.Fatalf("no formatter changed ordinary checks: %v", err)
	}
}

type preparationRepositoryFake struct {
	fakeRepository
	requests    []VerificationRequest
	discoveries int
}

func (r *preparationRepositoryFake) FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error) {
	r.discoveries++
	return r.requests, nil
}
func preparationFixture(t *testing.T, diff string) (string, *Journal, Runner, *patchFixtureChecker, *preparationRepositoryFake) {
	directory, journal := testRun(t)
	journal.State = "checking"
	request := sourcePatchRequest()
	request.ID = "formatter-csharpier-1"
	checker := &patchFixtureChecker{patch: patchFixtureResult(diff)}
	repository := &preparationRepositoryFake{requests: []VerificationRequest{request}}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &repository.fakeRepository)
	runner.Repository = repository
	runner.Checker = checker
	runner.rawOutput = io.Discard
	recorder, err := runprogress.Open(directory, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recorder.Close() })
	runner.progress = recorder
	runner.MaxRounds = 3
	return directory, journal, runner, checker, repository
}
func TestFormatterPreparationCorrectionPrecedesSuiteAndOwnsEvidence(t *testing.T) {
	directory, journal, runner, checker, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 0 || checker.patchCalls != 1 || journal.State != "repairing" || journal.Rounds != 1 || len(journal.Verification) != 0 {
		t.Fatal("correction bypassed suite gate or consumed agent slots")
	}
	if !strings.Contains(journal.Feedback, "source_patch") {
		t.Fatal("correction feedback missing")
	}
	if err := mergeVerification(journal, []VerificationRequest{journal.FormatterPreparation[0].Request}); err != nil {
		t.Fatal(err)
	}
	if len(journal.FormatterPreparation) != 1 || len(journal.Verification) != 1 {
		t.Fatal("agent replaced controller evidence")
	}
	loaded, err := Load(directory)
	if err != nil || len(loaded.FormatterPreparation) != 1 {
		t.Fatalf("preparation checkpoint lost: %v", err)
	}
	journal.State = "checking"
	if ready, err := runner.prepareCandidate(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil || ready || checker.patchCalls != 2 || journal.Verification[0].SourcePatch == nil {
		t.Fatal("agent request did not remain independent from unchanged preparation")
	}
}
func TestFormatterPreparationEmptyEvidenceReuseAndFreshCandidate(t *testing.T) {
	directory, journal, runner, checker, repository := preparationFixture(t, "")
	revision := Revision{Head: testHead, Tree: testTree, Clean: true}
	for i := 0; i < 2; i++ {
		if ready, err := runner.prepareCandidate(context.Background(), directory, journal, revision); err != nil || !ready {
			t.Fatalf("empty prep held suite: %v", err)
		}
	}
	if checker.patchCalls != 1 || journal.Rounds != 0 {
		t.Fatal("unchanged empty evidence not reused")
	}
	revision.Head = testSigned
	repository.revisions = []Revision{revision}
	checker.patch.Head = testSigned
	if ready, err := runner.prepareCandidate(context.Background(), directory, journal, revision); err != nil || !ready || checker.patchCalls != 2 {
		t.Fatalf("changed candidate reused prep: %v", err)
	}
}
func TestFormatterPreparationMalformedArtifactAndCleanupRemainHolds(t *testing.T) {
	for _, kind := range []string{"digest", "stale", "oversize", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			directory, journal, runner, checker, _ := preparationFixture(t, "")
			switch kind {
			case "digest":
				checker.patch.DiffSHA256 = "invalid"
			case "stale":
				checker.patch.Head = testSigned
			case "oversize":
				checker.patch.Diff = strings.Repeat("x", maxSourcePatchDiff+1)
			case "cleanup":
				checker.patchErr = errors.New("cleanup failed")
			}
			ready, err := runner.prepareCandidate(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true})
			if ready || !errors.Is(err, ErrStopped) || journal.Evidence.Passed || len(journal.FormatterPreparation) != 0 || checker.calls != 0 {
				t.Fatal("invalid artifact granted passing evidence")
			}
		})
	}
}

func TestFormatterPreparationNoChangedSupportedSource(t *testing.T) {
	journal, revision := formatterRepository(t, 0)
	journal.Plan.SourceSHA = revision.Head
	if requests, err := preparationRequests(context.Background(), journal, revision); err != nil || len(requests) != 0 {
		t.Fatalf("unchanged source requested preparation: %v", err)
	}
	os.RemoveAll(filepath.Join(journal.Workspace, ".git"))
	if _, err := preparationRequests(context.Background(), journal, revision); err == nil {
		t.Fatal("removed Git metadata granted discovery pass")
	}
}
func TestFormatterPreparationSemanticFailureReturnsRepairAndReusesEvidence(t *testing.T) {
	for _, failure := range []error{ErrCheckFailed, &sourcePatchDiffBudgetError{}} {
		t.Run(failure.Error(), func(t *testing.T) {
			directory, journal, runner, checker, _ := preparationFixture(t, "")
			checker.patchErr = failure
			revision := Revision{Head: testHead, Tree: testTree, Clean: true}
			if err := runner.checkCandidatePhase(context.Background(), directory, journal, revision); err != nil {
				t.Fatal(err)
			}
			if journal.State != "repairing" || journal.Rounds != 1 || checker.calls != 0 || len(journal.FormatterPreparation) != 1 || journal.FormatterPreparation[0].Passed || journal.FormatterPreparation[0].SourcePatch != nil {
				t.Fatal("semantic failure became pass or operational hold")
			}
			if loaded, err := Load(directory); err != nil || len(loaded.FormatterPreparation) != 1 {
				t.Fatalf("failed prep checkpoint lost: %v", err)
			}
			if ready, err := runner.prepareCandidate(context.Background(), directory, journal, revision); err != nil || ready || checker.patchCalls != 1 {
				t.Fatal("unchanged semantic failure reran")
			}
		})
	}
}
func TestFormatterPreparationInterruptedPhaseInvalidatesEvidence(t *testing.T) {
	directory, journal, runner, checker, _ := preparationFixture(t, "")
	revision := Revision{Head: testHead, Tree: testTree, Clean: true}
	if ready, err := runner.prepareCandidate(context.Background(), directory, journal, revision); err != nil || !ready {
		t.Fatal(err)
	}
	journal.CandidatePhasePending = true
	journal.Outcome.Status = "checks_requested"
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, revision); err != nil {
		t.Fatal(err)
	}
	if checker.patchCalls != 2 {
		t.Fatal("interrupted cleanup reused provisional preparation")
	}
}
func TestFormatterPreparationStaleResumeDropsArtifactAndRetainsSession(t *testing.T) {
	directory, journal, runner, _, repository := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	if ready, err := runner.prepareCandidate(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil || ready {
		t.Fatal(err)
	}
	before := journal.SessionID
	repository.revisions = []Revision{{Head: testSigned, Tree: testTree, Clean: true}}
	if err := runner.validateSourcePatchResume(context.Background(), directory, journal); err != nil {
		t.Fatal(err)
	}
	if len(journal.FormatterPreparation) != 0 || journal.SessionID != before || strings.Contains(journal.Feedback, "+after") {
		t.Fatal("stale preparation leaked or changed session")
	}
	if err := Save(directory, journal); err != nil {
		t.Fatal(err)
	}
}

type preparationRecoveryChecker struct {
	fakeChecker
	preparationCalls, diagnosticCalls, phases int
	overBudget                                bool
	cleanupErr                                error
}

func (c *preparationRecoveryChecker) VerifySourcePatch(_ context.Context, _ string, request VerificationRequest, head, tree string, _ io.Writer) (*SourcePatchResult, error) {
	if strings.HasPrefix(request.ID, "formatter-") {
		c.preparationCalls++
		if c.overBudget {
			return nil, &sourcePatchDiffBudgetError{}
		}
	} else {
		c.diagnosticCalls++
	}
	diff := ""
	if c.overBudget {
		diff = "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n"
	}
	result := patchFixtureResult(diff)
	result.Head, result.Tree = head, tree
	return result, nil
}
func (c *preparationRecoveryChecker) WithCandidate(_ context.Context, _ string, _ io.Writer, callback func(Checker) error) error {
	c.phases++
	return errors.Join(callback(c), c.cleanupErr)
}
func TestFormatterPreparationBudgetFailureAllowsSmallerAgentCorrectionBeforeSuite(t *testing.T) {
	directory, journal, runner, _, repository := preparationFixture(t, "")
	checker := &preparationRecoveryChecker{overBudget: true}
	runner.Checker = checker
	revision := Revision{Head: testHead, Tree: testTree, Clean: true}
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, revision); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 0 || checker.phases != 0 || journal.State != "repairing" {
		t.Fatal("budget failure ran configured suite")
	}
	smaller := sourcePatchRequest()
	smaller.ID = "smaller-correction"
	if err := mergeVerification(journal, []VerificationRequest{smaller}); err != nil {
		t.Fatal(err)
	}
	journal.State = "checking"
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, revision); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 0 || checker.preparationCalls != 1 || checker.diagnosticCalls != 1 || checker.phases != 1 || journal.State != "repairing" || journal.Verification[0].SourcePatch == nil || journal.Verification[0].Passed {
		t.Fatal("pending source correction was stranded behind preparation")
	}
	if !strings.Contains(journal.Feedback, "+after") || journal.CandidatePhasePending {
		t.Fatal("diagnostic correction was not returned after cleanup")
	}
	checker.overBudget = false
	revision.Head = testSigned
	repository.revisions = []Revision{revision}
	journal.State = "checking"
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, revision); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 || checker.preparationCalls != 2 || !journal.Evidence.Passed || !journal.FormatterPreparation[0].Passed {
		t.Fatal("committed correction did not receive fresh preparation and full suite")
	}
}
func TestFormatterPreparationDiagnosticCleanupFailureRemainsHold(t *testing.T) {
	directory, journal, runner, _, _ := preparationFixture(t, "")
	checker := &preparationRecoveryChecker{overBudget: true, cleanupErr: errors.New("cleanup failed")}
	runner.Checker = checker
	smaller := sourcePatchRequest()
	smaller.ID = "smaller-correction"
	if err := mergeVerification(journal, []VerificationRequest{smaller}); err != nil {
		t.Fatal(err)
	}
	err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true})
	if !errors.Is(err, ErrStopped) || journal.State != "blocked" || journal.Evidence.Passed || len(journal.FormatterPreparation) != 0 || journal.Verification[0].SourcePatch != nil || !journal.CandidatePhasePending || checker.calls != 0 {
		t.Fatal("diagnostic cleanup failure retained passing preparation or artifact")
	}
}

func TestFormatterPreparationJournalRejectsInvalidAndAggregateArtifacts(t *testing.T) {
	directory, journal, runner, checker, _ := preparationFixture(t, "")
	if ready, err := runner.prepareCandidate(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil || !ready {
		t.Fatal(err)
	}
	journal.FormatterPreparation[0].SourcePatch.DiffSHA256 = "invalid"
	if err := Save(directory, journal); err == nil {
		t.Fatal("invalid preparation artifact checkpoint admitted")
	}
	journal.FormatterPreparation = nil
	checker.patch = patchFixtureResult(strings.Repeat("x", 30*1024))
	for i := 0; i < 5; i++ {
		request := sourcePatchRequest()
		request.ID = fmt.Sprintf("formatter-csharpier-%d", i)
		journal.FormatterPreparation = append(journal.FormatterPreparation, VerificationResult{Request: request, Head: testHead, Tree: testTree, Key: verificationKey(journal, request, testHead, testTree), SourcePatch: checker.patch})
	}
	if err := validateSourcePatchJournal(journal); err == nil {
		t.Fatal("aggregate preparation artifact cap bypassed")
	}
	invalidateCommandEvidence(journal)
	if len(journal.FormatterPreparation) != 0 {
		t.Fatal("preparation survived command evidence invalidation")
	}
}

func TestFormatterPreparationUsesFrozenRootConfiguration(t *testing.T) {
	journal, revision := formatterRepository(t, 0)
	journal.Plan.Checks = [][]string{{"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "tool", "restore", "--verbosity", "quiet"}, {"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "csharpier", "check", ".", "--config-path", ".config/csharpier.json", "--ignore-path", ".config/csharpierignore", "--no-cache"}, {"expensive", "check"}}
	requests, err := preparationRequests(context.Background(), journal, revision)
	if err != nil || len(requests) != 1 {
		t.Fatal(err)
	}
	expected := []string{"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "csharpier", "format", "--config-path", ".config/csharpier.json", "--ignore-path", ".config/csharpierignore", "./-option.cs"}
	if !reflect.DeepEqual(requests[0].Commands[0], journal.Plan.Checks[0]) || !reflect.DeepEqual(requests[0].Commands[1], expected) {
		t.Fatalf("frozen root policy dropped: %v", requests[0].Commands)
	}
}

type noisyPreparationChecker struct{ patchFixtureChecker }

func (c *noisyPreparationChecker) VerifySourcePatch(_ context.Context, _ string, _ VerificationRequest, head, tree string, output io.Writer) (*SourcePatchResult, error) {
	c.patchCalls++
	collector := output.(*commandOutputCollector)
	for i := 1; i <= 2; i++ {
		capture := collector.commandOutput(i, false).(*boundedCommandOutput)
		capture.Write([]byte(strings.Repeat("x", 64*1024)))
		capture.completed(0)
	}
	patch := patchFixtureResult("")
	patch.Head, patch.Tree = head, tree
	return patch, nil
}
func TestFormatterPreparationBoundsNoisyBatchesBeforeIntermediateSave(t *testing.T) {
	directory, journal, runner, _, repository := preparationFixture(t, "")
	for i := 0; i < 3; i++ {
		request := sourcePatchRequest()
		request.ID = fmt.Sprintf("formatter-csharpier-%d", i+1)
		if i == 0 {
			repository.requests = nil
		}
		repository.requests = append(repository.requests, request)
	}
	checker := &noisyPreparationChecker{}
	runner.Checker = checker
	ready, err := runner.prepareCandidate(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true})
	if err != nil || !ready || checker.patchCalls != 3 {
		t.Fatalf("noisy batch failed intermediate checkpoint: %v", err)
	}
	if err := validateCommandEvidence(journal); err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, evidence := range journal.FormatterPreparation {
		for _, output := range evidence.Output {
			retained += output.HeadBytes + output.TailBytes
		}
	}
	if retained > 16*1024 {
		t.Fatal("preparation exceeded shared command feedback budget")
	}
}

func TestFormatterRepairLimitDefaultAndPersistedOverride(t *testing.T) {
	for _, overridden := range []bool{false, true} {
		t.Run(fmt.Sprint(overridden), func(t *testing.T) {
			directory, j, runner, checker, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
			j.SessionID = testNative
			j.Rounds = runner.MaxRounds
			j.ConfiguredRepairBudget = &ConfiguredRepairBudget{BaselineAt: time.Now().UTC(), Used: runner.MaxRounds}
			if err := Save(directory, j); err != nil {
				t.Fatal(err)
			}
			if overridden {
				overrideGrant(j, "verification_requests", 16)
			}
			_, err := runner.prepareCandidate(context.Background(), directory, j, Revision{Head: testHead, Tree: testTree, Clean: true})
			if overridden {
				if err != nil || j.State != "repairing" || j.Rounds != runner.MaxRounds+1 {
					t.Fatal("persisted grant did not reach repair", err)
				}
			} else {
				if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "checking" || j.StopReason != configuredCheckRepairBound {
					t.Fatal("default formatter limit missing", err)
				}
				if err := ValidateRunLimitOverride(*j); err != nil {
					t.Fatal("formatter numeric hold not admitted", err)
				}
				invalid := *j
				invalid.StopReason = "formatter preparation could not complete; inspect its private log"
				if ValidateRunLimitOverride(invalid) == nil {
					t.Fatal("operational formatter hold admitted")
				}
				invalid = *j
				invalid.FormatterPreparation = append([]VerificationResult(nil), j.FormatterPreparation...)
				invalid.FormatterPreparation[0].Key = "stale"
				if ValidateRunLimitOverride(invalid) == nil {
					t.Fatal("stale formatter evidence admitted")
				}
			}
			if checker.calls != 0 || checker.patchCalls != 1 {
				t.Fatal("full suite ran before committed repair")
			}
		})
	}
}

func TestFormatterNumericHoldAdmitsCompletedSemanticFailure(t *testing.T) {
	directory, j, runner, _, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	j.SessionID = testNative
	j.Rounds = runner.MaxRounds
	j.ConfiguredRepairBudget = &ConfiguredRepairBudget{BaselineAt: time.Now().UTC(), Used: runner.MaxRounds}
	if _, err := runner.prepareCandidate(context.Background(), directory, j, Revision{Head: testHead, Tree: testTree, Clean: true}); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	j.FormatterPreparation[0].SourcePatch = nil
	j.FormatterPreparation[0].Diagnostic = "Formatter preparation commands failed."
	if err := ValidateRunLimitOverride(*j); err != nil {
		t.Fatal("completed semantic formatter failure rejected", err)
	}
	j.FormatterPreparation[0].Diagnostic = ""
	if ValidateRunLimitOverride(*j) == nil {
		t.Fatal("empty failure evidence admitted")
	}
}

func TestRunnerResumesFormatterHoldWithOriginalPersistedGrant(t *testing.T) {
	directory, j, runner, checker, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	j.SessionID = testNative
	j.Rounds = runner.MaxRounds
	j.ConfiguredRepairBudget = &ConfiguredRepairBudget{BaselineAt: time.Now().UTC(), Used: runner.MaxRounds}
	j.Attempt = 26
	j.Outcome = testOutcome("implemented")
	if _, err := runner.prepareCandidate(context.Background(), directory, j, Revision{Head: testHead, Tree: testTree, Clean: true}); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	overrideGrant(j, "verification_requests", 16)
	j.SessionTurnExtension = &SessionTurnExtension{GrantedAt: j.RunLimitOverride.GrantedAt, GrantedAttempt: 24, OriginalLimit: 24, Additional: 6, OriginalSessionID: testNative}
	for i := 0; i < 17; i++ {
		request := VerificationRequest{ID: fmt.Sprintf("offline-%d", i), Purpose: "Check fixture", Commands: [][]string{{"test", "fixture"}}}
		j.Verification = append(j.Verification, VerificationResult{Request: request, Key: verificationKey(j, request, testHead, testTree), Head: testHead, Tree: testTree, Passed: true})
	}
	grant := *j.RunLimitOverride
	if err := Save(directory, j); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{executeErr: errors.New("offline native boundary")}
	runner.Provider = provider
	if err := runner.Run(context.Background(), directory, &saved, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.calls[0].ResumeID != testNative || saved.SessionID != testNative || saved.Attempt != 27 || saved.Rounds != runner.MaxRounds+2 || saved.RepairWindowStart != 0 {
		t.Fatal("ordinary resume changed original session or repair accounting")
	}
	if !reflect.DeepEqual(grant, *saved.RunLimitOverride) || saved.SessionTurnExtension.Additional != 6 || checker.calls != 0 || checker.patchCalls != 1 || len(saved.FormatterPreparation) != 1 || saved.FormatterPreparation[0].SourcePatch == nil {
		t.Fatal("resume lost persisted grant/artifact or ran full checks before commit")
	}
}
