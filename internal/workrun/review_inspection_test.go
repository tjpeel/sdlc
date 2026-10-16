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
