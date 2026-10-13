package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func combinedFixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

type combinedFixture struct {
	driver                                        *seriesDriver
	ticket                                        workseries.Ticket
	integration                                   workseries.Target
	parents                                       []workseries.Dependency
	checks, publications, recoveries, validations int
	checkError, publishError                      error
	source                                        workrun.BranchSnapshot
}

func newCombinedFixture(t *testing.T) *combinedFixture {
	t.Helper()
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	combinedFixtureGit(t, root, "init", "-b", "main")
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		combinedFixtureGit(t, root, "add", name)
		combinedFixtureGit(t, root, "commit", "-m", name)
	}
	write("base.txt", "base")
	base := combinedFixtureGit(t, root, "rev-parse", "HEAD")
	snapshots := map[string]workrun.BranchSnapshot{}
	snapshot := func(branch string) {
		head := combinedFixtureGit(t, root, "rev-parse", branch)
		combinedFixtureGit(t, root, "update-ref", "refs/sdlc/snapshot", head)
		bundle := filepath.Join(t.TempDir(), "source.bundle")
		combinedFixtureGit(t, root, "bundle", "create", bundle, "refs/sdlc/snapshot")
		snapshots[branch] = workrun.BranchSnapshot{Branch: branch, SHA: head, Bundle: bundle}
	}
	snapshot("main")
	combinedFixtureGit(t, root, "checkout", "-b", "a")
	write("a.txt", "a")
	snapshot("a")
	combinedFixtureGit(t, root, "checkout", "-b", "b", base)
	write("b.txt", "b")
	snapshot("b")
	if err := os.WriteFile(filepath.Join(root, "guidance.txt"), []byte("frozen check guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("frozen check guidance"))
	directory := filepath.Join(root, ".sdlc", "work", "TASK-1", "series")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	f := &combinedFixture{ticket: workseries.Ticket{File: "03-c.md"}, integration: workseries.Target{Base: "main", SHA: base}, source: snapshots["main"]}
	f.parents = []workseries.Dependency{{Ticket: "01-a.md", RunID: strings.Repeat("1", 24), Branch: "a", Base: "main", BaseSHA: base, HeadSHA: snapshots["a"].SHA, State: "ready", PRNumber: 1}, {Ticket: "02-b.md", RunID: strings.Repeat("2", 24), Branch: "b", Base: "main", BaseSHA: base, HeadSHA: snapshots["b"].SHA, State: "ready", PRNumber: 2}}
	f.driver = &seriesDriver{plan: workseries.Plan{Root: root, Reference: "TASK-1", Base: "main"}, directory: directory, settings: seriesSettings{InputHashes: map[string]string{"guidance.txt": hex.EncodeToString(hash[:])}, Config: project.Config{NugetCache: &project.NugetCacheBudget{MaxPackages: 16, MaxBytes: 1024 * 1024}, Checks: [][]string{{"go", "test", "./..."}}, InputFiles: []string{"guidance.txt"}}}}
	f.driver.combinedOps = &combinedOperations{
		Validate: func(context.Context, workseries.Target, []workseries.Dependency) error { f.validations++; return nil },
		Base: func(_ context.Context, branch string) (workseries.Target, error) {
			return workseries.Target{Base: branch, SHA: strings.Repeat("f", 40)}, nil
		},
		Snapshot: func(_ context.Context, _ workseries.Target, _ string) (workrun.BranchSnapshot, func(), error) {
			return snapshots["main"], func() {}, nil
		},
		ParentSnapshot: func(_ context.Context, parent workseries.Dependency, _ string) (workrun.BranchSnapshot, error) {
			return snapshots[parent.Branch], nil
		},
		Check: func(_ context.Context, checker workrun.DockerChecker, workspace string, _ [][]string) error {
			f.checks++
			if checker.NugetCache == nil || checker.NugetCache.MaxPackages != 16 || checker.NugetCache.MaxBytes != 1024*1024 {
				t.Fatal("combined checks lost selected package cache budget")
			}
			for _, name := range []string{"a.txt", "b.txt"} {
				if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(filepath.Join(checker.InputDirectory, "guidance.txt"))
			if err != nil || string(data) != "frozen check guidance" {
				t.Fatalf("frozen check input lost: %q %v", data, err)
			}
			return f.checkError
		},
		Recover: func(_ context.Context, checker workrun.DockerChecker) error {
			f.recoveries++
			if !strings.HasPrefix(checker.WorkerName, "sdlc-check-") {
				t.Fatal("recovery lacks frozen worker identity")
			}
			return nil
		},
		Publish: func(_ context.Context, generation combinedGeneration, _ string) (workrun.Publication, error) {
			f.publications++
			if generation.Plan.NugetCache == nil || generation.Plan.NugetCache.MaxPackages != 16 {
				t.Fatal("retained check plan lost selected package cache budget")
			}
			if !generation.Evidence.Passed || generation.Evidence.Head != generation.Revision.Head || generation.Evidence.Tree != generation.Revision.Tree {
				t.Fatal("publication lacks exact source check proof")
			}
			if f.publishError != nil {
				return workrun.Publication{}, f.publishError
			}
			return workrun.Publication{BaseSHA: base, HeadSHA: strings.Repeat("f", 40)}, nil
		},
	}
	return f
}
func (f *combinedFixture) combine(ctx context.Context) (workseries.Target, error) {
	return f.driver.Combine(ctx, f.ticket, f.integration, f.parents)
}

func TestCombinedDriverRetainsUnchangedSuccessAndFullContext(t *testing.T) {
	f := newCombinedFixture(t)
	first, err := f.combine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.combine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Base != second.Base || first.SHA != second.SHA || f.checks != 1 || f.publications != 1 {
		t.Fatalf("unchanged generation repeated work: checks %d publications %d", f.checks, f.publications)
	}
	var dependency workrun.DependencyContext
	if json.Unmarshal(first.Context, &dependency) != nil || dependency.IntegrationSHA != f.integration.SHA || dependency.SourceSHA == dependency.SignedSHA || dependency.SignedSHA != first.SHA || !dependency.Evidence.Passed || len(dependency.Parents) != 2 || dependency.Parents[0].RunID != f.parents[0].RunID {
		t.Fatalf("retained context missing exact source/sign/vector proof: %+v", dependency)
	}
	f.parents[0], f.parents[1] = f.parents[1], f.parents[0]
	reordered, err := f.combine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Base == first.Base || f.checks != 2 {
		t.Fatal("ordered vector change reused prior evidence")
	}
}
func TestCombinedDriverCachesCompletedFailureButRetriesCheckedPublication(t *testing.T) {
	t.Run("semantic failure", func(t *testing.T) {
		f := newCombinedFixture(t)
		f.checkError = workrun.ErrCheckFailed
		if _, err := f.combine(context.Background()); err == nil {
			t.Fatal("failed checks admitted")
		}
		if _, err := f.combine(context.Background()); err == nil {
			t.Fatal("retained failure admitted")
		}
		if f.checks != 1 || f.publications != 0 {
			t.Fatal("same failed vector repeated checks or published")
		}
	})
	t.Run("checked publication retry", func(t *testing.T) {
		f := newCombinedFixture(t)
		f.publishError = errors.New("transport unavailable")
		if _, err := f.combine(context.Background()); err == nil {
			t.Fatal("publication transport failure admitted")
		}
		f.publishError = nil
		if _, err := f.combine(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.checks != 1 || f.publications != 2 {
			t.Fatalf("checked evidence lost on retry: %d %d", f.checks, f.publications)
		}
	})
}
func TestCombinedDriverPreparingAndInterruptedCheckRecovery(t *testing.T) {
	t.Run("preparing interrupted", func(t *testing.T) {
		f := newCombinedFixture(t)
		calls := 0
		f.driver.combinedOps.Snapshot = func(_ context.Context, _ workseries.Target, attempt string) (workrun.BranchSnapshot, func(), error) {
			calls++
			if calls == 1 {
				if err := os.Mkdir(filepath.Join(attempt, "check-inputs"), 0700); err != nil {
					t.Fatal(err)
				}
				return workrun.BranchSnapshot{}, func() {}, errors.New("snapshot transport unavailable")
			}
			return f.source, func() {}, nil
		}
		if _, err := f.combine(context.Background()); err == nil {
			t.Fatal("preparation failure admitted")
		}
		if _, err := f.combine(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.checks != 1 || f.publications != 1 {
			t.Fatal("preparation retry duplicated later stages")
		}
	})
	t.Run("check interrupted", func(t *testing.T) {
		f := newCombinedFixture(t)
		f.checkError = context.Canceled
		if _, err := f.combine(context.Background()); err == nil {
			t.Fatal("interrupted check admitted")
		}
		f.checkError = nil
		if _, err := f.combine(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.checks != 2 || f.recoveries != 1 || f.publications != 1 {
			t.Fatalf("interrupted check did not recover once before retry: %+v", f)
		}
	})
}
