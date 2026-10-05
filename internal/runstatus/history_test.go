package runstatus

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func historyFixture(t *testing.T) (*Registry, string, workrun.Journal) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("private history ownership checks are unsupported on Windows")
	}
	r, directory, journal := fixture(t)
	if err := r.Register(directory, journal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceJSON(filepath.Join(directory, "activity.json"), Snapshot{Version: 1, ID: journal.ID, ControllerID: "abcdefabcdefabcdefabcdef", HeartbeatAt: time.Now().UTC(), Stopped: true}); err != nil {
		t.Fatal(err)
	}
	return r, directory, journal
}

func historyTree(t *testing.T, directory string) string {
	t.Helper()
	hash := sha256.New()
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s:%s\n", relative, info.Mode())
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash.Write(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func TestForgetOnlyRemovesRegistrationAndRetainsArtifactsForResume(t *testing.T) {
	r, directory, journal := historyFixture(t)
	for _, name := range []string{"workspace/source.go", "logs/provider.jsonl", "inputs/requirements.md", "source.bundle"} {
		path := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("disposable offline artifact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := historyTree(t, directory)
	registryLockPath := filepath.Join(r.stateDir, "runs", journal.ID+".lock")
	registryLock, _ := os.Stat(registryLockPath)
	controllerLock, _ := os.Stat(filepath.Join(directory, "run.lock"))
	if err := r.Forget(journal.ID); err != nil {
		t.Fatal(err)
	}
	views, err := r.List(time.Now())
	if err != nil || len(views) != 0 {
		t.Fatal("forgotten run remains listed", err)
	}
	if historyTree(t, directory) != before {
		t.Fatal("forget changed retained run artifacts")
	}
	entries, err := os.ReadDir(filepath.Join(r.stateDir, "runs"))
	if err != nil || len(entries) != 1 || entries[0].Name() != journal.ID+".lock" {
		t.Fatal("forget removed more than registry JSON", err)
	}
	for _, held := range []struct {
		path string
		info os.FileInfo
	}{{registryLockPath, registryLock}, {filepath.Join(directory, "run.lock"), controllerLock}} {
		info, err := os.Stat(held.path)
		if err != nil || !os.SameFile(info, held.info) {
			t.Fatal("forget changed lock inode", err)
		}
	}
	lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := r.Register(directory, journal); err != nil {
		t.Fatal("resume could not register retained checkpoint", err)
	}
	if view := onlyView(t, r, time.Now()); view.ID != journal.ID || !view.Available {
		t.Fatal("re-registration lost journal identity")
	}
	if historyTree(t, directory) != before {
		t.Fatal("registration changed retained artifacts")
	}
}

func TestForgetRefusesLiveAndBusyControllersIncludingStaleHeartbeat(t *testing.T) {
	for _, kind := range []string{"live", "busy stopped", "stale busy", "unavailable busy"} {
		t.Run(kind, func(t *testing.T) {
			r, directory, journal := historyFixture(t)
			activity := Snapshot{Version: 1, ID: journal.ID, ControllerID: "abcdefabcdefabcdefabcdef", HeartbeatAt: time.Now().UTC(), Stopped: kind == "busy stopped"}
			if kind == "stale busy" {
				activity.HeartbeatAt = time.Now().Add(-2 * StaleAfter)
			}
			if err := replaceJSON(filepath.Join(directory, "activity.json"), activity); err != nil {
				t.Fatal(err)
			}
			if kind == "unavailable busy" {
				if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "live" {
				lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			before := historyTree(t, directory)
			if err := r.Forget(journal.ID); err == nil {
				t.Fatal("live or busy controller forgotten")
			}
			if _, err := os.Stat(filepath.Join(r.stateDir, "runs", journal.ID+".json")); err != nil || historyTree(t, directory) != before {
				t.Fatal("refusal changed run artifacts or registry", err)
			}
		})
	}
}

func TestForgetAllowsUnavailableAndStoppedLegacyOnlyWithRealLock(t *testing.T) {
	for _, kind := range []string{"missing journal", "corrupt journal", "stale unlocked", "legacy absent lock", "missing directory"} {
		t.Run(kind, func(t *testing.T) {
			r, directory, journal := historyFixture(t)
			switch kind {
			case "missing journal":
				if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt journal":
				if err := os.WriteFile(filepath.Join(directory, "journal.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stale unlocked":
				if err := replaceJSON(filepath.Join(directory, "activity.json"), Snapshot{Version: 1, ID: journal.ID, ControllerID: "abcdefabcdefabcdefabcdef", HeartbeatAt: time.Now().Add(-2 * StaleAfter)}); err != nil {
					t.Fatal(err)
				}
			case "legacy absent lock":
				if err := os.Remove(filepath.Join(directory, "run.lock")); err != nil {
					t.Fatal(err)
				}
			case "missing directory":
				if err := os.Rename(directory, directory+".retained"); err != nil {
					t.Fatal(err)
				}
			}
			err := r.Forget(journal.ID)
			if kind == "missing directory" {
				if err == nil || !strings.Contains(err.Error(), "restore") {
					t.Fatal("missing directory forgotten without its real controller lock", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(directory, "run.lock")); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("controller lock missing or unsafe", err)
			}
			if kind == "corrupt journal" {
				data, err := os.ReadFile(filepath.Join(directory, "journal.json"))
				if err != nil || string(data) != "{" {
					t.Fatal("corrupt journal changed", err)
				}
			}
		})
	}
}

func TestForgetRejectsUnsafeStorageAndUntrustedMetadata(t *testing.T) {
	for _, kind := range []string{"entry symlink", "entry hardlink", "entry public", "entry readonly", "corrupt entry", "wrong ID", "relative directory", "oversize entry", "run lock symlink", "run lock hardlink", "registry lock symlink", "registry lock hardlink", "run directory symlink", "registry directory symlink", "state directory symlink", "public run directory", "public registry directory"} {
		t.Run(kind, func(t *testing.T) {
			r, directory, journal := historyFixture(t)
			entryPath := filepath.Join(r.stateDir, "runs", journal.ID+".json")
			path := entryPath
			if strings.HasPrefix(kind, "run lock") {
				path = filepath.Join(directory, "run.lock")
			} else if strings.HasPrefix(kind, "registry lock") {
				path = filepath.Join(r.stateDir, "runs", journal.ID+".lock")
			}
			switch {
			case strings.HasSuffix(kind, "symlink"):
				if kind == "run directory symlink" {
					path = directory
				} else if kind == "registry directory symlink" {
					path = filepath.Join(r.stateDir, "runs")
				} else if kind == "state directory symlink" {
					path = r.stateDir
				}
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			case strings.HasSuffix(kind, "hardlink"):
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			case kind == "entry public" || kind == "entry readonly":
				mode := fs.FileMode(0644)
				if kind == "entry readonly" {
					mode = 0400
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case strings.HasPrefix(kind, "public "):
				path = directory
				if kind == "public registry directory" {
					path = filepath.Join(r.stateDir, "runs")
				}
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			default:
				data := "{"
				if kind == "oversize entry" {
					data = strings.Repeat("x", maxJSON+1)
				} else if kind != "corrupt entry" {
					record := entry{1, journal.ID, directory, journal.Plan.Root, journal.Plan.Reference, journal.Plan.Ticket, false}
					if kind == "wrong ID" {
						record.ID = "abcdefabcdefabcdefabcdef"
					} else {
						record.Directory = "../relative"
					}
					if err := replaceJSON(entryPath, record); err != nil {
						t.Fatal(err)
					}
					data = ""
				}
				if data != "" {
					if err := os.WriteFile(entryPath, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := r.Forget(journal.ID); err == nil {
				t.Fatal("unsafe storage or identity allowed removal")
			}
			if _, err := os.Lstat(entryPath); err != nil {
				t.Fatal("unsafe entry was deleted", err)
			}
		})
	}
}

func TestForgetRequiresFullExistingIDWithoutCreatingStorage(t *testing.T) {
	r, directory, journal := historyFixture(t)
	before := historyTree(t, filepath.Dir(directory))
	for _, id := range []string{"", journal.ID[:8], "../" + journal.ID, strings.Repeat("f", 24)} {
		if err := r.Forget(id); err == nil {
			t.Fatal("missing or invalid ID accepted")
		}
	}
	if historyTree(t, filepath.Dir(directory)) != before {
		t.Fatal("invalid removal created or changed storage")
	}
}

func TestForgetTakesControllerBeforeRegistryMutexAndRechecksIdentity(t *testing.T) {
	r, directory, journal := historyFixture(t)
	controllerPath := filepath.Join(directory, "run.lock")
	controller, err := filelock.Acquire(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	result := make(chan error, 1)
	go func() { result <- r.Forget(journal.ID) }()
	select {
	case err := <-result:
		if err == nil {
			t.Error("busy controller forgotten")
		}
	case <-time.After(time.Second):
		t.Error("forget waited on registry mutex before checking controller")
	}
	r.mu.Unlock()
	controller.Close()
	if t.Failed() {
		return
	}
	r.mu.Lock()
	go func() { result <- r.Forget(journal.ID) }()
	deadline := time.Now().Add(time.Second)
	acquired := false
	for time.Now().Before(deadline) {
		lock, err := filelock.Acquire(controllerPath)
		if err != nil {
			acquired = true
			break
		}
		lock.Close()
		time.Sleep(time.Millisecond)
	}
	if !acquired {
		r.mu.Unlock()
		t.Fatal("forget never acquired controller lock")
	}
	path := filepath.Join(r.stateDir, "runs", journal.ID+".json")
	if err := replaceJSON(path, entry{1, journal.ID, directory, journal.Plan.Root, "changed-reference", journal.Plan.Ticket, false}); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	r.mu.Unlock()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatal("entry replacement during lock acquisition ignored", err)
		}
	case <-time.After(time.Second):
		t.Fatal("forget did not finish after registry mutex released")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("replacement entry deleted", err)
	}
}

func TestForgetRefusesBusyRegistryAndReleasesControllerLock(t *testing.T) {
	r, directory, journal := historyFixture(t)
	path := filepath.Join(r.stateDir, "runs", journal.ID+".lock")
	registration, err := filelock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	before := historyTree(t, filepath.Dir(directory))
	if err := r.Forget(journal.ID); err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatal("busy registration removed", err)
	}
	if historyTree(t, filepath.Dir(directory)) != before {
		t.Fatal("busy registration changed artifacts")
	}
	controller, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		t.Fatal("refused removal retained controller lock", err)
	}
	controller.Close()
}

type historyUnknownOwner struct{ os.FileInfo }

func (historyUnknownOwner) Sys() any { return nil }

func TestForgetOwnershipChecksRequireKnownSameOwner(t *testing.T) {
	_, directory, _ := historyFixture(t)
	info, err := os.Stat(filepath.Join(directory, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !historyOwned(info, true) {
		t.Fatal("disposable owned private lock rejected")
	}
	if historyOwned(historyUnknownOwner{info}, true) || historyOwned(historyUnknownOwner{info}, false) {
		t.Fatal("unverifiable ownership accepted")
	}
}
