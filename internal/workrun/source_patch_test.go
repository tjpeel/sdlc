package workrun

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
	"strings"
	"testing"
)

func TestSourcePatchRequestAcceptedWithoutBaseline(t *testing.T) {
	var outcome Outcome
	data := `{"verification_requests":[{"id":"formatter","purpose":"review isolated source correction","commands":[["formatter","src/Example.cs"]],"baseline":null,"source_patch":{"paths":["src/Example.cs"]}}]}`
	if err := json.Unmarshal([]byte(data), &outcome); err != nil {
		t.Fatalf("valid source correction request rejected: %v", err)
	}
	if err := validateVerificationRequests(outcome.VerificationRequests, nil); err != nil {
		t.Fatal(err)
	}
}

func sourcePatchRequest() VerificationRequest {
	return VerificationRequest{ID: "formatter", Purpose: "review isolated source correction", Commands: [][]string{{"formatter", "src/Example.cs"}}, SourcePatch: &SourcePatchRequest{Paths: []string{"src/Example.cs"}}}
}
func patchDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func patchFixtureResult(diff string) *SourcePatchResult {
	after := "before\n"
	if diff != "" {
		after = "after\n"
	}
	return &SourcePatchResult{Head: testHead, Tree: testTree, Files: []SourcePatchFile{{Path: "src/Example.cs", Before: patchDigest("before\n"), After: patchDigest(after)}}, Diff: diff, DiffSHA256: patchDigest(diff)}
}

type patchFixtureChecker struct {
	fakeChecker
	patch      *SourcePatchResult
	patchErr   error
	patchCalls int
}

func (checker *patchFixtureChecker) Verify(context.Context, string, VerificationRequest, io.Writer) error {
	return errors.New("generic verification must not receive source corrections")
}
func (checker *patchFixtureChecker) VerifySourcePatch(_ context.Context, _ string, _ VerificationRequest, _ string, _ string, output io.Writer) (*SourcePatchResult, error) {
	checker.patchCalls++
	io.WriteString(output, "untrusted-formatter-output\n")
	return checker.patch, checker.patchErr
}

func TestSourcePatchCorrectionResumesSameSessionWithoutPublicationOrSourceMutation(t *testing.T) {
	for _, configuredFailed := range []bool{false, true} {
		dir, journal := testRun(t)
		sourceWrite(t, journal.Workspace, "src/Example.cs", "before\n")
		asked := testOutcome("checks_requested")
		asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
		provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("waiting_for_human")}}
		checker := &patchFixtureChecker{patch: patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")}
		if configuredFailed {
			checker.fakeChecker.err = ErrCheckFailed
		}
		publisher := &fakePublisher{}
		runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
		runner.Checker = checker
		if err := runner.Run(context.Background(), dir, journal, ""); !errors.Is(err, ErrStopped) {
			t.Fatal(err)
		}
		if checker.patchCalls != 1 || publisher.calls != 0 || len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative || journal.Verification[0].Passed || journal.Verification[0].SourcePatch == nil {
			t.Fatal("source correction bypassed review or lost session")
		}
		prompt := provider.calls[1].Prompt
		if !strings.Contains(prompt, "source_patch") || !strings.Contains(prompt, "+after") || strings.Contains(prompt, "untrusted-formatter-output") {
			t.Fatal("private correction lost or raw output exported")
		}
		if configuredFailed && (journal.Evidence.Passed || !strings.Contains(prompt, "Configured checks failed")) {
			t.Fatal("source artifact replaced configured failure")
		}
		data, _ := os.ReadFile(filepath.Join(journal.Workspace, "src/Example.cs"))
		if string(data) != "before\n" {
			t.Fatal("implementation source mutated")
		}
		if saved, err := Load(dir); err != nil || saved.Verification[0].SourcePatch == nil || saved.Verification[0].Passed {
			t.Fatal("artifact checkpoint invalid", err)
		}
	}
}

func TestSourcePatchEmptyCorrectionPassesPersistentRequest(t *testing.T) {
	dir, journal := testRun(t)
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
	provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &patchFixtureChecker{patch: patchFixtureResult("")}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), dir, journal, ""); err != nil {
		t.Fatal(err)
	}
	if checker.patchCalls != 1 || !journal.Verification[0].Passed || publisher.calls != 1 {
		t.Fatal("empty correction did not pass or cache")
	}
}

type applyingPatchProvider struct {
	fakeProvider
	workspace  string
	repository *fakeRepository
	checker    *patchFixtureChecker
}

func (provider *applyingPatchProvider) Execute(ctx context.Context, session Session, output, diagnostic io.Writer) (SessionResult, error) {
	if len(provider.calls) == 1 {
		before, err := os.ReadFile(filepath.Join(provider.workspace, "src/Example.cs"))
		if err != nil || string(before) != "before\n" {
			return SessionResult{}, errors.New("controller changed source before worker review")
		}
		if err := os.WriteFile(filepath.Join(provider.workspace, "src/Example.cs"), []byte("after\n"), 0644); err != nil {
			return SessionResult{}, err
		}
		// Model the original worker's reviewed commit at the repository boundary.
		provider.repository.revisions = []Revision{{Head: testHead, Tree: testTree, Clean: true}}
		provider.checker.patch = patchFixtureResult("")
		provider.checker.patch.Files[0].Before = patchDigest("after\n")
		provider.checker.patch.Files[0].After = patchDigest("after\n")
	}
	return provider.fakeProvider.Execute(ctx, session, output, diagnostic)
}

func TestSourcePatchReviewedCommitRerunsSuiteBeforePublication(t *testing.T) {
	dir, journal := testRun(t)
	sourceWrite(t, journal.Workspace, "src/Example.cs", "before\n")
	initialHead, initialTree := strings.Repeat("4", 40), strings.Repeat("5", 40)
	repository := &fakeRepository{revisions: []Revision{{Head: initialHead, Tree: initialTree, Clean: true}}}
	artifact := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	artifact.Head, artifact.Tree = initialHead, initialTree
	checker := &patchFixtureChecker{fakeChecker: fakeChecker{failures: 1}, patch: artifact}
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
	provider := &applyingPatchProvider{fakeProvider: fakeProvider{outcomes: []Outcome{asked, testOutcome("implemented"), testOutcome("reviewed")}}, workspace: journal.Workspace, repository: repository, checker: checker}
	publisher := &fakePublisher{}
	runner := fakeRunner(&provider.fakeProvider, &checker.fakeChecker, publisher, repository)
	runner.Provider = provider
	runner.Checker = checker
	if err := runner.Run(context.Background(), dir, journal, ""); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 2 || checker.patchCalls != 2 || publisher.calls != 1 || journal.State != "ready" || journal.Evidence.Head != testHead || journal.Evidence.Tree != testTree || !journal.Evidence.Passed {
		t.Fatal("reviewed candidate did not receive fresh full suite verification")
	}
	if len(provider.calls) != 3 || provider.calls[1].ResumeID != testNative || journal.Verification[0].SourcePatch.Diff != "" || !journal.Verification[0].Passed || journal.Verification[0].Head != testHead {
		t.Fatal("persistent correction did not resolve in original native session")
	}
	if saved, err := Load(dir); err != nil || !saved.Verification[0].Passed || saved.Verification[0].SourcePatch.Diff != "" {
		t.Fatal("resolved correction checkpoint lost", err)
	}
}

func TestSourcePatchFailureAndStaleCandidateDiscardArtifacts(t *testing.T) {
	for _, scenario := range []string{"command", "cleanup", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			dir, journal := testRun(t)
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
			provider := &fakeProvider{outcomes: []Outcome{asked}}
			checker := &patchFixtureChecker{patch: patchFixtureResult("correction data")}
			checker.patchErr = errors.New("disposable cleanup failure")
			if scenario == "command" {
				checker.patchErr = ErrCheckFailed
			}
			if scenario == "stale" {
				checker.patchErr = nil
				checker.patch.Head = testBase
			}
			publisher := &fakePublisher{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
			runner.Checker = checker
			if err := runner.Run(context.Background(), dir, journal, ""); err == nil || journal.Verification[0].SourcePatch != nil || journal.Verification[0].Passed || publisher.calls != 0 {
				t.Fatal("failed or stale artifact exported", err)
			}
		})
	}
}

func TestSourcePatchInterruptedRepairDiscardsStaleFeedbackAndRetainsSession(t *testing.T) {
	for _, clean := range []bool{true, false} {
		dir, journal := testRun(t)
		journal.State = "repairing"
		journal.SessionID = testNative
		request := sourcePatchRequest()
		artifact := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
		journal.Verification = []VerificationResult{{Request: request, Head: testHead, Tree: testTree, SourcePatch: artifact}}
		journal.Feedback = "Preserve independent review recommendation and human answer.\n" + verificationFeedback(journal)
		if err := Save(dir, journal); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		provider := &fakeProvider{outcomes: []Outcome{testOutcome("waiting_for_human")}}
		publisher := &fakePublisher{}
		runner := fakeRunner(provider, &fakeChecker{}, publisher, &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testBase, Clean: clean}}})
		if err := runner.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
			t.Fatal(err)
		}
		if len(provider.calls) != 1 || provider.calls[0].ResumeID != testNative || loaded.Verification[0].SourcePatch != nil || publisher.calls != 0 {
			t.Fatal("stale resume changed session or exported artifact")
		}
		prompt := provider.calls[0].Prompt
		if strings.Contains(prompt, "+after") || !strings.Contains(prompt, "artifacts are stale") || !strings.Contains(prompt, "independent review recommendation and human answer") {
			t.Fatal("stale source data redelivered or unrelated feedback lost")
		}
	}
}

func TestSourcePatchJournalBoundsAndDigestProtectCheckpoint(t *testing.T) {
	dir, journal := testRun(t)
	for i := 0; i < 3; i++ {
		request := sourcePatchRequest()
		request.ID = fmt.Sprintf("formatter-%d", i)
		journal.Verification = append(journal.Verification, VerificationResult{Request: request, Head: testHead, Tree: testTree, SourcePatch: patchFixtureResult(strings.Repeat("x", maxSourcePatchDiff-100))})
	}
	if err := Save(dir, journal); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	request := sourcePatchRequest()
	request.ID = "formatter-overflow"
	journal.Verification = append(journal.Verification, VerificationResult{Request: request, Head: testHead, Tree: testTree, SourcePatch: patchFixtureResult(strings.Repeat("x", maxSourcePatchDiff-100))})
	if err := Save(dir, journal); err == nil {
		t.Fatal("aggregate artifact bound bypassed")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "journal.json")); !bytes.Equal(after, before) {
		t.Fatal("oversized artifact replaced checkpoint")
	}
	artifact := patchFixtureResult("patch data")
	artifact.Diff = "changed"
	if validateSourcePatchResult(artifact, sourcePatchRequest(), testHead, testTree) == nil {
		t.Fatal("altered diff digest accepted")
	}
}

func TestSourcePatchSelectionProtectsPrivateAndFrozenPaths(t *testing.T) {
	for _, p := range []string{"../src/Example.cs", ".git/config", ".sdlc/project.json", ".secrets/token", ".codex/auth.json", ".config/settings.json", ".env", "src/config.json", "src/Example.cs\n"} {
		request := sourcePatchRequest()
		request.SourcePatch.Paths = []string{p}
		if validateVerificationRequests([]VerificationRequest{request}, nil) == nil {
			t.Fatal("unsafe selection accepted", p)
		}
	}
	request := sourcePatchRequest()
	request.SourcePatch.Paths = append(request.SourcePatch.Paths, request.SourcePatch.Paths[0])
	if validateVerificationRequests([]VerificationRequest{request}, nil) == nil {
		t.Fatal("duplicate selection accepted")
	}
	request = sourcePatchRequest()
	request.Baseline = regressionRequest().Baseline
	if validateVerificationRequests([]VerificationRequest{request}, nil) == nil {
		t.Fatal("baseline and correction combined")
	}
	request = sourcePatchRequest()
	plan := Plan{CheckInputs: []Input{{Path: "src/Example.cs"}}}
	if validateVerificationRequests([]VerificationRequest{request}, &plan) == nil {
		t.Fatal("frozen input selected")
	}
	if err := (DockerChecker{}).Verify(context.Background(), "", request, io.Discard); err == nil {
		t.Fatal("generic Verify silently ignored artifact")
	}
}

func TestSourcePatchCollectorProducesApplicableTextDiff(t *testing.T) {
	for _, before := range []string{"before\n", "before\r\n", "before"} {
		t.Run(patchDigest(before)[:8], func(t *testing.T) {
			root, copy, head, tree := sourcePatchCollectorFixture(t, before)
			if err := os.WriteFile(filepath.Join(copy, "src/Example.cs"), []byte("after\n"), 0644); err != nil {
				t.Fatal(err)
			}
			result, err := runSourcePatchCollector(t, root, copy, head, tree)
			if err != nil || result.Diff == "" || validateSourcePatchResult(result, sourcePatchRequest(), head, tree) != nil {
				t.Fatal("source collector failed", err)
			}
			command := exec.Command("git", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", root, "apply", "--check", "-")
			command.Stdin = strings.NewReader(result.Diff)
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
			if data, err := command.CombinedOutput(); err != nil {
				t.Fatalf("invalid unified diff: %s %v", data, err)
			}
			if data, _ := os.ReadFile(filepath.Join(root, "src/Example.cs")); string(data) != before {
				t.Fatal("collector changed original source")
			}
		})
	}
}
func sourcePatchCollectorFixture(t *testing.T, before string) (string, string, string, string) {
	t.Helper()
	root, _ := sourceFixture(t)
	sourceWrite(t, root, "src/Example.cs", before)
	for _, args := range [][]string{{"add", "src"}, {"commit", "-m", "Add disposable source"}} {
		if _, err := SafeGit(context.Background(), root, args...); err != nil {
			t.Fatal(err)
		}
	}
	head, _ := SafeGit(context.Background(), root, "rev-parse", "HEAD")
	tree, _ := SafeGit(context.Background(), root, "rev-parse", "HEAD^{tree}")
	copy := realTemp(t)
	paths, _ := SafeGit(context.Background(), root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, p := range strings.Split(strings.TrimSpace(paths), "\n") {
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		sourceWrite(t, copy, p, string(data))
		if err := os.Chmod(filepath.Join(copy, p), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root, copy, strings.TrimSpace(head), strings.TrimSpace(tree)
}
func runSourcePatchCollector(t *testing.T, root, copy, head, tree string) (*SourcePatchResult, error) {
	t.Helper()
	return runSourcePatchCollectorSelection(t, root, copy, head, tree, []string{"src/Example.cs"})
}
func runSourcePatchCollectorSelection(t *testing.T, root, copy, head, tree string, paths []string) (*SourcePatchResult, error) {
	t.Helper()
	selection, _ := json.Marshal(map[string]any{"paths": paths, "head": head, "tree": tree, "capture": true})
	script := strings.NewReplacer("/source", root, "/workspace", copy).Replace(collectSourcePatchPython)
	command := exec.Command("python3", "-I", "-c", script, string(selection))
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	data, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("collector fixture: %w: %s", err, data)
	}
	if failure, failureErr := sourcePatchCollectionFailure(data); failure {
		return nil, failureErr
	}
	var result SourcePatchResult
	err = json.Unmarshal(data, &result)
	return &result, err
}

func sourcePatchBudgetFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root, copy, _, _ := sourcePatchCollectorFixture(t, "before\n")
	sourceWrite(t, root, "src/Other.cs", "before\n")
	sourceWrite(t, copy, "src/Other.cs", "before\n")
	if err := os.Chmod(filepath.Join(copy, "src/Other.cs"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "src"}, {"commit", "-m", "Add another disposable source"}} {
		if _, err := SafeGit(context.Background(), root, args...); err != nil {
			t.Fatal(err)
		}
	}
	head, _ := SafeGit(context.Background(), root, "rev-parse", "HEAD")
	tree, _ := SafeGit(context.Background(), root, "rev-parse", "HEAD^{tree}")
	return root, copy, strings.TrimSpace(head), strings.TrimSpace(tree)
}

func TestSourcePatchCumulativeBudgetCanBeRefinedToNamedFiles(t *testing.T) {
	root, copy, head, tree := sourcePatchBudgetFixture(t)
	paths := []string{"src/Example.cs", "src/Other.cs"}
	after := strings.Repeat("after\n", maxSourcePatchDiff/12)
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(copy, p), []byte(after), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if artifact, err := runSourcePatchCollectorSelection(t, root, copy, head, tree, paths); !errors.Is(err, ErrCheckFailed) || artifact != nil {
		t.Fatal("cumulative overflow did not request refinement", err)
	}
	for _, selected := range paths {
		for _, p := range paths {
			data := "before\n"
			if p == selected {
				data = after
			}
			if err := os.WriteFile(filepath.Join(copy, p), []byte(data), 0644); err != nil {
				t.Fatal(err)
			}
		}
		artifact, err := runSourcePatchCollectorSelection(t, root, copy, head, tree, []string{selected})
		if err != nil || len(artifact.Diff) == 0 || len(artifact.Diff) > maxSourcePatchDiff {
			t.Fatal("smaller named selection failed", err)
		}
		command := exec.Command("git", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-C", root, "apply", "--check", "-")
		command.Stdin = strings.NewReader(artifact.Diff)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("refined artifact is not applicable: %s %v", data, err)
		}
	}
}

func TestSourcePatchBudgetRefinementResumesSessionWithoutArtifactOrPublication(t *testing.T) {
	directory, journal := testRun(t)
	sourceWrite(t, journal.Workspace, "src/Example.cs", "before\n")
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{sourcePatchRequest()}
	provider := &fakeProvider{outcomes: []Outcome{asked, testOutcome("waiting_for_human")}}
	checker := &patchFixtureChecker{patchErr: &sourcePatchDiffBudgetError{}}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative || checker.patchCalls != 1 || publisher.calls != 0 || journal.Verification[0].Passed || journal.Verification[0].SourcePatch != nil {
		t.Fatal("budget refinement bypassed review or became an operational hold")
	}
	if !strings.Contains(provider.calls[1].Prompt, "Reduce source_patch.paths and the formatter arguments") {
		t.Fatal("safe split-request guidance missing")
	}
	if data, _ := os.ReadFile(filepath.Join(journal.Workspace, "src/Example.cs")); string(data) != "before\n" {
		t.Fatal("budget failure changed implementation source")
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded.State, loaded.ResumeState = "blocked", "checking"
	if err := runner.Run(context.Background(), directory, &loaded, ""); !errors.Is(err, ErrStopped) || checker.patchCalls != 1 || len(provider.calls) != 2 || publisher.calls != 0 {
		t.Fatal("unchanged overflow retried automatically", err)
	}
}

func TestSourcePatchCollectorRejectsSourceAndSizeViolations(t *testing.T) {
	for _, scenario := range []string{"outside selection", "link", "mode", "delete", "binary", "file bound", "diff bound", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			root, copy, head, tree := sourcePatchCollectorFixture(t, "before\n")
			file := filepath.Join(copy, "src/Example.cs")
			switch scenario {
			case "outside selection":
				os.WriteFile(filepath.Join(copy, "README.md"), []byte("changed\n"), 0644)
			case "link":
				os.Remove(file)
				os.Symlink(filepath.Join(root, "src/Example.cs"), file)
			case "mode":
				os.Chmod(file, 0755)
			case "delete":
				os.Remove(file)
			case "binary":
				os.WriteFile(file, []byte{0xff, 0}, 0644)
			case "file bound":
				os.WriteFile(file, bytes.Repeat([]byte("x"), 262145), 0644)
			case "diff bound":
				os.WriteFile(file, bytes.Repeat([]byte("x"), 32769), 0644)
			case "identity":
				head = testHead
			}
			if _, err := runSourcePatchCollector(t, root, copy, head, tree); err == nil {
				t.Fatal("unsafe source correction exported")
			}
		})
	}
}
