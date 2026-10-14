package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func aggregateGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func aggregateWrite(t *testing.T, root, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "add", name)
	aggregateGit(t, root, "commit", "-m", name)
}
func aggregateSnapshot(t *testing.T, root, branch, directory string) BranchSnapshot {
	t.Helper()
	sha := aggregateGit(t, root, "rev-parse", branch)
	aggregateGit(t, root, "update-ref", "refs/sdlc/snapshot", sha)
	path := filepath.Join(directory, branch+".bundle")
	aggregateGit(t, root, "bundle", "create", path, "refs/sdlc/snapshot")
	return BranchSnapshot{Branch: branch, SHA: sha, Bundle: path}
}
func TestCombinedSnapshotsAreLinearAndReplayOnlyChildAfterSquashMerges(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateWrite(t, root, "base.txt", "base")
	base := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "-b", "parent-a")
	aggregateWrite(t, root, "a.txt", "a")
	aggregateGit(t, root, "checkout", "-b", "parent-b", base)
	aggregateWrite(t, root, "b.txt", "b")
	directory := t.TempDir()
	integration := aggregateSnapshot(t, root, "main", directory)
	parents := []BranchSnapshot{aggregateSnapshot(t, root, "parent-a", directory), aggregateSnapshot(t, root, "parent-b", directory)}
	workspace := filepath.Join(directory, "combined")
	revision, err := CombineSnapshots(context.Background(), workspace, integration, parents)
	if err != nil {
		t.Fatal(err)
	}
	if got := aggregateGit(t, workspace, "show", "-s", "--format=%P", "HEAD"); got != base {
		t.Fatalf("aggregate is not one linear integration child: %s", got)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	aggregateWrite(t, workspace, "child.txt", "child")
	aggregateGit(t, root, "checkout", "main")
	aggregateGit(t, root, "merge", "--squash", "parent-a")
	aggregateGit(t, root, "commit", "-m", "squash a")
	aggregateGit(t, root, "merge", "--squash", "parent-b")
	aggregateGit(t, root, "commit", "-m", "squash b")
	merged := aggregateGit(t, root, "rev-parse", "HEAD")
	final := aggregateSnapshot(t, root, "main", directory)
	aggregateGit(t, workspace, "fetch", final.Bundle, "refs/sdlc/snapshot")
	aggregateGit(t, workspace, "rebase", "--onto", merged, revision.Head)
	if got := aggregateGit(t, workspace, "diff", "--name-only", merged+"..HEAD"); got != "child.txt" {
		t.Fatalf("parents entered child own diff: %s", got)
	}
	if got := aggregateGit(t, workspace, "rev-list", "--count", merged+"..HEAD"); got != "1" {
		t.Fatalf("child replay includes aggregate: %s", got)
	}
}
func TestCombinedSnapshotsConflictBeforeChildWorkspace(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateWrite(t, root, "shared.txt", "base")
	base := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "-b", "parent-a")
	aggregateWrite(t, root, "shared.txt", "a")
	aggregateGit(t, root, "checkout", "-b", "parent-b", base)
	aggregateWrite(t, root, "shared.txt", "b")
	directory := t.TempDir()
	integration := aggregateSnapshot(t, root, "main", directory)
	parents := []BranchSnapshot{aggregateSnapshot(t, root, "parent-a", directory), aggregateSnapshot(t, root, "parent-b", directory)}
	if _, err := CombineSnapshots(context.Background(), filepath.Join(directory, "combined"), integration, parents); err == nil {
		t.Fatal("conflicting parents combined")
	}
}

func TestDependencyInvalidJournalRejectsStandaloneResumeBeforeNativeWork(t *testing.T) {
	directory, journal := testRun(t)
	journal.DependencyInvalid = true
	runner := Runner{}
	if err := runner.Run(context.Background(), directory, journal, ""); err == nil || !strings.Contains(err.Error(), "dependency base changed") {
		t.Fatalf("stale standalone resume was admitted: %v", err)
	}
}

func TestRebaseHelperPreservesDirtyTrackedChildAcrossDependencyChange(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateWrite(t, root, "base.txt", "base")
	base := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "-b", "parent-a")
	aggregateWrite(t, root, "a.txt", "a")
	aggregateGit(t, root, "checkout", "-b", "parent-b", base)
	aggregateWrite(t, root, "b.txt", "b")
	directory := t.TempDir()
	integration := aggregateSnapshot(t, root, "main", directory)
	parents := []BranchSnapshot{aggregateSnapshot(t, root, "parent-a", directory), aggregateSnapshot(t, root, "parent-b", directory)}
	workspace := filepath.Join(directory, "combined")
	revision, err := CombineSnapshots(context.Background(), workspace, integration, parents)
	if err != nil {
		t.Fatal(err)
	}
	aggregateWrite(t, workspace, "child.txt", "committed child")
	if err := os.WriteFile(filepath.Join(workspace, "child.txt"), []byte("unfinished child answer work"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "checkout", "main")
	aggregateGit(t, root, "merge", "--squash", "parent-a")
	aggregateGit(t, root, "commit", "-m", "squash a")
	aggregateGit(t, root, "merge", "--squash", "parent-b")
	aggregateGit(t, root, "commit", "-m", "squash b")
	snapshot := aggregateSnapshot(t, root, "main", directory)
	helper := strings.ReplaceAll(rebaseHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", workspace)+")")
	helper = strings.ReplaceAll(helper, "'/newbase.bundle'", fmt.Sprintf("%q", snapshot.Bundle))
	run := func() {
		t.Helper()
		cmd := exec.Command("python3", "-c", helper, revision.Head, snapshot.SHA)
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), `"conflict": false`) {
			t.Fatalf("dirty restack: %v %s", err, output)
		}
	}
	run()
	head := aggregateGit(t, workspace, "rev-parse", "HEAD")
	data, err := os.ReadFile(filepath.Join(workspace, "child.txt"))
	if err != nil || string(data) != "unfinished child answer work" {
		t.Fatalf("unfinished native work lost: %q %v", data, err)
	}
	if got := aggregateGit(t, workspace, "diff", "--name-only", snapshot.SHA+"..HEAD"); got != "child.txt" {
		t.Fatalf("parent changes replayed into child: %s", got)
	}
	run()
	if got := aggregateGit(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatal("recovery duplicated the dirty-work commit or rebase")
	}
}

func TestRebaseHelperRecoversLostResultAfterNonoverlappingSameFileChanges(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i+1)
	}
	aggregateWrite(t, root, "shared.txt", strings.Join(lines, "\n")+"\n")
	old := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "-b", "child")
	own := append([]string{}, lines...)
	own[24] = "child change"
	aggregateWrite(t, root, "shared.txt", strings.Join(own, "\n")+"\n")
	aggregateGit(t, root, "checkout", "main")
	upstream := append([]string{}, lines...)
	upstream[1] = "upstream change"
	upstream[21] = "nearby upstream change"
	aggregateWrite(t, root, "shared.txt", strings.Join(upstream, "\n")+"\n")
	snapshot := aggregateSnapshot(t, root, "main", t.TempDir())
	aggregateGit(t, root, "checkout", "child")
	helper := strings.ReplaceAll(rebaseHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
	helper = strings.ReplaceAll(helper, "'/newbase.bundle'", fmt.Sprintf("%q", snapshot.Bundle))
	run := func() {
		t.Helper()
		output, err := exec.Command("python3", "-c", helper, old, snapshot.SHA).CombinedOutput()
		if err != nil || !strings.Contains(string(output), `"conflict": false`) {
			t.Fatalf("same-file rebase recovery: %v %s", err, output)
		}
	}
	run()
	head := aggregateGit(t, root, "rev-parse", "HEAD")
	// Model interruption after Git finishes but before completed-result receipt.
	aggregateGit(t, root, "update-ref", "-d", "refs/sdlc/rebase-result/"+old+"/"+snapshot.SHA)
	run()
	if got := aggregateGit(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("lost result repeated rebase or changed child revision")
	}
	data, err := os.ReadFile(filepath.Join(root, "shared.txt"))
	if err != nil || !strings.Contains(string(data), "upstream change") || !strings.Contains(string(data), "child change") {
		t.Fatalf("same-file replay lost work: %s %v", data, err)
	}
	run()
}

// Git can detach an automatic collector from fetch. Observe real subprocesses
// rather than treating a particular command-line spelling as the guarantee.
func assertNoDetachedGitMaintenance(t *testing.T, trace string) {
	t.Helper()
	file, err := os.Open(trace)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	commands := 0
	for {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Event == "start" {
			commands++
		}
		if event.Event != "child_start" {
			continue
		}
		for _, argument := range event.Argv {
			if argument == "gc" || argument == "repack" {
				t.Fatal("fixture started automatic object mutation", event.Argv)
			}
		}
	}
	if commands == 0 {
		t.Fatal("Git trace did not record fixture execution")
	}
}

func TestCombinedFixtureFetchKeepsObjectsStableUnderLowMaintenanceThresholds(t *testing.T) {
	source, destination, bundles := t.TempDir(), t.TempDir(), t.TempDir()
	aggregateGit(t, source, "init", "-b", "main")
	aggregateWrite(t, source, "base.txt", "base")
	aggregateGit(t, destination, "init", "-b", "main")
	for _, setting := range [][]string{{"gc.auto", "1"}, {"gc.autoPackLimit", "1"}, {"fetch.unpackLimit", "1"}} {
		aggregateGit(t, destination, "config", setting[0], setting[1])
	}
	first := aggregateSnapshot(t, source, "main", bundles)
	aggregateGit(t, destination, "fetch", first.Bundle, "refs/sdlc/snapshot:refs/heads/imported")
	aggregateWrite(t, source, "child.txt", "child")
	second := aggregateSnapshot(t, source, "main", bundles)
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	aggregateGit(t, destination, "fetch", second.Bundle, "refs/sdlc/snapshot:refs/heads/imported")
	aggregateGit(t, destination, "fsck", "--strict", "--no-reflogs")
	assertNoDetachedGitMaintenance(t, trace)
}
