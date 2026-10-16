package workrun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type inspectionRepository struct {
	*fakeRepository
	paths []string
}

func (r inspectionRepository) ReviewChangedPaths(context.Context, string, string, string) ([]string, error) {
	return r.paths, nil
}
func inspectionOutcome(items ...ReviewFileInspection) Outcome {
	out := testOutcome("reviewed")
	out.ReviewCoverage = coverage("full")
	out.ReviewCoverage.Inspection = items
	return out
}
func TestFileInspectionContradictionContinuesSameReview(t *testing.T) {
	for _, mode := range []string{"incomplete body", "omitted file", "empty inventory"} {
		t.Run(mode, func(t *testing.T) {
			first := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Traced request and error paths"})
			if mode == "incomplete body" {
				first.ReviewCoverage.Inspection = append(first.ReviewCoverage.Inspection, ReviewFileInspection{Path: "tests/app_test.go", Evidence: "Only test names inspected; read assertions and fixture setup"})
			}
			if mode == "empty inventory" {
				first.ReviewCoverage.Inspection = []ReviewFileInspection{}
			}
			final := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Request and error paths"}, ReviewFileInspection{Path: "tests/app_test.go", Complete: true, Evidence: "Assertions, fixtures and negative control"})
			dir, j, r, p, workspace := continuationFixture(t, first, final)
			r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"app.go", "tests/app_test.go"}}
			result, err := r.independentReview(context.Background(), dir, j, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome.Status != "reviewed" || len(p.calls) != 2 || p.calls[1].ResumeID != testNative || p.calls[0].Directory != p.calls[1].Directory {
				t.Fatal("inspection did not continue original owner", result.Outcome.Status, p.calls)
			}
			if len(j.ReviewReports) != 1 || j.ReviewReports[0].Coverage.Scope != "partial" || len(j.ReviewReports[0].Coverage.Gaps) == 0 {
				t.Fatal("overstated coverage was accepted")
			}
			if !strings.Contains(p.calls[1].Prompt, "tests/app_test.go") {
				t.Fatal("missing path not supplied to continuation")
			}
		})
	}
}
func TestCompleteFileInspectionDoesNotRepeatReview(t *testing.T) {
	out := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "All changed runtime paths inspected"})
	dir, j, r, p, workspace := continuationFixture(t, out)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"app.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || len(p.calls) != 1 {
		t.Fatal("complete review repeated", err)
	}
}
func TestInspectionRejectsDuplicateOrUnsafePaths(t *testing.T) {
	for _, path := range []string{"app.go", "../private"} {
		out := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Inspected"}, ReviewFileInspection{Path: path, Evidence: "Pending"})
		if err := validateReviewDetails(out, "review"); err == nil {
			t.Fatal("invalid inventory accepted", path)
		}
	}
}

func TestReviewSchemaRequiresFileInspection(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(reviewOutcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	for _, variant := range props["review_coverage"].(map[string]any)["anyOf"].([]any) {
		object := variant.(map[string]any)
		if object["type"] != "object" {
			continue
		}
		required := false
		for _, name := range object["required"].([]any) {
			if name == "inspection" {
				required = true
			}
		}
		if !required {
			t.Fatal("native review can omit inspection accounting")
		}
	}
}

func TestChangedFileInspectorIncludesDeletedAndTestPaths(t *testing.T) {
	root := t.TempDir()
	repo := feedbackGitRepository{t: t}
	repo.git(root, "init")
	repo.git(root, "config", "user.name", "Example")
	repo.git(root, "config", "user.email", "example@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git(root, "add", ".")
	repo.git(root, "commit", "-m", "Base fixture")
	base := repo.git(root, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(root, "app.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tests", "fixture file.py"), []byte("pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git(root, "add", ".")
	repo.git(root, "commit", "-m", "Change fixture")
	head := repo.git(root, "rev-parse", "HEAD")
	marker := filepath.Join(root, "external-ran")
	repo.git(root, "config", "diff.external", "touch "+marker)
	helper := strings.ReplaceAll(repositoryHelper, "/workspace", root)
	raw, err := exec.Command("python3", "-I", "-c", helper, "review_files:"+base+":"+head).CombinedOutput()
	if err != nil {
		t.Fatalf("inspector failed: %v %s", err, raw)
	}
	var paths []string
	if json.Unmarshal(raw, &paths) != nil || len(paths) != 2 || paths[0] != "app.go" || paths[1] != "tests/fixture file.py" {
		t.Fatalf("paths lost: %s", raw)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("external Git diff helper was executed")
	}
	if err := exec.Command("python3", "-I", "-c", helper, "review_files:"+base+":"+base).Run(); err == nil {
		t.Fatal("stale snapshot accepted")
	}
}

func TestEmptyChangedFileSetCanCompleteInspection(t *testing.T) {
	out := inspectionOutcome()
	out.ReviewCoverage.Inspection = []ReviewFileInspection{}
	dir, j, r, p, workspace := continuationFixture(t, out)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || len(p.calls) != 1 {
		t.Fatal("empty diff invented unfinished work", err)
	}
}

func TestDisjointFileInspectionAccumulatesWithoutNarrativeReceipts(t *testing.T) {
	first := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Inspected request parsing"})
	second := inspectionOutcome(ReviewFileInspection{Path: "b.go", Complete: true, Evidence: "Inspected runtime wiring"})
	third := inspectionOutcome(ReviewFileInspection{Path: "c.go", Complete: true, Evidence: "Inspected failure recovery"})
	dir, j, r, p, workspace := continuationFixture(t, first, second, third)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go", "c.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || len(p.calls) != 3 || j.ReviewContinuation != nil {
		t.Fatal(result.Outcome.Status, err, len(p.calls))
	}
	if len(result.Outcome.ReviewCoverage.Inspection) != 3 {
		t.Fatal("earlier file inspection lost")
	}
	for _, item := range result.Outcome.ReviewCoverage.Inspection {
		if !item.Complete {
			t.Fatal(item)
		}
	}
	if len(j.ReviewReports) != 2 || len(j.ReviewReports[1].Coverage.Inspection) != 3 {
		t.Fatal("cumulative interim evidence missing")
	}
	for _, call := range p.calls[1:] {
		if call.ResumeID != testNative || call.Directory != p.calls[0].Directory {
			t.Fatal("review owner changed")
		}
	}
}

func TestGroupedInspectionGetsNoCreditAndActionableExactPaths(t *testing.T) {
	first := inspectionOutcome(ReviewFileInspection{Path: "a.go, b.go", Complete: true, Evidence: "Read both"})
	second := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Read a"})
	third := inspectionOutcome(ReviewFileInspection{Path: "b.go", Complete: true, Evidence: "Read b"})
	dir, j, r, p, workspace := continuationFixture(t, first, second, third)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || len(p.calls) != 3 {
		t.Fatal(result.Outcome.Status, err)
	}
	for _, entry := range j.ReviewReports[0].Coverage.Inspection {
		if entry.Complete {
			t.Fatal("grouped path credited", entry)
		}
	}
	if !strings.Contains(p.calls[1].Prompt, "one per entry") || !strings.Contains(p.calls[1].Prompt, `"path":"a.go"`) {
		t.Fatal("no exact-path correction in continuation")
	}
}

func TestRepeatedCompletedFileDoesNotManufactureProgress(t *testing.T) {
	first := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Inspected a"})
	next := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Same inspection, different prose"})
	dir, j, r, p, workspace := continuationFixture(t, first, next)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "blocked" || len(p.calls) != 2 {
		t.Fatal(result.Outcome.Status, err)
	}
	if j.ReviewContinuation == nil || len(j.ReviewContinuation.Inspection) != 2 {
		t.Fatal("pending file lost")
	}
}

func TestLegacyContinuationRestoresExactReceiptsWithoutRewritingReports(t *testing.T) {
	_, j, r, _, workspace := continuationFixture(t)
	j.Attempt = 5
	legacy := "Finish material file inspection: " + strings.Repeat("a.go: no file-inspection entry supplied; ", 100)
	c := &ReviewContinuation{Publication: j.Publication, Model: selectedReviewModel(j), Speed: j.Plan.Roles.ReviewSpeed, Tree: testTree, SessionID: testNative, Workspace: "review-source-fixture", StartAttempt: 4, LastAttempt: 5, Gaps: []string{legacy}}
	j.ReviewContinuation = c
	old := &ReviewCoverage{BaseSHA: testBase, HeadSHA: testHead, Scope: "partial", Gaps: []string{legacy}, Inspection: []ReviewFileInspection{{Path: "a.go, b.go", Complete: true, Evidence: "Grouped claim"}}}
	latest := &ReviewCoverage{BaseSHA: testBase, HeadSHA: testHead, Scope: "partial", Gaps: []string{legacy + "paraphrased"}, Inspection: []ReviewFileInspection{{Path: "b.go", Complete: true, Evidence: "Inspected b exactly"}}}
	j.ReviewReports = []ReviewReport{{Publication: j.Publication, Attempt: 4, Coverage: old}, {Publication: j.Publication, Attempt: 5, Coverage: latest}}
	original, _ := json.Marshal(j.ReviewReports)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go"}}
	if err := r.restoreReviewInspection(context.Background(), workspace, j); err != nil {
		t.Fatal(err)
	}
	if len(c.Gaps) != 1 || c.Gaps[0] != fileInspectionGap || len(c.Inspection) != 2 || c.Inspection[0].Complete || !c.Inspection[1].Complete {
		t.Fatal("legacy accounting not recovered", c)
	}
	if err := validateReviewContinuation(j); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reviewContinuationPrompt(*j), legacy) {
		t.Fatal("giant legacy gap remains in prompt")
	}
	after, _ := json.Marshal(j.ReviewReports)
	if string(original) != string(after) {
		t.Fatal("historical reports rewritten")
	}
	out := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Finished a"})
	if err := r.reconcileReviewInspection(context.Background(), workspace, &out, reviewInspectionHistory(j)...); err != nil {
		t.Fatal(err)
	}
	if out.Status != "reviewed" || len(out.ReviewCoverage.Inspection) != 2 {
		t.Fatal("recovered file inspection not retained")
	}
}

func TestFileProgressCannotEraseSeparateUnfinishedGap(t *testing.T) {
	c := &ReviewCoverage{Gaps: []string{fileInspectionGap}, CompletedGaps: []string{}}
	if reviewInspectionAdvanced([]string{fileInspectionGap, "Trace transaction rollback"}, c, true) {
		t.Fatal("file progress erased independent unfinished work")
	}
	c.Gaps = append(c.Gaps, "Trace transaction rollback")
	if !reviewInspectionAdvanced([]string{fileInspectionGap, "Trace transaction rollback"}, c, true) {
		t.Fatal("file progress with preserved generic gap rejected")
	}
}

func TestCumulativeFileInspectionRetainsRecommendationsUntilFinalJudgement(t *testing.T) {
	recommendation := Finding{Priority: "P3", Path: "a.go", Line: 1, Scenario: "Repeated identity checks can drift", Recommendation: "Share the identity predicate"}
	first := inspectionOutcome(ReviewFileInspection{Path: "a.go", Complete: true, Evidence: "Read identity checks"})
	first.Recommendations = []Finding{recommendation}
	second := inspectionOutcome(ReviewFileInspection{Path: "b.go", Complete: true, Evidence: "Read publication path"})
	third := inspectionOutcome(ReviewFileInspection{Path: "c.go", Complete: true, Evidence: "Read tests"})
	third.Recommendations = []Finding{recommendation}
	dir, j, r, p, workspace := continuationFixture(t, first, second, third)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go", "c.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" {
		t.Fatal(result.Outcome.Status, err)
	}
	if len(j.ReviewReports[1].Recommendations) != 1 || j.ReviewReports[1].Recommendations[0] != recommendation {
		t.Fatal("intermediate turn lost advisory recommendation")
	}
	if !strings.Contains(p.calls[2].Prompt, "earlier_recommendations") || !strings.Contains(p.calls[2].Prompt, recommendation.Scenario) {
		t.Fatal("next reviewer lost duplication recommendation")
	}
}

func TestInspectionFromAnotherPublicationDoesNotCreditCurrentReview(t *testing.T) {
	_, j, r, _, workspace := continuationFixture(t)
	j.Attempt = 5
	j.ReviewContinuation = &ReviewContinuation{Publication: j.Publication, StartAttempt: 4, LastAttempt: 5, Gaps: []string{fileInspectionGap}}
	prior := j.Publication
	prior.HeadSHA = testBase
	j.ReviewReports = []ReviewReport{{Publication: prior, Attempt: 4, Coverage: &ReviewCoverage{BaseSHA: testBase, HeadSHA: testBase, Scope: "full", Gaps: []string{}, Inspection: []ReviewFileInspection{{Path: "a.go", Complete: true, Evidence: "Old source"}}}}}
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go"}}
	if err := r.restoreReviewInspection(context.Background(), workspace, j); err != nil {
		t.Fatal(err)
	}
	if len(j.ReviewContinuation.Inspection) != 1 || j.ReviewContinuation.Inspection[0].Complete {
		t.Fatal("stale publication supplied current coverage")
	}
}

func TestMissingInspectionCannotBypassPendingManifest(t *testing.T) {
	r := Runner{Repository: inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go", "b.go"}}}
	out := inspectionOutcome()
	out.ReviewCoverage.Inspection = nil
	prior := []ReviewFileInspection{{Path: "a.go", Complete: true, Evidence: "Read a"}}
	if err := r.reconcileReviewInspection(context.Background(), "unused", &out, prior); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" || len(out.ReviewCoverage.Inspection) != 2 || out.ReviewCoverage.Inspection[1].Complete {
		t.Fatal("missing array bypassed unfinished work")
	}
}

func TestCompleteFilesStillRequireReviewerFinalJudgement(t *testing.T) {
	out := unfinishedReview(fileInspectionGap)
	out.ReviewCoverage.Inspection = []ReviewFileInspection{{Path: "a.go", Complete: true, Evidence: "Read a"}}
	r := Runner{Repository: inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"a.go"}}}
	if err := r.reconcileReviewInspection(context.Background(), "unused", &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" || len(out.ReviewCoverage.Gaps) != 1 {
		t.Fatal("controller invented final judgement")
	}
	if err := validateOutcome(out, "review"); err != nil {
		t.Fatal("controller produced invalid continuation", err)
	}
}
