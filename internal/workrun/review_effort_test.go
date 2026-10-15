package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestReviewEffortMatchesSizeAndRisk(t *testing.T) {
	large := make([]ReviewDiffFile, 25)
	for i := range large {
		large[i] = ReviewDiffFile{Path: "app.go", Added: 1}
	}
	for _, tc := range []struct {
		name   string
		diff   ReviewDiff
		effort string
	}{
		{"small docs", ReviewDiff{Files: []ReviewDiffFile{{Path: "README.md", Added: 20}}}, "low"},
		{"large lockfile", ReviewDiff{Files: []ReviewDiffFile{{Path: "package-lock.json", Added: 5000}}}, "medium"},
		{"public contract", ReviewDiff{Files: []ReviewDiffFile{{Path: "contracts/service.proto", Added: 2}}}, "high"},
		{"ordinary code", ReviewDiff{Files: []ReviewDiffFile{{Path: "app.go", Added: 30}}}, "medium"},
		{"small auth", ReviewDiff{Files: []ReviewDiffFile{{Path: "src/Authorization.cs", Added: 1}}}, "high"},
		{"content risk", ReviewDiff{Files: []ReviewDiffFile{{Path: "app.go", Added: 1}}, Sensitive: true}, "high"},
		{"large PR", ReviewDiff{Files: large}, "high"},
		{"large file", ReviewDiff{Files: []ReviewDiffFile{{Path: "app.go", Added: 1000}}}, "high"},
		{"large docs", ReviewDiff{Files: []ReviewDiffFile{{Path: "design.md", Added: 1000}}}, "medium"},
		{"dependency text", ReviewDiff{Files: []ReviewDiffFile{{Path: "requirements.txt", Added: 1}}}, "medium"},
		{"instructions", ReviewDiff{Files: []ReviewDiffFile{{Path: "AGENTS.md", Added: 1}}}, "medium"},
		{"bounded assessment", ReviewDiff{Truncated: true}, "high"},
		{"empty", ReviewDiff{}, "medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := assessReviewEffort(tc.diff)
			if s.Effort != tc.effort || len(s.Reasons) == 0 {
				t.Fatalf("selection: %+v", s)
			}
		})
	}
}

type effortRepository struct {
	fakeRepository
	reads int
	diff  ReviewDiff
}

func (r *effortRepository) ReviewDiff(context.Context, string, string, string) (ReviewDiff, error) {
	r.reads++
	return r.diff, nil
}

func TestReviewEffortDispatchAndSavedBoundary(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.Roles.ReviewEffortPolicy = "pr-v1"
	j.Plan.Roles.Review.Effort = "medium"
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	repo := &effortRepository{diff: ReviewDiff{Sensitive: true}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &repo.fakeRepository)
	r.Repository = repo
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls[1].Model.Effort != "high" || saved.Plan.Roles.Review.Effort != "medium" || saved.ReviewEffort == nil || saved.ReviewEffort.Selection.Effort != "high" {
		t.Fatal("selection not dispatched and retained", p.calls[1].Model, saved.ReviewEffort)
	}
	summary, err := runusage.LoadSummary(dir, j.ID, j.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	var invocation *runusage.SessionInvocation
	for _, s := range summary.Sessions {
		for _, c := range s.Invocations {
			if c.Role == "review" {
				copy := c
				invocation = &copy
			}
		}
	}
	if invocation == nil || invocation.RequestedEffort != "high" || invocation.ReviewEffortSelection == nil {
		t.Fatal("selection lost from usage", invocation)
	}
	repo.diff = ReviewDiff{Files: []ReviewDiffFile{{Path: "README.md", Added: 1}}}
	if err := r.selectReviewEffort(context.Background(), dir, &saved, j.Workspace); err != nil {
		t.Fatal(err)
	}
	if repo.reads != 1 || selectedReviewModel(&saved).Effort != "high" {
		t.Fatal("same boundary reassessed")
	}
	saved.Publication.HeadSHA = strings.Repeat("d", 40)
	if err := r.selectReviewEffort(context.Background(), dir, &saved, j.Workspace); err != nil {
		t.Fatal(err)
	}
	if repo.reads != 2 || selectedReviewModel(&saved).Effort != "low" {
		t.Fatal("changed boundary not reassessed")
	}
}

func TestExplicitAndLegacyEffortRemainFixed(t *testing.T) {
	roles, err := ResolveModels(DefaultModels(), "codex", "", "", "", "high")
	if err != nil {
		t.Fatal(err)
	}
	if roles.ReviewEffortPolicy != "" || roles.Review.Effort != "high" {
		t.Fatal(roles)
	}
	dir, j := testRun(t)
	j.Plan.Roles = roles
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.selectReviewEffort(context.Background(), dir, j, j.Workspace); err != nil || j.ReviewEffort != nil {
		t.Fatal("explicit effort overridden", err)
	}
	// Missing policy in older saved defaults means the effort was explicitly configured.
	defaults := DefaultModels()
	defaults.Codex.ReviewEffortPolicy = ""
	defaults.Codex.Review.Effort = "low"
	data, _ := json.Marshal(defaults)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadModels(dir)
	if err != nil || loaded.Codex.ReviewEffortPolicy != "" || loaded.Codex.Review.Effort != "low" {
		t.Fatal("legacy effort changed", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"version":1,"codex":{"implementation":{"model":"custom-model"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadModels(dir)
	if err != nil || loaded.Codex.ReviewEffortPolicy != "" || loaded.Codex.Review.Effort != "high" {
		t.Fatal("partial defaults changed high review baseline", loaded, err)
	}

}

func TestReviewDiffInspectorUsesExactRangeWithoutExternalCommands(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		data, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Example")
	git("config", "user.email", "example@example.invalid")
	git("config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n"), 0600)
	git("add", ".")
	git("commit", "-m", "Initial source")
	base := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\n// Authorization check changed\n"), 0600)
	git("add", ".")
	git("commit", "-m", "Change access check")
	head := git("rev-parse", "HEAD")
	marker := filepath.Join(root, "external-ran")
	git("config", "diff.external", "touch "+marker)
	helper := strings.ReplaceAll(repositoryHelper, "/workspace", root)
	data, err := exec.Command("python3", "-I", "-c", helper, "review_diff:"+base+":"+head).CombinedOutput()
	if err != nil {
		t.Fatalf("inspector: %v %s", err, data)
	}
	var diff ReviewDiff
	if err := json.Unmarshal(data, &diff); err != nil {
		t.Fatal(err, string(data))
	}
	if len(diff.Files) != 1 || diff.Files[0].Added != 1 || !diff.Sensitive || assessReviewEffort(diff).Effort != "high" {
		t.Fatal("incorrect source assessment", string(data))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("external diff executed")
	}
	if data, err := exec.Command("python3", "-I", "-c", helper, "review_diff:"+base+":"+base).CombinedOutput(); err == nil {
		t.Fatal("wrong snapshot admitted", string(data))
	}
	os.WriteFile(filepath.Join(root, "README.md"), []byte("Authentication documentation only\n"), 0600)
	os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(strings.Repeat("credential dependency metadata\n", 1200)), 0600)
	git("add", ".")
	git("commit", "-m", "Update documentation and dependency metadata")
	next := git("rev-parse", "HEAD")
	data, err = exec.Command("python3", "-I", "-c", helper, "review_diff:"+head+":"+next).CombinedOutput()
	if err != nil || json.Unmarshal(data, &diff) != nil {
		t.Fatalf("metadata inspector: %v %s", err, data)
	}
	if diff.Sensitive || assessReviewEffort(diff).Effort != "medium" {
		t.Fatal("docs or generated metadata inflated risk", string(data))
	}

}

func TestDefaultIndependentReviewerDispatchesHighForBothProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			dir, j := testRun(t)
			roles, err := ResolveModels(DefaultModels(), provider, "", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			j.Plan.Roles = roles
			p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
			r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if len(p.calls) != 2 || p.calls[1].Model.Provider == provider || p.calls[1].Model.Effort != "high" || j.ReviewEffort != nil {
				t.Fatal("default independent review was not fixed high effort", p.calls)
			}
		})
	}
}
