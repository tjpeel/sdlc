package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func workflowTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = root
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func workflowTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	workflowTestGit(t, root, "init", "-q")
	workflowTestGit(t, root, "config", "user.name", "Test")
	workflowTestGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workflowTestGit(t, root, "add", "source.txt")
	workflowTestGit(t, root, "commit", "-qm", "baseline")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/.sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestComparisonRejectsChangedSourceRequirementsAndChecks(t *testing.T) {
	root := workflowTestRepo(t)
	sha := workflowTestGit(t, root, "rev-parse", "HEAD")
	path := ".sdlc/work/example/specification.md"
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("approved"))
	in := workrun.Input{Path: path, SHA256: fmt.Sprintf("%x", h)}
	old := workrun.Plan{Root: root, SourceSHA: sha, StartingSHA: sha, Inputs: []workrun.Input{in}, Checks: [][]string{{"go", "test"}}}
	launch := project.LaunchResult{Root: root, Head: sha, Inputs: []string{path}, Config: project.Config{Checks: [][]string{{"go", "test"}}}}
	if err := validateComparison(context.Background(), launch, old, nil); err != nil {
		t.Fatalf("matching comparison rejected: %v", err)
	}
	launch.Config.Checks = [][]string{{"false"}}
	if err := validateComparison(context.Background(), launch, old, nil); err == nil {
		t.Fatal("changed checks accepted")
	}
	launch.Config.Checks = old.Checks

}
func TestAmendmentStorageRejectsDriftAndSymlinks(t *testing.T) {
	root := workflowTestRepo(t)
	work := filepath.Join(root, ".sdlc", "work", "example")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "amendments")
	if err := validateAmendmentDirectory(root, dir); err != nil {
		t.Fatalf("ignored directory rejected: %v", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "A1.md")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateAmendmentExisting(path, "original"); err != nil {
		t.Fatal(err)
	}
	if err := validateAmendmentExisting(path, "changed"); err == nil {
		t.Fatal("existing key overwritten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "source.txt"), path); err != nil {
		t.Fatal(err)
	}
	if err := validateAmendmentExisting(path, "base\n"); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestAmendmentFlagRequiresAFileAndSavedRun(t *testing.T) {
	if _, err := parseRunOptions([]string{"--specification-amendment"}); err == nil {
		t.Fatal("amendment without a saved run accepted")
	}
	o, err := parseRunOptions([]string{"--reference", "example", "--ticket", "01-example.md", "--resume", "abc", "--pr-feedback-file", "amendment.md", "--specification-amendment"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.localAmendment || !o.processPRFeedback {
		t.Fatal("amendment did not enter feedback lifecycle")
	}
}

func TestAmendmentPreviewDoesNotSaveOrDispatch(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	target := ".sdlc/work/" + j.Plan.Reference + "/specification.md"
	j.Plan.Inputs = append(j.Plan.Inputs, workrun.Input{Path: target, SHA256: strings.Repeat("a", 64)})
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(j.Plan.Root, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n/.sdlc/work/\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	source := filepath.Join(t.TempDir(), "amendment.md")
	if err := os.WriteFile(source, []byte("New requirement"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = amendmentCommand(context.Background(), []string{"--run", j.ID, "--key", "A1", "--target", target, "--file", source, "--dry-run"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "New requirement") {
		t.Fatal("preview exposed private amendment")
	}
	if _, err := os.Stat(filepath.Join(j.Plan.Root, ".sdlc", "work", j.Plan.Reference, "amendments")); !os.IsNotExist(err) {
		t.Fatal("preview saved an amendment", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed journal")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview invoked connected work", err)
	}
}

func TestComparisonAcceptsOnlyExplicitMatchingAmendment(t *testing.T) {
	root := workflowTestRepo(t)
	sha := workflowTestGit(t, root, "rev-parse", "HEAD")
	path := ".sdlc/work/example/specification.md"
	amendment := ".sdlc/work/example/amendments/A1.md"
	for p, text := range map[string]string{path: "approved", amendment: "amendment"} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	specHash := sha256.Sum256([]byte("approved"))
	amendHash := sha256.Sum256([]byte("amendment"))
	spec := workrun.Input{Path: path, SHA256: fmt.Sprintf("%x", specHash)}
	selected := workrun.Input{Path: amendment, SHA256: fmt.Sprintf("%x", amendHash)}
	old := workrun.Plan{Root: root, SourceSHA: sha, StartingSHA: sha, Inputs: []workrun.Input{spec}, Checks: [][]string{{"go", "test"}}}
	launch := project.LaunchResult{Root: root, Head: sha, Inputs: []string{path, amendment}, Config: project.Config{Checks: [][]string{{"go", "test"}}}}
	if err := validateComparison(context.Background(), launch, old, []workrun.Input{selected}); err != nil {
		t.Fatal(err)
	}
	if err := validateComparison(context.Background(), launch, old, nil); err == nil {
		t.Fatal("unselected amendment accepted")
	}
	if err := os.WriteFile(filepath.Join(root, amendment), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateComparison(context.Background(), launch, old, []workrun.Input{selected}); err == nil {
		t.Fatal("changed amendment accepted")
	}
}

func TestComparisonRejectsConflictingCapturedHashesForOnePath(t *testing.T) {
	p := workrun.Plan{Inputs: []workrun.Input{{Path: "same", SHA256: strings.Repeat("a", 64)}}, CheckInputs: []workrun.Input{{Path: "same", SHA256: strings.Repeat("b", 64)}}}
	if _, err := comparisonHashes(p, nil); err == nil {
		t.Fatal("different captured bytes for the same path were accepted")
	}
}
