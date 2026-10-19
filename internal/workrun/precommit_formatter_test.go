package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func captureFormatterSnapshot(t *testing.T, root string) (string, Revision, error) {
	t.Helper()
	destination := t.TempDir()
	script := strings.ReplaceAll(formatterSnapshotHelper, "'/workspace'", strconv.Quote(root))
	script = strings.ReplaceAll(script, "'/export'", strconv.Quote(destination))
	cmd := exec.Command("python3", "-I", "-c", script)
	data, err := cmd.Output()
	var revision Revision
	if err == nil {
		err = json.Unmarshal(data, &revision)
	}
	return filepath.Join(destination, "workspace"), revision, err
}

func TestPendingFormatterSnapshotPreservesHeadIndexAndEdits(t *testing.T) {
	journal, _ := formatterRepository(t, 0)
	root := journal.Workspace
	beforeHead := formatterGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "Unchanged.cs"), []byte("class Edited{ }"), 0600); err != nil {
		t.Fatal(err)
	}
	formatterGit(t, root, "add", "Unchanged.cs")
	beforeIndex := formatterGit(t, root, "write-tree")
	beforeObjects := formatterGit(t, root, "count-objects", "-v")
	if err := os.WriteFile(filepath.Join(root, "New.cs"), []byte("class New{ }"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private.env"), []byte("synthetic-private-data"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, revision, err := captureFormatterSnapshot(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if formatterGit(t, root, "count-objects", "-v") != beforeObjects {
		t.Fatal("snapshot wrote objects into the read-only source repository")
	}
	if formatterGit(t, root, "rev-parse", "HEAD") != beforeHead || formatterGit(t, root, "write-tree") != beforeIndex {
		t.Fatal("snapshot changed HEAD or index")
	}
	if formatterGit(t, snapshot, "show", "HEAD:New.cs") != "class New{ }" || formatterGit(t, snapshot, "show", "HEAD:Unchanged.cs") != "class Edited{ }" {
		t.Fatal("pending files absent")
	}
	if strings.Contains(formatterGit(t, snapshot, "ls-tree", "-r", "--name-only", "HEAD"), "private.env") {
		t.Fatal("unrelated untracked data captured")
	}
	shadow := *journal
	shadow.Workspace = snapshot
	shadow.Plan = journal.Plan
	shadow.Plan.SourceSHA = beforeHead
	requests, err := preparationRequests(context.Background(), &shadow, revision)
	if err != nil || len(requests) != 1 || len(requests[0].SourcePatch.Paths) != 2 {
		t.Fatal("snapshot cannot reach supported formatter", requests, err)
	}
	if revision.Head == beforeHead || !revision.Clean {
		t.Fatal("not a private clean snapshot", revision)
	}
	_, again, err := captureFormatterSnapshot(t, root)
	if err != nil || again.Tree != revision.Tree || again.Head != revision.Head {
		t.Fatal("snapshot identity unstable", again, err)
	}
}

func TestPendingFormatterSnapshotRejectsSymlinkSource(t *testing.T) {
	journal, _ := formatterRepository(t, 0)
	if err := os.Symlink("Unchanged.cs", filepath.Join(journal.Workspace, "Linked.cs")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureFormatterSnapshot(t, journal.Workspace); err == nil {
		t.Fatal("symlink source accepted")
	}
}

func TestAutomaticFormattingStopsStructuredDiagnosticsBeforeBuild(t *testing.T) {
	directory, journal, runner, checker, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	journal.Outcome.Status = "diagnostics_requested"
	journal.Outcome.DiagnosticRequests = []VerificationRequest{diagnosticRequest("build")}
	readyErr := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true})
	if readyErr != nil && !errors.Is(readyErr, ErrStopped) {
		t.Fatal(readyErr)
	}
	if checker.calls != 0 || len(journal.Diagnostics) != 0 || journal.State != "repairing" {
		t.Fatal("build ran before formatter correction", checker.calls, journal.State)
	}
}

func TestAutomaticFormattingStopsLiveDiagnosticsBeforeBuild(t *testing.T) {
	directory, journal, runner, checker, _ := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	journal.State = "implementing"
	response, err := runner.liveDiagnostics(context.Background(), directory, journal, liveRequest("build"))
	if err != nil || response.Error == "" || len(response.Results) != 1 || checker.calls != 0 || journal.State != "implementing" || journal.Evidence.Passed {
		t.Fatal("live build ran or state changed", response, err, journal.State)
	}
}

func TestFormatOnlyEnvelopeCannotRunArbitraryRequests(t *testing.T) {
	request := liveRequest("build")
	request.FormatOnly = true
	if validateLiveRequest(request, &Plan{}) == nil {
		t.Fatal("format mode accepted commands")
	}
	request.Requests = nil
	if err := validateLiveRequest(request, &Plan{}); err != nil {
		t.Fatal(err)
	}
}

type pendingFormatterFake struct {
	preparationRepositoryFake
	snapshots int
	stale     bool
}

func (r *pendingFormatterFake) FormatterSnapshot(context.Context, string) (string, Revision, func(), error) {
	r.snapshots++
	revision := Revision{Head: testHead, Tree: testTree, Clean: true}
	if r.stale && r.snapshots > 1 {
		revision.Tree = strings.Repeat("b", 40)
	}
	return "private-snapshot", revision, func() {}, nil
}

func TestPrecommitFormattingIsDetachedAndRejectsStaleEdits(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(strconv.FormatBool(stale), func(t *testing.T) {
			directory, j, runner, _, repository := preparationFixture(t, "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
			j.State = "implementing"
			originalPlan, _ := json.Marshal(j.Plan)
			originalEvidence, _ := json.Marshal(j.Evidence)
			fake := &pendingFormatterFake{preparationRepositoryFake: *repository, stale: stale}
			runner.Repository = fake
			request := liveRequest()
			request.FormatOnly = true
			response, err := runner.liveDiagnostics(context.Background(), directory, j, request)
			if err != nil {
				t.Fatal(err)
			}
			afterPlan, _ := json.Marshal(j.Plan)
			afterEvidence, _ := json.Marshal(j.Evidence)
			if string(originalPlan) != string(afterPlan) || string(originalEvidence) != string(afterEvidence) || j.State != "implementing" || len(j.FormatterPreparation) != 0 {
				t.Fatal("private snapshot altered frozen inputs or readiness gates")
			}
			if stale {
				if response.Error == "" || len(response.Results) != 0 {
					t.Fatal("stale edits retained corrections", response)
				}
			} else if response.Error != "" || response.FormatBaseHead != testHead || len(response.Results) != 1 || response.Results[0].SourcePatch == nil {
				t.Fatal("precommit corrections missing", response)
			}
			loaded, err := Load(directory)
			if err != nil || loaded.Plan.SourceSHA != j.Plan.SourceSHA || loaded.Workspace != j.Workspace {
				t.Fatal("snapshot metadata replaced run plan", err)
			}
		})
	}
}

func TestLiveHelperFormatsDirtyEditsWithoutCheckpointCommit(t *testing.T) {
	if !liveDiagnosticsSupported {
		t.Skip("live helper unavailable")
	}
	helper, requests, responses, workspace := helperFixture(t)
	head := formatterGit(t, workspace, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workspace, "New.cs"), []byte("class Pending{}"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan LiveDiagnosticRequest, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveDiagnosticMailbox(ctx, requests, responses, func(_ context.Context, r LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
			received <- r
			return LiveDiagnosticResponse{FormatBaseHead: r.Head}, nil
		})
	}()
	command := exec.CommandContext(ctx, "python3", "-I", helper, "--format")
	data, err := command.CombinedOutput()
	if err != nil {
		cancel()
		t.Fatal(string(data), err)
	}
	var response LiveDiagnosticResponse
	if json.Unmarshal(data, &response) != nil || response.Error != "" || response.FormatBaseHead != head {
		t.Fatal(string(data))
	}
	request := <-received
	if !request.FormatOnly || len(request.Requests) != 0 || request.Head != head || formatterGit(t, workspace, "rev-parse", "HEAD") != head {
		t.Fatal("helper created checkpoint or sent arbitrary commands", request)
	}
	cancel()
	<-done
}

func TestFormatterCorrectionRecoveryDefersGenericVerification(t *testing.T) {
	directory, journal, runner, _, _ := preparationFixture(t, "")
	checker := &preparationRecoveryChecker{overBudget: true}
	runner.Checker = checker
	correction := sourcePatchRequest()
	correction.ID = "smaller-correction"
	if err := mergeVerification(journal, []VerificationRequest{correction, diagnosticRequest("build")}); err != nil {
		t.Fatal(err)
	}
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 0 || checker.diagnosticCalls != 1 || len(journal.Verification) != 2 || journal.Verification[0].SourcePatch == nil || journal.Verification[1].Head != "" {
		t.Fatal("format recovery ran or discarded generic verification")
	}
}

type splitPendingChecker struct {
	fakeChecker
	patches int
}

func (c *splitPendingChecker) WithCandidate(context.Context, string, io.Writer, func(Checker) error) error {
	return errors.New("formatter started generic test infrastructure")
}

func (c *splitPendingChecker) VerifySourcePatch(_ context.Context, _ string, r VerificationRequest, head, tree string, _ io.Writer) (*SourcePatchResult, error) {
	c.patches++
	if len(r.SourcePatch.Paths) > 1 {
		return nil, &sourcePatchDiffBudgetError{}
	}
	patch := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	patch.Head = head
	patch.Tree = tree
	return patch, nil
}
func TestPendingFormatterSplitsOversizedCorrectionsWithoutCommitting(t *testing.T) {
	directory, j, runner, _, repo := preparationFixture(t, "")
	request := repo.requests[0]
	request.SourcePatch = &SourcePatchRequest{Paths: []string{"src/Example.cs", "src/Other.cs"}}
	request.Commands = [][]string{{"dotnet", "tool", "restore"}, {"dotnet", "csharpier", "format", "./src/Example.cs", "./src/Other.cs"}}
	repo.requests = []VerificationRequest{request}
	checker := &splitPendingChecker{}
	runner.Checker = checker
	results, corrections, err := runner.formatterResults(context.Background(), directory, j, "private-snapshot", Revision{Head: testHead, Tree: testTree, Clean: true}, false)
	if err != nil || !corrections || len(results) != 1 || len(results[0].Request.SourcePatch.Paths) != 1 || checker.patches != 2 || len(j.FormatterPreparation) != 0 {
		t.Fatal("oversized pending correction did not split safely", results, err)
	}
}

func TestPendingFormatterSnapshotHandlesRemovedSourceDirectory(t *testing.T) {
	journal, _ := formatterRepository(t, 0)
	root := journal.Workspace
	if err := os.Mkdir(filepath.Join(root, "Removed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Removed", "Old.cs"), []byte("class Old{}"), 0600); err != nil {
		t.Fatal(err)
	}
	formatterGit(t, root, "add", ".")
	formatterGit(t, root, "commit", "-qm", "Old source")
	if err := os.RemoveAll(filepath.Join(root, "Removed")); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := captureFormatterSnapshot(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(formatterGit(t, snapshot, "ls-tree", "-r", "--name-only", "HEAD"), "Removed/") {
		t.Fatal("deleted source was restored in private snapshot")
	}
}
