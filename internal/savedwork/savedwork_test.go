package savedwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

const firstID = "0123456789abcdef01234567"
const secondID = "abcdef0123456789abcdef01"

func fixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	write(t, filepath.Join(root, ".git", "info", "exclude"), "/.sdlc/work/\n")
	state := filepath.Join(root, "state")
	mkdir(t, state)
	return root, state
}
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func write(t *testing.T, path, data string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func saved(t *testing.T, root, ref, id string) (Run, workrun.Journal) {
	return savedTicket(t, root, ref, id, "01-example.md")
}
func savedTicket(t *testing.T, root, ref, id, ticket string) (Run, workrun.Journal) {
	t.Helper()
	write(t, filepath.Join(root, ".sdlc", "work", ref, "tickets", ticket), "Disposable ticket\n")
	dir, err := workrun.RunDirectory(root, ref, ticket, id, true)
	if err != nil {
		t.Fatal(err)
	}
	j := workrun.Journal{Version: 1, ID: id, State: "implementing", Workspace: filepath.Join(dir, "workspace"), Plan: workrun.Plan{Root: root, Reference: ref, Ticket: filepath.ToSlash(filepath.Join(".sdlc", "work", ref, "tickets", ticket)), StartingSHA: strings.Repeat("a", 40), SourceSHA: strings.Repeat("b", 40), Roles: workrun.DefaultModels().Codex}}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	return Run{ID: id, Root: root, Reference: ref, Ticket: j.Plan.Ticket, Directory: dir}, j
}
func series(t *testing.T, root, ref string, runs ...Run) string {
	t.Helper()
	write(t, filepath.Join(root, ".sdlc", "work", ref, "tickets", "01-example.md"), "Disposable ticket\n")
	p, err := workseries.Discover(context.Background(), root, ref, "main")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := workseries.Directory(root, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	s := workseries.State{Version: 1, Plan: p, Results: map[string]workseries.Result{}}
	for _, run := range runs {
		s.Results[filepath.Base(run.Ticket)] = workseries.Result{RunID: run.ID, Directory: run.Directory, State: "blocked"}
	}
	if err := workseries.Save(dir, &s); err != nil {
		t.Fatal(err)
	}
	return dir
}
func exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if (err == nil) != want || err != nil && !os.IsNotExist(err) {
		t.Fatalf("exists(%s)=%v, want %v", filepath.Base(path), err, want)
	}
}

func TestOrphanPurgePreservesInputsAndPreview(t *testing.T) {
	root, state := fixture(t)
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	run, _ := saved(t, root, "Example", firstID)
	input := filepath.Join(root, ".sdlc", "work", "Example", "inputs", "example.txt")
	write(t, input, "Disposable input")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "retained.txt")
	write(t, sentinel, "Disposable external file")
	mkdir(t, filepath.Join(run.Directory, "workspace"))
	if err := os.Symlink(outside, filepath.Join(run.Directory, "workspace", "outside")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runs, err := Discover(ctx, root)
	if err != nil || len(runs) != 1 || runs[0] != run {
		t.Fatalf("Discover: %+v %v", runs, err)
	}
	preview, err := Remove(ctx, state, root, RemoveOptions{Run: "012", DryRun: true})
	if err != nil || len(preview.Runs) != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	exists(t, filepath.Join(state, "runtime-build.lock"), false)
	exists(t, state, false)
	exists(t, filepath.Join(run.Directory, "run.lock"), false)
	if _, err := Remove(ctx, state, root, RemoveOptions{Run: "012"}); err != nil {
		t.Fatal(err)
	}
	exists(t, run.Directory, false)
	exists(t, input, true)
	exists(t, sentinel, true)
	exists(t, filepath.Join(root, filepath.FromSlash(run.Ticket)), true)
	runs, err = Discover(ctx, root)
	if err != nil || len(runs) != 0 {
		t.Fatalf("after purge: %+v %v", runs, err)
	}
}

func TestPurgeDeregistersStoppedRun(t *testing.T) {
	root, state := fixture(t)
	run, j := saved(t, root, "Example", firstID)
	registry := runstatus.New(state)
	if err := registry.Register(run.Directory, j); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(context.Background(), state, root, RemoveOptions{Run: "012"}); err != nil {
		t.Fatal(err)
	}
	views, err := registry.List(time.Now())
	if err != nil || len(views) != 0 {
		t.Fatalf("registry after removal: %+v %v", views, err)
	}
}

func TestPurgeAbandonsOwnerAndAllRunlessSeries(t *testing.T) {
	root, state := fixture(t)
	run, _ := saved(t, root, "Example", firstID)
	other, _ := saved(t, root, "Example", secondID)
	owner := series(t, root, "Example", run)
	runless := series(t, root, "Frozen")
	ctx := context.Background()
	preview, err := Remove(ctx, state, root, RemoveOptions{Run: "012", DryRun: true})
	if err != nil || len(preview.Series) != 1 || preview.Series[0] != owner {
		t.Fatalf("owner preview: %+v %v", preview, err)
	}
	if _, err := Remove(ctx, state, root, RemoveOptions{Run: "012"}); err != nil {
		t.Fatal(err)
	}
	exists(t, owner, false)
	exists(t, other.Directory, true)
	exists(t, runless, true)
	preview, err = Remove(ctx, state, root, RemoveOptions{All: true, DryRun: true})
	if err != nil || len(preview.Series) != 1 || preview.Series[0] != runless {
		t.Fatalf("all preview: %+v %v", preview, err)
	}
	if _, err := Remove(ctx, state, root, RemoveOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	exists(t, other.Directory, false)
	exists(t, runless, false)
}

func TestActiveControllersAndRuntimeLeaseRefuseBatch(t *testing.T) {
	for _, kind := range []string{"run", "series", "runtime", "owned-sibling"} {
		t.Run(kind, func(t *testing.T) {
			root, state := fixture(t)
			run, _ := saved(t, root, "Example", firstID)
			other, _ := saved(t, root, "Other", secondID)
			path := filepath.Join(run.Directory, "run.lock")
			if kind == "series" {
				path = filepath.Join(series(t, root, "Example", run), "series.lock")
			}
			if kind == "owned-sibling" {
				owned, _ := savedTicket(t, root, "Example", "bbbbbbbbbbbbbbbbbbbbbbbb", "02-example.md")
				path = filepath.Join(owned.Directory, "run.lock")
				series(t, root, "Example", run, owned)
			}
			var lock *os.File
			var err error
			if kind == "runtime" {
				lock, err = filelock.AcquireContext(context.Background(), filepath.Join(state, "runtime-build.lock"), filelock.Shared, nil)
			} else {
				lock, err = filelock.Acquire(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			options := RemoveOptions{All: true}
			if kind == "owned-sibling" {
				options = RemoveOptions{Run: "012"}
			}
			if _, err := Remove(context.Background(), state, root, options); err == nil {
				t.Fatal("active controller allowed purge")
			}
			if _, err := Archive(context.Background(), state, root, "Example", false); err == nil {
				t.Fatal("active controller allowed archive")
			}
			exists(t, run.Directory, true)
			exists(t, other.Directory, true)
		})
	}
}

func TestArchivePreservesReferenceAndAllowsReuse(t *testing.T) {
	root, state := fixture(t)
	run, j := saved(t, root, "Example", firstID)
	series(t, root, "Example", run)
	registry := runstatus.New(state)
	if err := registry.Register(run.Directory, j); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(root, ".sdlc", "work", "Example")
	write(t, filepath.Join(ref, "spec.md"), "Disposable specification")
	write(t, filepath.Join(run.Directory, "workspace", "file.txt"), "Disposable workspace")
	if err := os.Symlink("/not-present", filepath.Join(run.Directory, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	preview, err := Archive(ctx, state, root, "Example", true)
	if err != nil || len(preview.Runs) != 1 {
		t.Fatalf("archive preview: %+v %v", preview, err)
	}
	exists(t, filepath.Join(state, "runtime-build.lock"), false)
	exists(t, filepath.Join(root, ".sdlc", "work", ".archive"), false)
	archived, err := Archive(ctx, state, root, "Example", false)
	if err != nil {
		t.Fatal(err)
	}
	exists(t, ref, false)
	exists(t, filepath.Join(archived.Destination, "spec.md"), true)
	exists(t, filepath.Join(archived.Destination, "runs", "01-example", firstID, "workspace", "link"), true)
	runs, err := Discover(ctx, root)
	if err != nil || len(runs) != 0 {
		t.Fatalf("archived runs discovered: %+v %v", runs, err)
	}
	views, err := registry.List(time.Now())
	if err != nil || len(views) != 0 {
		t.Fatalf("archived registry: %+v %v", views, err)
	}
	saved(t, root, "Example", secondID)
	second, err := Archive(ctx, state, root, "Example", false)
	if err != nil || second.Destination == archived.Destination {
		t.Fatalf("reused archive: %+v %v", second, err)
	}
	exists(t, archived.Destination, true)
	exists(t, second.Destination, true)
}

func TestArchiveReferenceWithoutTickets(t *testing.T) {
	root, state := fixture(t)
	path := filepath.Join(root, ".sdlc", "work", "Notes")
	write(t, filepath.Join(path, "spec.md"), "Disposable specification")
	result, err := Archive(context.Background(), state, root, "Notes", false)
	if err != nil {
		t.Fatal(err)
	}
	exists(t, filepath.Join(result.Destination, "spec.md"), true)
}

func TestUnsafeStorageRejectsWholeBatch(t *testing.T) {
	for _, kind := range []string{"journal-mode", "journal-link", "run-symlink", "lock-symlink", "registry-identity", "tracked", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			root, state := fixture(t)
			run, j := saved(t, root, "Example", firstID)
			other, _ := saved(t, root, "Other", secondID)
			switch kind {
			case "journal-mode":
				if err := os.Chmod(filepath.Join(run.Directory, "journal.json"), 0644); err != nil {
					t.Fatal(err)
				}
			case "journal-link":
				if err := os.Link(filepath.Join(run.Directory, "journal.json"), filepath.Join(root, "extra-link")); err != nil {
					t.Fatal(err)
				}
			case "run-symlink":
				if err := os.Rename(run.Directory, run.Directory+"-elsewhere"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(run.Directory+"-elsewhere", run.Directory); err != nil {
					t.Fatal(err)
				}
			case "lock-symlink":
				if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(run.Directory, "run.lock")); err != nil {
					t.Fatal(err)
				}
			case "registry-identity":
				if err := runstatus.New(state).Register(run.Directory, j); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(state, "runs", run.ID+".json"), `{"version":1,"id":"`+run.ID+`","directory":"/different"}`)
			case "tracked":
				c := exec.Command("git", "-C", root, "add", "-f", filepath.Join(root, filepath.FromSlash(run.Ticket)))
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("stage: %s %v", out, err)
				}
			case "malformed":
				write(t, filepath.Join(run.Directory, "journal.json"), "{}")
			}
			if _, err := Remove(context.Background(), state, root, RemoveOptions{All: true}); err == nil {
				t.Fatal("unsafe batch accepted")
			}
			exists(t, other.Directory, true)
		})
	}
}

func TestPreparationAndEmptyWorkDiscovery(t *testing.T) {
	root, state := fixture(t)
	mkdir(t, filepath.Join(root, ".sdlc", "work"))
	runs, err := Discover(context.Background(), root)
	if err != nil || len(runs) != 0 {
		t.Fatalf("empty: %+v %v", runs, err)
	}
	run, _ := saved(t, root, "Example", firstID)
	if err := os.Remove(filepath.Join(run.Directory, "journal.json")); err != nil {
		t.Fatal(err)
	}
	p := runstatus.PreparationFailure{Version: 1, ID: run.ID, Root: root, Reference: "Example", Ticket: "01-example.md", Roles: workrun.DefaultModels().Codex, FailedAt: time.Now().UTC(), Reason: "Disposable preparation failure", FeatureOwned: true}
	ownerSeries := series(t, root, "Example")
	owner, err := runstatus.OwnPreparation(run.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(state).RegisterPreparationFailure(run.Directory, p, owner); err != nil {
		t.Fatal(err)
	}
	owner.Close()
	runs, err = Discover(context.Background(), root)
	if err != nil || len(runs) != 1 || runs[0] != run {
		t.Fatalf("preparation: %+v %v", runs, err)
	}
	if _, err := Remove(context.Background(), state, root, RemoveOptions{Run: "012"}); err != nil {
		t.Fatal(err)
	}
	exists(t, ownerSeries, false)
}

func TestFailedWorkspaceDeletionRetainsSelectableCheckpoint(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses the disposable permission failure")
	}
	root, state := fixture(t)
	run, j := saved(t, root, "Example", firstID)
	registry := runstatus.New(state)
	if err := registry.Register(run.Directory, j); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(run.Directory, "workspace", "protected")
	write(t, filepath.Join(blocked, "file.txt"), "Disposable file")
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0700)
	if _, err := Remove(context.Background(), state, root, RemoveOptions{Run: "012"}); err == nil {
		t.Fatal("permission failure was not reported")
	}
	runs, err := Discover(context.Background(), root)
	if err != nil || len(runs) != 1 || runs[0] != run {
		t.Fatalf("failed removal lost checkpoint: %+v %v", runs, err)
	}
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(context.Background(), state, root, RemoveOptions{Run: "012"}); err != nil {
		t.Fatal(err)
	}
	exists(t, run.Directory, false)
}

func TestMissingWorkRejectsSymlinkAncestor(t *testing.T) {
	root, _ := fixture(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".sdlc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), root); err == nil {
		t.Fatal("symlink ancestor treated as empty work")
	}
}

func TestInstallationStateInsideWorkIsNeverMovedOrDeleted(t *testing.T) {
	root, _ := fixture(t)
	run, _ := saved(t, root, "Example", firstID)
	state := filepath.Join(run.Directory, "state")
	write(t, filepath.Join(state, "profile.json"), "Disposable installation metadata")
	ctx := context.Background()
	if _, err := Remove(ctx, state, root, RemoveOptions{Run: "012"}); err == nil {
		t.Fatal("purge accepted installation state inside run")
	}
	if _, err := Archive(ctx, state, root, "Example", false); err == nil {
		t.Fatal("archive accepted installation state inside reference")
	}
	exists(t, filepath.Join(state, "profile.json"), true)
}

func referenceMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestReadableScopingReferencesDoNotBlockSavedWork(t *testing.T) {
	root, state := fixture(t)
	referenceMode(t, root, 0755)
	run, _ := saved(t, root, "Example", firstID)
	work := filepath.Join(root, ".sdlc", "work")
	empty := filepath.Join(work, "Empty")
	mkdir(t, empty)
	referenceMode(t, empty, 0755)
	scoped := filepath.Join(work, "Scoping")
	spec := filepath.Join(scoped, "spec.md")
	write(t, spec, "Disposable scoping specification\n")
	referenceMode(t, scoped, 0755)
	write(t, filepath.Join(work, "notes.txt"), "Disposable ordinary work note\n")
	ctx := context.Background()
	runs, err := Discover(ctx, root)
	if err != nil || len(runs) != 1 || runs[0] != run {
		t.Fatalf("discovery with ordinary references: %+v %v", runs, err)
	}
	preview, err := Remove(ctx, state, root, RemoveOptions{All: true, DryRun: true})
	if err != nil || len(preview.Runs) != 1 || len(preview.Series) != 0 {
		t.Fatalf("all preview with ordinary references: %+v %v", preview, err)
	}
	if _, err := Archive(ctx, state, root, "Example", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(ctx, state, root, RemoveOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	exists(t, run.Directory, false)
	exists(t, empty, true)
	exists(t, spec, true)
	exists(t, filepath.Join(work, "notes.txt"), true)
	data, err := os.ReadFile(spec)
	if err != nil || string(data) != "Disposable scoping specification\n" {
		t.Fatalf("scoping input changed: %q %v", data, err)
	}
	for _, path := range []string{root, empty, scoped} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("ordinary directory mode changed: %s %v", filepath.Base(path), err)
		}
	}
}

func TestArchiveReadableReferencePreservesModeAndFullContents(t *testing.T) {
	root, state := fixture(t)
	referenceMode(t, root, 0755)
	run, j := saved(t, root, "Example", firstID)
	series(t, root, "Example", run)
	if err := runstatus.New(state).Register(run.Directory, j); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(root, ".sdlc", "work", "Example")
	referenceMode(t, ref, 0755)
	write(t, filepath.Join(ref, "spec.md"), "Disposable specification\n")
	write(t, filepath.Join(ref, "inputs", "example.txt"), "Disposable input\n")
	journal, err := os.ReadFile(filepath.Join(run.Directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := Archive(ctx, state, root, "Example", true); err != nil {
		t.Fatal(err)
	}
	result, err := Archive(ctx, state, root, "Example", false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(result.Destination)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("archived reference mode changed: %v", err)
	}
	for _, item := range []struct{ path, contents string }{{"spec.md", "Disposable specification\n"}, {"inputs/example.txt", "Disposable input\n"}, {"tickets/01-example.md", "Disposable ticket\n"}, {"runs/01-example/" + firstID + "/journal.json", string(journal)}} {
		data, err := os.ReadFile(filepath.Join(result.Destination, filepath.FromSlash(item.path)))
		if err != nil || string(data) != item.contents {
			t.Fatalf("archive changed %s: %v", item.path, err)
		}
	}
	exists(t, filepath.Join(result.Destination, "series", "journal.json"), true)
	// A runtime-free scoping reference follows the same permission policy.
	notes := filepath.Join(root, ".sdlc", "work", "Notes")
	write(t, filepath.Join(notes, "spec.md"), "Disposable scoping input")
	referenceMode(t, notes, 0755)
	result, err = Archive(ctx, state, root, "Notes", false)
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(result.Destination)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("scoping archive changed mode: %v", err)
	}
	exists(t, filepath.Join(result.Destination, "spec.md"), true)
}

func TestReferencePermissionsAndLinksStillFailClosed(t *testing.T) {
	for _, kind := range []string{"group-writable", "other-writable", "symlink", "runtime-mode", "malformed-runtime"} {
		t.Run(kind, func(t *testing.T) {
			root, state := fixture(t)
			run, _ := saved(t, root, "Example", firstID)
			ref := filepath.Join(root, ".sdlc", "work", "Unsafe")
			mkdir(t, ref)
			badPath := ref
			switch kind {
			case "group-writable":
				referenceMode(t, ref, 0775)
			case "other-writable":
				referenceMode(t, ref, 0757)
			case "symlink":
				if err := os.Remove(ref); err != nil {
					t.Fatal(err)
				}
				outside, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, ref); err != nil {
					t.Fatal(err)
				}
			case "runtime-mode":
				referenceMode(t, ref, 0755)
				badPath = filepath.Join(ref, "runs")
				mkdir(t, badPath)
				referenceMode(t, badPath, 0755)
			case "malformed-runtime":
				other, _ := saved(t, root, "Unsafe", secondID)
				referenceMode(t, ref, 0755)
				badPath = other.Directory
				write(t, filepath.Join(other.Directory, "journal.json"), "{}")
			}
			ctx := context.Background()
			if _, err := Discover(ctx, root); err == nil {
				t.Fatal("unsafe reference allowed discovery")
			} else if !strings.Contains(err.Error(), badPath) {
				t.Fatalf("error does not identify unsafe directory: %v", err)
			}
			if _, err := Remove(ctx, state, root, RemoveOptions{All: true}); err == nil {
				t.Fatal("unsafe reference allowed purge")
			}
			if _, err := Archive(ctx, state, root, "Example", false); err == nil {
				t.Fatal("unsafe sibling allowed archive")
			}
			exists(t, run.Directory, true)
		})
	}
}

func TestEmptyPreparationSiblingDoesNotBlockSavedWork(t *testing.T) {
	root, state := fixture(t)
	run, _ := saved(t, root, "Example", firstID)
	empty, err := workrun.RunDirectory(root, "Pending", "01-pending.md", secondID, true)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".sdlc", "work", "Pending", "spec.md"), "Disposable pending specification")
	ctx := context.Background()
	runs, err := Discover(ctx, root)
	if err != nil || len(runs) != 1 || runs[0] != run {
		t.Fatalf("empty preparation blocked discovery: %+v %v", runs, err)
	}
	preview, err := Remove(ctx, state, root, RemoveOptions{All: true, DryRun: true})
	if err != nil || len(preview.Runs) != 1 {
		t.Fatalf("empty preparation blocked purge preview: %+v %v", preview, err)
	}
	if _, err := Archive(ctx, state, root, "Example", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(ctx, state, root, RemoveOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	exists(t, run.Directory, false)
	exists(t, empty, true)
	exists(t, filepath.Join(empty, "run.lock"), false)
	archived, err := Archive(ctx, state, root, "Pending", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived.Runs) != 0 {
		t.Fatalf("empty directory advertised as saved run: %+v", archived.Runs)
	}
	exists(t, filepath.Join(archived.Destination, "runs", "01-pending", secondID), true)
	exists(t, filepath.Join(archived.Destination, "spec.md"), true)
}

func TestNonemptyCheckpointlessPreparationStillFailsClosed(t *testing.T) {
	for _, entry := range []string{"run.lock", "activity.json", "unexpected.txt"} {
		t.Run(entry, func(t *testing.T) {
			root, state := fixture(t)
			run, _ := saved(t, root, "Example", firstID)
			empty, err := workrun.RunDirectory(root, "Pending", "01-pending.md", secondID, true)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(empty, entry), "Disposable incomplete preparation metadata")
			ctx := context.Background()
			if _, err := Discover(ctx, root); err == nil || !strings.Contains(err.Error(), empty) {
				t.Fatalf("nonempty preparation escaped validation: %v", err)
			}
			if _, err := Remove(ctx, state, root, RemoveOptions{All: true}); err == nil {
				t.Fatal("nonempty preparation allowed purge")
			}
			if _, err := Archive(ctx, state, root, "Example", false); err == nil {
				t.Fatal("nonempty preparation allowed archive")
			}
			exists(t, run.Directory, true)
			exists(t, filepath.Join(empty, entry), true)
		})
	}
}
