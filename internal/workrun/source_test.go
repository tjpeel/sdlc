package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/project"
)

func sourceFixture(t *testing.T) (string, project.LaunchResult) {
	t.Helper()
	ctx := context.Background()
	root := realTemp(t)
	if _, e := isolatedGit(ctx, root, "init", "-b", "main"); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{"user.name", "Example"}, {"user.email", "example@example.invalid"}} {
		if _, e := SafeGit(ctx, root, "config", pair[0], pair[1]); e != nil {
			t.Fatal(e)
		}
	}
	sourceWrite(t, root, "README.md", "original\n")
	sourceWrite(t, root, ".gitignore", "/.sdlc/work/\n/.secrets/\n")
	if _, e := SafeGit(ctx, root, "add", "."); e != nil {
		t.Fatal(e)
	}
	if _, e := SafeGit(ctx, root, "commit", "-m", "Initial example"); e != nil {
		t.Fatal(e)
	}
	sourceWrite(t, root, project.ConfigPath, `{"version":1,"checks":[],"input_files":[]}`)
	sourceWrite(t, root, ".sdlc/work/JOB:1/tickets/1-first.md", "disposable ticket\n")
	sourceWrite(t, root, ".sdlc/work/JOB:1/tickets/2-other.md", "unselected\n")
	launch, e := project.Launch(ctx, root, "JOB:1", "1-first.md", nil)
	if e != nil {
		t.Fatal(e)
	}
	return root, launch
}
func sourceWrite(t *testing.T, root, path, data string) {
	t.Helper()
	path = filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestCaptureDirtySourceAndExplicitInputs(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, "README.md", "local change\n")
	sourceWrite(t, root, "new.txt", "untracked\n")
	sourceWrite(t, root, ".secrets/hidden", "disposable secret\n")
	sourceWrite(t, root, "profiles.local.json", "disposable settings\n")
	destination := filepath.Join(realTemp(t), "workspace")
	t.Setenv("GIT_DIR", "/does/not/exist")
	plan, e := Capture(context.Background(), launch, destination, "ticket-work", "main", "example/repository", Roles{})
	if e != nil {
		t.Fatal(e)
	}
	if plan.StartingSHA == launch.Head || len(plan.Inputs) != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, path := range []string{"README.md", "new.txt", launch.Ticket} {
		a, _ := os.ReadFile(filepath.Join(root, path))
		b, e := os.ReadFile(filepath.Join(destination, path))
		if e != nil || string(a) != string(b) {
			t.Fatalf("missing copied content %s", path)
		}
	}
	for _, path := range []string{".secrets/hidden", "profiles.local.json", ".sdlc/work/JOB:1/tickets/2-other.md"} {
		if _, e := os.Lstat(filepath.Join(destination, path)); !os.IsNotExist(e) {
			t.Fatalf("copied excluded %s", path)
		}
	}
	if remote, _ := SafeGit(context.Background(), destination, "remote"); remote != "" {
		t.Fatal("retained remote")
	}
	status, e := SafeGit(context.Background(), destination, "status", "--porcelain")
	if e != nil || strings.TrimSpace(status) != "" {
		t.Fatalf("worker dirty: %s %v", status, e)
	}
	head, e := isolatedGit(context.Background(), root, "rev-parse", "HEAD")
	if e != nil || strings.TrimSpace(head) != launch.Head {
		t.Fatal("source HEAD changed")
	}
}
func TestCaptureRejectsSourceSymlink(t *testing.T) {
	root, launch := sourceFixture(t)
	if e := os.Symlink("README.md", filepath.Join(root, "unsafe")); e != nil {
		t.Fatal(e)
	}
	if _, e := Capture(context.Background(), launch, filepath.Join(realTemp(t), "workspace"), "ticket-work", "main", "example/repository", Roles{}); e == nil {
		t.Fatal("symlink accepted")
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	path, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0700); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestCaptureKeepsCheckInputsOutsideWorker(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".env", "DISPOSABLE_TEST=value\n")
	launch.Config.InputFiles = []string{".env"}
	destination := filepath.Join(realTemp(t), "workspace")
	plan, e := Capture(context.Background(), launch, destination, "ticket-work", "main", "example/repository", Roles{})
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.CheckInputs) != 1 || len(plan.Inputs) != 1 {
		t.Fatalf("wrong inputs: %+v", plan)
	}
	if _, e = os.Lstat(filepath.Join(destination, ".env")); !os.IsNotExist(e) {
		t.Fatal("check input entered provider workspace")
	}
	data, e := os.ReadFile(filepath.Join(filepath.Dir(destination), "check-inputs", ".env"))
	if e != nil || string(data) != "DISPOSABLE_TEST=value\n" {
		t.Fatal("check input not captured")
	}
}

func TestCaptureSnapshotUsesTrustedBundleAndOnlySelectedLocalInputs(t *testing.T) {
	root, launch := sourceFixture(t)
	// The snapshot represents the committed source while the selected ticket is
	// still copied from the original launch root.
	snapshotDir := realTemp(t)
	bundle := filepath.Join(snapshotDir, "source.bundle")
	if _, err := isolatedGit(context.Background(), root, "update-ref", "refs/sdlc/snapshot", launch.Head); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedGit(context.Background(), root, "bundle", "create", bundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, root, "unselected.txt", "must not enter snapshot staging\n")
	destination := filepath.Join(realTemp(t), "workspace")
	plan, err := CaptureSnapshot(context.Background(), launch, BranchSnapshot{Branch: "main", SHA: launch.Head, Bundle: bundle}, destination, "ticket-work", "example/repository", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Root != root || plan.Base != "main" || plan.BaseSHA != launch.Head {
		t.Fatalf("unexpected snapshot plan: %+v", plan)
	}
	if plan.SourceSHA != launch.Head || plan.StartingSHA != launch.Head {
		t.Fatalf("snapshot capture introduced unrelated baseline changes: %+v", plan)
	}
	if _, err := os.Lstat(filepath.Join(destination, "unselected.txt")); !os.IsNotExist(err) {
		t.Fatal("unselected local source entered worker")
	}
	if data, err := os.ReadFile(filepath.Join(destination, "README.md")); err != nil || string(data) != "original\n" {
		t.Fatalf("snapshot source was not materialized: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(destination, launch.Ticket)); err != nil || string(data) != "disposable ticket\n" {
		t.Fatalf("selected ticket missing: %v %q", err, data)
	}
}

func TestSafeGitRejectsHostileMetadata(t *testing.T) {
	root, _ := sourceFixture(t)
	sourceWrite(t, root, ".git/config", "[core]\n repositoryformatversion = 0\n bare = false\n[include]\n path = /does/not/exist\n")
	if _, e := SafeGit(context.Background(), root, "status", "--porcelain"); e == nil {
		t.Fatal("unsafe config accepted")
	}
	sourceWrite(t, root, ".git/config", "[core]\n repositoryformatversion = 0\n bare = false\n")
	if e := os.Symlink("/does/not/exist", filepath.Join(root, ".git", "objects", "info", "alternates")); e != nil {
		t.Fatal(e)
	}
	if _, e := SafeGit(context.Background(), root, "rev-parse", "HEAD"); e == nil {
		t.Fatal("metadata symlink accepted")
	}
}

func TestCaptureRejectsCredentialCheckInputs(t *testing.T) {
	for _, path := range []string{".secrets/private.env", ".secrets/.env", ".aws/credentials", ".ssh/key", ".codex/auth.json", ".claude/settings.json", "profiles.local.json", ".sdlc/work/JOB:1/tickets/1-first.md"} {
		t.Run(path, func(t *testing.T) {
			root, launch := sourceFixture(t)
			sourceWrite(t, root, path, "disposable fixture\n")
			launch.Config.InputFiles = []string{path}
			destination := filepath.Join(realTemp(t), "workspace")
			if _, e := Capture(context.Background(), launch, destination, "ticket-work", "main", "example/repository", Roles{}); e == nil {
				t.Fatal("credential check input accepted")
			}
			if _, e := os.Lstat(destination); !os.IsNotExist(e) {
				t.Fatal("rejected capture created worker")
			}
		})
	}
}

func TestCaptureRetainsTrackedProjectCodexInstructions(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".codex/instructions.md", "disposable project instructions\n")
	if _, e := SafeGit(context.Background(), root, "add", ".codex/instructions.md"); e != nil {
		t.Fatal(e)
	}
	if _, e := SafeGit(context.Background(), root, "commit", "-m", "Add project instructions"); e != nil {
		t.Fatal(e)
	}
	head, e := SafeGit(context.Background(), root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	launch.Head = strings.TrimSpace(head)
	destination := filepath.Join(realTemp(t), "workspace")
	if _, e = Capture(context.Background(), launch, destination, "ticket-work", "main", "example/repository", Roles{}); e != nil {
		t.Fatal(e)
	}
	if data, e := os.ReadFile(filepath.Join(destination, ".codex/instructions.md")); e != nil || string(data) != "disposable project instructions\n" {
		t.Fatal("project instructions were not retained")
	}
}

func TestCaptureExcludesUntrackedCodexState(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".codex/auth.json", "disposable account fixture\n")
	destination := filepath.Join(realTemp(t), "workspace")
	if _, e := Capture(context.Background(), launch, destination, "ticket-work", "main", "example/repository", Roles{}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Lstat(filepath.Join(destination, ".codex/auth.json")); !os.IsNotExist(e) {
		t.Fatal("untracked account state copied")
	}
}

func TestCaptureRejectsSourceBehindOrDivergedFromSelectedBase(t *testing.T) {
	for _, kind := range []string{"behind", "diverged"} {
		t.Run(kind, func(t *testing.T) {
			root, launch := sourceFixture(t)
			ctx := context.Background()
			git := func(args ...string) string {
				t.Helper()
				out, err := SafeGit(ctx, root, args...)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
			tree := git("rev-parse", "HEAD^{tree}")
			baseHead := git("commit-tree", tree, "-p", launch.Head, "-m", "Advance selected base")
			git("update-ref", "refs/heads/selected-base", baseHead)
			if kind == "diverged" {
				sourceHead := git("commit-tree", tree, "-p", launch.Head, "-m", "Advance source independently")
				git("update-ref", "refs/heads/main", sourceHead)
				launch.Head = sourceHead
			}
			destination := filepath.Join(realTemp(t), "workspace")
			_, err := Capture(ctx, launch, destination, "ticket-work", "selected-base", "example/repository", DefaultModels().Codex)
			if err == nil || !strings.Contains(err.Error(), "include the selected base") {
				t.Fatalf("source %s from base accepted or unclear error: %v", kind, err)
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatal("invalid base created provider workspace")
			}
			if head := git("rev-parse", "HEAD"); head != launch.Head {
				t.Fatal("capture changed source HEAD")
			}
		})
	}
}

func captureTemplateFixture(t *testing.T, launch project.LaunchResult, destination string, snapshot bool) error {
	t.Helper()
	ctx := context.Background()
	if !snapshot {
		_, err := Capture(ctx, launch, destination, "ticket-work", "main", "example/repository", Roles{})
		return err
	}
	bundle := filepath.Join(realTemp(t), "source.bundle")
	if _, err := isolatedGit(ctx, launch.Root, "update-ref", "refs/sdlc/snapshot", launch.Head); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedGit(ctx, launch.Root, "bundle", "create", bundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	_, err := CaptureSnapshot(ctx, launch, BranchSnapshot{Branch: "main", SHA: launch.Head, Bundle: bundle}, destination, "ticket-work", "example/repository", Roles{})
	return err
}

func commitSourceFixture(t *testing.T, root string, launch *project.LaunchResult) {
	t.Helper()
	ctx := context.Background()
	if _, err := SafeGit(ctx, root, "commit", "-m", "Add public test fixture"); err != nil {
		t.Fatal(err)
	}
	head, err := SafeGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	launch.Head = strings.TrimSpace(head)
}

func TestCaptureRetainsCommittedEnvironmentTemplate(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "source"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			root, launch := sourceFixture(t)
			const path = "tools/healthcheck/.env.example"
			const content = "PUBLIC_EXAMPLE=value\n"
			sourceWrite(t, root, path, content)
			if _, err := SafeGit(context.Background(), root, "add", "--", path); err != nil {
				t.Fatal(err)
			}
			commitSourceFixture(t, root, &launch)
			sourceWrite(t, root, "tools/local/.env.example", "UNTRACKED_EXAMPLE=value\n")
			destination := filepath.Join(realTemp(t), "workspace")
			if err := captureTemplateFixture(t, launch, destination, snapshot); err != nil {
				t.Fatalf("committed environment template rejected: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(destination, path)); err != nil || string(data) != content {
				t.Fatalf("committed template not retained: %v %q", err, data)
			}
			if _, err := os.Lstat(filepath.Join(destination, "tools/local/.env.example")); !os.IsNotExist(err) {
				t.Fatal("untracked template entered provider workspace")
			}
		})
	}
}

func TestCaptureRejectsPrivateAndUnsafeTrackedTemplates(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "source"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			for _, fixture := range []struct {
				path string
				mode string
			}{
				{path: "tools/.env"},
				{path: "tools/.env.production"},
				{path: "tools/.env.example.local"},
				{path: "tools/hostile\n\x1b[31m/.env"},
				{path: ".secrets/.env.example"},
				{path: "tools/healthcheck/.env.example", mode: "120000"},
				{path: "tools/healthcheck/.env.example", mode: "160000"},
			} {
				t.Run(fixture.path+fixture.mode, func(t *testing.T) {
					root, launch := sourceFixture(t)
					ctx := context.Background()
					const content = "PUBLIC_REJECTION_FIXTURE=value\n"
					if fixture.mode == "" {
						sourceWrite(t, root, fixture.path, content)
						if _, err := SafeGit(ctx, root, "add", "--force", "--", fixture.path); err != nil {
							t.Fatal(err)
						}
					} else {
						object := launch.Head
						if fixture.mode == "120000" {
							var err error
							object, err = SafeGit(ctx, root, "rev-parse", "HEAD:README.md")
							if err != nil {
								t.Fatal(err)
							}
						}
						if _, err := SafeGit(ctx, root, "update-index", "--add", "--cacheinfo", fixture.mode+","+strings.TrimSpace(object)+","+fixture.path); err != nil {
							t.Fatal(err)
						}
					}
					commitSourceFixture(t, root, &launch)
					destination := filepath.Join(realTemp(t), "workspace")
					err := captureTemplateFixture(t, launch, destination, snapshot)
					if err == nil || !strings.Contains(err.Error(), strconv.Quote(fixture.path)) || strings.ContainsAny(err.Error(), "\n\r\x1b") || strings.Contains(err.Error(), content) {
						t.Fatalf("expected rejection with escaped offending path %q: %v", fixture.path, err)
					}
					if _, err := os.Lstat(destination); !os.IsNotExist(err) {
						t.Fatal("rejected capture retained provider workspace")
					}
				})
			}
		})
	}
}
