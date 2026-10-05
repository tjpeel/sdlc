package runstatus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func preparationFixture(t *testing.T) (*Registry, string, PreparationFailure) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := workrun.RunDirectory(root, "example", "01-ticket.md", "0123456789abcdef01234567", true)
	if err != nil {
		t.Fatal(err)
	}
	p := PreparationFailure{Version: 1, ID: filepath.Base(directory), Root: root, Reference: "example", Ticket: "01-ticket.md", Roles: workrun.DefaultModels().Codex, FailedAt: time.Now().UTC(), Reason: "public fixture capture failed", FeatureOwned: true}
	return New(filepath.Join(root, "state")), directory, p
}

func registerPreparationFixture(t *testing.T, r *Registry, directory string, p PreparationFailure) {
	t.Helper()
	owner, err := OwnPreparation(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := r.RegisterPreparationFailure(directory, p, owner); err != nil {
		t.Fatal(err)
	}
}

func TestPreparationFailureListsOriginalReasonAndPromotesJournal(t *testing.T) {
	r, directory, p := preparationFixture(t)
	registerPreparationFixture(t, r, directory, p)
	v := onlyView(t, r, time.Now())
	if !v.Available || v.State != "blocked" || !v.NeedsAttention || !v.Stopped || v.Live || v.Journal != nil || v.StopReason != p.Reason || !v.Preparation || !v.FeatureOwned || v.Model != p.Roles.Implementation.Name {
		t.Fatalf("preparation failure hidden: %+v", v)
	}
	_, _, j := fixture(t)
	j.ID, j.Plan.Root, j.Plan.Reference, j.Plan.Ticket = p.ID, p.Root, p.Reference, ".sdlc/work/example/tickets/01-ticket.md"
	j.Workspace = filepath.Join(directory, "workspace")
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(directory, j); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if v.Journal == nil || v.Preparation || v.State != "implementing" {
		t.Fatalf("journal did not supersede preparation: %+v", v)
	}
	var record entry
	if err := readJSON(filepath.Join(r.stateDir, "runs", p.ID+".json"), &record); err != nil || record.Preparation {
		t.Fatalf("registration was not promoted: %+v %v", record, err)
	}
}

func TestPreparationFallbackRejectsUnsafeOrPresentJournals(t *testing.T) {
	for _, change := range []string{"corrupt journal", "symlink journal", "public metadata", "linked metadata", "wrong identity", "future failure"} {
		t.Run(change, func(t *testing.T) {
			r, directory, p := preparationFixture(t)
			registerPreparationFixture(t, r, directory, p)
			path := filepath.Join(directory, "preparation.json")
			switch change {
			case "corrupt journal":
				if err := os.WriteFile(filepath.Join(directory, "journal.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink journal":
				if err := os.Symlink("missing", filepath.Join(directory, "journal.json")); err != nil {
					t.Fatal(err)
				}
			case "public metadata":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "linked metadata":
				if err := os.Link(path, filepath.Join(directory, "alias")); err != nil {
					t.Fatal(err)
				}
			case "wrong identity":
				p.Reference = "other"
				if err := replaceJSON(path, p); err != nil {
					t.Fatal(err)
				}
			case "future failure":
				p.FailedAt = time.Now().Add(time.Hour)
				if err := replaceJSON(path, p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadPreparation(directory); err == nil {
				t.Fatal("unsafe preparation fallback accepted")
			}
			if v := onlyView(t, r, time.Now()); v.Available || v.Error == "" || v.Preparation {
				t.Fatalf("unsafe preparation shown as recoverable: %+v", v)
			}
		})
	}
}

func TestPreparationRetryAndForgetRespectOwnershipAndLeftovers(t *testing.T) {
	r, directory, p := preparationFixture(t)
	registerPreparationFixture(t, r, directory, p)
	owner, err := OwnPreparation(directory)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := ValidatePreparationRetry(directory, p); err == nil {
		retry.Close()
		t.Fatal("busy preparation retried")
	}
	if err := r.Forget(p.ID); err == nil {
		t.Fatal("busy preparation forgotten")
	}
	owner.Close()
	for _, name := range []string{"workspace", "check-inputs", "events-1.jsonl", "snapshot.bundle", "unknown"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("retained public fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if retry, err := ValidatePreparationRetry(directory, p); err == nil {
			retry.Close()
			t.Fatalf("leftover %s accepted", name)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "retained public fixture" {
			t.Fatal("uncertain leftover changed")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	owner, err = ValidatePreparationRetry(directory, p)
	if err != nil {
		t.Fatal(err)
	}
	owner.Close()
	if v := onlyView(t, r, time.Now()); v.StopReason != p.Reason {
		t.Fatal("retry validation erased original failure")
	}
	if err := r.Forget(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPreparation(directory); err != nil {
		t.Fatal("forget removed preparation state")
	}
}

func TestPreparationRegistrationRejectsInvalidMetadataWithoutEntry(t *testing.T) {
	r, directory, p := preparationFixture(t)
	owner, err := OwnPreparation(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	p.FailedAt = time.Time{}
	if err := r.RegisterPreparationFailure(directory, p, owner); err == nil {
		t.Fatal("invalid failure registered")
	}
	views, err := r.List(time.Now())
	if err != nil || len(views) != 0 {
		t.Fatalf("invalid failure left orphan entry: %+v %v", views, err)
	}
}
