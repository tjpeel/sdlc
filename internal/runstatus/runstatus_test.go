package runstatus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func fixture(t *testing.T) (*Registry, string, workrun.Journal) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef01234567"
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j := workrun.Journal{Version: 1, ID: id, State: "implementing", Workspace: filepath.Join(dir, "workspace"),
		Plan: workrun.Plan{Root: root, Reference: "example", Ticket: "ticket.md", StartingSHA: strings.Repeat("a", 40), SourceSHA: strings.Repeat("b", 40), Roles: workrun.DefaultModels().Codex}}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(root, "state")), dir, j
}

func onlyView(t *testing.T, r *Registry, now time.Time) View {
	t.Helper()
	views, err := r.List(now)
	if err != nil || len(views) != 1 {
		t.Fatalf("List: %+v, %v", views, err)
	}
	return views[0]
}

func TestRegistrationRequiresCheckpointAndUniqueIdentity(t *testing.T) {
	r, dir, j := fixture(t)
	if err := r.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	other := j
	other.ID = "abcdef0123456789abcdef01"
	if err := r.Register(dir, other); err == nil {
		t.Fatal("mismatched identity accepted")
	}
	other = j
	other.Plan.Ticket = "different.md"
	if err := r.Register(dir, other); err == nil {
		t.Fatal("mismatched ticket accepted")
	}
	second := filepath.Join(filepath.Dir(dir), "elsewhere", j.ID)
	if err := os.MkdirAll(second, 0700); err != nil {
		t.Fatal(err)
	}
	other = j
	other.Workspace = filepath.Join(second, "workspace")
	if err := workrun.Save(second, &other); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(second, other); err == nil {
		t.Fatal("reused identity accepted")
	}
	if err := os.Remove(filepath.Join(dir, "journal.json")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(dir, j); err == nil {
		t.Fatal("missing checkpoint accepted")
	}
	v := onlyView(t, r, time.Now())
	if v.Available || v.ID != j.ID || v.Directory != dir || !v.NeedsAttention {
		t.Fatalf("missing journal hidden: %+v", v)
	}
}

func TestRegistryIsPrivateAndDoesNotCopyTicketText(t *testing.T) {
	r, dir, j := fixture(t)
	j.Instructions = "private-ticket-text-not-for-the-index"
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{r.stateDir, filepath.Join(r.stateDir, "runs")} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory mode %s: %v %v", p, info, err)
		}
	}
	path := filepath.Join(r.stateDir, "runs", j.ID+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("entry mode: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(j.Instructions)) {
		t.Fatal("registry retained private ticket text")
	}
	v := onlyView(t, r, time.Now())
	if !v.Available || v.StartedAt.IsZero() || v.Journal == nil || v.Journal.Instructions != j.Instructions {
		t.Fatalf("journal detail missing: %+v", v)
	}
}

func TestUnavailableEntriesAreRetained(t *testing.T) {
	for _, kind := range []string{"corrupt-entry", "large-entry", "entry-symlink", "journal-symlink", "directory-symlink", "corrupt-journal"} {
		t.Run(kind, func(t *testing.T) {
			r, dir, j := fixture(t)
			if err := r.Register(dir, j); err != nil {
				t.Fatal(err)
			}
			entryPath := filepath.Join(r.stateDir, "runs", j.ID+".json")
			journalPath := filepath.Join(dir, "journal.json")
			switch kind {
			case "corrupt-entry":
				os.WriteFile(entryPath, []byte("{"), 0600)
			case "large-entry":
				os.WriteFile(entryPath, bytes.Repeat([]byte("x"), maxJSON+1), 0600)
			case "entry-symlink", "journal-symlink":
				path := entryPath
				if kind == "journal-symlink" {
					path = journalPath
				}
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Rename(dir, dir+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".real", dir); err != nil {
					t.Fatal(err)
				}
			case "corrupt-journal":
				os.WriteFile(journalPath, []byte("{}"), 0600)
			}
			v := onlyView(t, r, time.Now())
			if v.Available || v.Error == "" || v.ID != j.ID || !v.NeedsAttention {
				t.Fatalf("bad entry hidden: %+v", v)
			}
		})
	}
}

func TestLivenessUsesHeartbeatNotOutputOrStage(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	v := onlyView(t, r, time.Now())
	if !v.Live || v.Stale || v.Stopped || v.NeedsAttention {
		t.Fatalf("fresh controller: %+v", v)
	}
	a := v.Activity
	a.LastActivityAt = time.Now().Add(-time.Hour)
	if err := replaceJSON(filepath.Join(dir, "activity.json"), a); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if !v.Live || v.Stale {
		t.Fatal("quiet controller became stale")
	}
	v = onlyView(t, r, a.HeartbeatAt.Add(StaleAfter+time.Second))
	if v.Live || !v.Stale || v.Stopped || !v.NeedsAttention || v.State != "implementing" {
		t.Fatalf("stale active stage: %+v", v)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if v.Live || v.Stale || !v.Stopped || !v.NeedsAttention {
		t.Fatalf("interrupted stage: %+v", v)
	}
	j.State = "ready"
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if !v.Stopped || !v.NeedsAttention {
		t.Fatalf("completed controller: %+v", v)
	}
}

func TestTrackerConcurrentOutputAndUpdates(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				tracker.Activity([]byte("sample"))
				if err := tracker.Update(j); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	j.State = "reviewing"
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Writer(nil).Write([]byte("review")); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	var a Snapshot
	if err := readJSON(filepath.Join(dir, "activity.json"), &a); err != nil {
		t.Fatal(err)
	}
	if a.OutputBytes != 1206 || a.Provider != j.Plan.Roles.Review.Provider || a.Role != "review" || !a.Stopped || a.StoppedAt.IsZero() {
		t.Fatalf("activity lost: %+v", a)
	}
	info, err := os.Stat(filepath.Join(dir, "activity.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode: %v %v", info, err)
	}
	persisted, err := workrun.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != "implementing" {
		t.Fatal("tracker mutated journal")
	}
	if err := tracker.Update(j); err == nil {
		t.Fatal("update after close accepted")
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOldTrackerCannotStopNewController(t *testing.T) {
	_, dir, j := fixture(t)
	old, err := Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Begin(dir, j)
	if err != nil {
		old.Close()
		t.Fatal(err)
	}
	defer current.Close()
	if err := old.Close(); err == nil {
		t.Fatal("old controller overwrote new controller")
	}
	var a Snapshot
	if err := readJSON(filepath.Join(dir, "activity.json"), &a); err != nil {
		t.Fatal(err)
	}
	if a.Stopped || a.ControllerID != current.snapshot.ControllerID {
		t.Fatal("new controller stopped")
	}
}

func TestCorruptActivityPreservesJournal(t *testing.T) {
	r, dir, j := fixture(t)
	if err := r.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("{"), []byte(`{"version":1}`), bytes.Repeat([]byte("x"), maxJSON+1)} {
		if err := os.WriteFile(filepath.Join(dir, "activity.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		v := onlyView(t, r, time.Now())
		if !v.Available || v.State != j.State || v.ActivityError == "" || !v.NeedsAttention {
			t.Fatalf("journal hidden by activity: %+v", v)
		}
	}
}

func TestFutureHeartbeatIsUnavailable(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	v := onlyView(t, r, time.Now())
	a := v.Activity
	a.HeartbeatAt = time.Now().Add(time.Minute)
	if err := replaceJSON(filepath.Join(dir, "activity.json"), a); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if !v.Available || v.Live || v.ActivityError == "" || !v.NeedsAttention {
		t.Fatalf("future heartbeat accepted: %+v", v)
	}
}

func TestRejectSymlinkWritesAndTrailingJSON(t *testing.T) {
	r, dir, j := fixture(t)
	if err := r.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "activity.json")
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if tracker, err := Begin(dir, j); err == nil {
		tracker.Close()
		t.Fatal("symlink activity accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "untouched" {
		t.Fatal("target changed")
	}
	entryPath := filepath.Join(r.stateDir, "runs", j.ID+".json")
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, append(data, []byte("{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if v := onlyView(t, r, time.Now()); v.Available || v.Error == "" {
		t.Fatal("trailing JSON accepted")
	}
}
