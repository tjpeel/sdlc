package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureComparisonReusesSavedSourceWithoutChangingEitherCheckout(t *testing.T) {
	ctx := context.Background()
	root, launch := sourceFixture(t)
	parent := realTemp(t)
	original := filepath.Join(parent, "original")
	old, err := Capture(ctx, launch, original, "original-branch", "main", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, original, "README.md", "implementation\n")
	if _, err = SafeGit(ctx, original, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err = SafeGit(ctx, original, "commit", "-m", "implementation"); err != nil {
		t.Fatal(err)
	}
	previousHead, err := SafeGit(ctx, original, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, root, "README.md", "unrelated checkout edit\n")
	sourceWrite(t, root, ".sdlc/work/JOB:1/tickets/1-first.md", "later ticket change\n")
	dest := filepath.Join(parent, "comparison")
	plan, err := CaptureComparison(ctx, launch, old, original, dest, "comparison-branch", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceSHA != old.SourceSHA || plan.StartingSHA != old.StartingSHA || plan.BaseSHA != old.BaseSHA {
		t.Fatalf("comparison changed baseline: %+v", plan)
	}
	got, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil || string(got) != "original\n" {
		t.Fatalf("comparison got %q: %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dest, ".sdlc/work/JOB:1/tickets/1-first.md"))
	if err != nil || string(got) != "disposable ticket\n" {
		t.Fatalf("captured requirement changed: %q %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(got) != "unrelated checkout edit\n" {
		t.Fatalf("source checkout changed: %q %v", got, err)
	}
	after, err := SafeGit(ctx, original, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(after) != strings.TrimSpace(previousHead) {
		t.Fatalf("original worker changed: %s %v", after, err)
	}
}

func TestCaptureComparisonRetainsCapturedDirtyBaseline(t *testing.T) {
	ctx := context.Background()
	root, launch := sourceFixture(t)
	sourceWrite(t, root, "README.md", "captured edit\n")
	parent := realTemp(t)
	original := filepath.Join(parent, "original")
	old, err := Capture(ctx, launch, original, "original-branch", "main", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if old.StartingSHA == old.SourceSHA {
		t.Fatal("fixture did not capture the dirty baseline")
	}
	sourceWrite(t, root, "README.md", "later checkout edit\n")
	plan, err := CaptureComparison(ctx, launch, old, original, filepath.Join(parent, "comparison"), "comparison-branch", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.StartingSHA != old.StartingSHA || plan.SourceSHA != old.SourceSHA {
		t.Fatalf("baseline changed: %+v", plan)
	}
	got, err := os.ReadFile(filepath.Join(parent, "comparison", "README.md"))
	if err != nil || string(got) != "captured edit\n" {
		t.Fatalf("captured baseline lost: %q %v", got, err)
	}
}

func TestCaptureComparisonRejectsMissingRecordedRevision(t *testing.T) {
	ctx := context.Background()
	_, launch := sourceFixture(t)
	parent := realTemp(t)
	original := filepath.Join(parent, "original")
	old, err := Capture(ctx, launch, original, "original-branch", "main", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	old.StartingSHA = strings.Repeat("1", 40)
	dest := filepath.Join(parent, "comparison")
	if _, err = CaptureComparison(ctx, launch, old, original, dest, "comparison-branch", "example/repository", Roles{}); err == nil {
		t.Fatal("missing source was accepted")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed capture left a worker", err)
	}
}

func TestCaptureComparisonUsesSavedCheckInputs(t *testing.T) {
	ctx := context.Background()
	root, launch := sourceFixture(t)
	sourceWrite(t, root, "checks/input.txt", "original check input\n")
	launch.Config.InputFiles = []string{"checks/input.txt"}
	originalParent := realTemp(t)
	original := filepath.Join(originalParent, "original")
	old, err := Capture(ctx, launch, original, "original-branch", "main", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, root, "checks/input.txt", "later check input\n")
	comparisonParent := realTemp(t)
	dest := filepath.Join(comparisonParent, "comparison")
	plan, err := CaptureComparison(ctx, launch, old, original, dest, "comparison-branch", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(comparisonParent, "check-inputs", "checks", "input.txt"))
	if err != nil || string(data) != "original check input\n" {
		t.Fatalf("saved check input lost: %q %v", data, err)
	}
	if len(plan.CheckInputs) != 1 || plan.CheckInputs[0].SHA256 != old.CheckInputs[0].SHA256 {
		t.Fatal("check input hash changed")
	}
}
