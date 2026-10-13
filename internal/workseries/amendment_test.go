package workseries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func amendmentFixture(t *testing.T) (string, string, State, func(State) error) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("offline fixture: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".sdlc", "work", "Example")
	tickets := filepath.Join(work, "tickets")
	if err := os.MkdirAll(tickets, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01-a.md", "02-b.md", "03-c.md"} {
		if err := os.WriteFile(filepath.Join(tickets, name), []byte("Disposable offline requirement\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Discover(context.Background(), root, "Example", "main")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := Directory(root, "Example", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "series.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	state := State{Version: 1, Plan: plan, Settings: json.RawMessage(`{"frozen_account":"example","image":"offline-pinned","model":"offline-model","input_hash":"unchanged"}`), Results: map[string]Result{}}
	for i, name := range []string{"01-a.md", "02-b.md"} {
		id := strings.Repeat(string(rune('1'+i)), 24)
		directory := filepath.Join(work, "runs", strings.TrimSuffix(name, ".md"), id)
		os.MkdirAll(directory, 0700)
		os.WriteFile(filepath.Join(directory, "journal.json"), []byte(`{"session":"offline-original","attempt":7,"rounds":2}`), 0600)
		state.Results[name] = Result{RunID: id, Directory: directory, State: "ready", Branch: "example-" + name, Base: "main", BaseSHA: sha("base"), HeadSHA: sha(name), PRNumber: i + 1}
	}
	if err := Save(dir, &state); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(tickets, "03-c.md")
	expected, _ := os.ReadFile(input)
	validate := func(s State) error {
		if !bytes.Equal(s.Settings, state.Settings) {
			return errors.New("settings changed")
		}
		actual, err := os.ReadFile(input)
		if err != nil || !bytes.Equal(actual, expected) {
			return errors.New("input changed")
		}
		return nil
	}
	return root, dir, state, validate
}
func TestDependencyAmendmentPreservesJobsAndUsesExistingCombinedGate(t *testing.T) {
	root, dir, old, validate := amendmentFixture(t)
	before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	preview, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, true, validate)
	if err != nil || !preview.Changed || !preview.DryRun {
		t.Fatal(preview, err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "journal.json")); !bytes.Equal(before, after) {
		t.Fatal("preview changed checkpoint")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "plan.json")); !os.IsNotExist(err) {
		t.Fatal("preview created definition")
	}
	if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); err != nil {
		t.Fatal(err)
	}
	next, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := Discover(context.Background(), root, "Example", "main")
	if err != nil || !reflect.DeepEqual(discovered, next.Plan) {
		t.Fatal("normal saved-plan guard would refuse amendment", err)
	}
	if !reflect.DeepEqual(next.Results, old.Results) || !bytes.Equal(next.Settings, old.Settings) || len(next.DependencyAmendments) != 1 || next.DependencyAmendments[0].Before != old.Plan.DefinitionSHA || next.DependencyAmendments[0].After != next.Plan.DefinitionSHA {
		t.Fatal("amendment changed existing jobs/settings or lacks audit")
	}
	for _, name := range []string{"01-a.md", "02-b.md"} {
		if !reflect.DeepEqual(ticketMap(old.Plan.Tickets)[name], ticketMap(next.Plan.Tickets)[name]) {
			t.Fatal("unrelated ticket changed")
		}
		data, _ := os.ReadFile(filepath.Join(next.Results[name].Directory, "journal.json"))
		if string(data) != `{"session":"offline-original","attempt":7,"rounds":2}` {
			t.Fatal("parent session/counters changed")
		}
	}
	driver := &combinedDriverFake{fakeDriver: fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("base")}}}}
	runner := Runner{Driver: driver}
	ticket := ticketMap(next.Plan.Tickets)["03-c.md"]
	target, wait, err := runner.target(context.Background(), next, ticket)
	if err != nil || wait || target.Base != "derived-base" || len(target.Dependencies) != 2 || target.Dependencies[0].RunID != old.Results["01-a.md"].RunID || target.Dependencies[1].HeadSHA != old.Results["02-b.md"].HeadSHA {
		t.Fatal("amended graph bypassed combined parent gate", target, wait, err)
	}
	parent := next.Results["01-a.md"]
	parent.State = "ci"
	next.Results["01-a.md"] = parent
	if _, wait, err := runner.target(context.Background(), next, ticket); err != nil || !wait || len(driver.vectors) != 1 {
		t.Fatal("third ticket did not wait for first readiness")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	preview, err = AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate)
	if err != nil || preview.Changed {
		t.Fatal("identical retry was not idempotent", err)
	}
	repeated, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(after, repeated) {
		t.Fatal("identical retry rewrote checkpoint")
	}
}
func TestDependencyAmendmentInterruptedWritesAreFencedAndRecoverable(t *testing.T) {
	for _, point := range []string{amendmentPending, "plan.json", "journal.json"} {
		t.Run(point, func(t *testing.T) {
			root, dir, old, validate := amendmentFixture(t)
			original := amendmentWrite
			failed := false
			amendmentWrite = func(path string, data []byte) error {
				err := original(path, data)
				if err == nil && filepath.Base(path) == point && !failed {
					failed = true
					return errors.New("offline interruption after durable write")
				}
				return err
			}
			t.Cleanup(func() { amendmentWrite = original })
			if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); err == nil {
				t.Fatal("interruption not observed")
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("partial transaction permitted feature publication")
			}
			if err := Save(dir, &old); err == nil {
				t.Fatal("partial transaction permitted coordinator checkpoint")
			}
			if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"02-b.md"}, false, validate); err == nil {
				t.Fatal("conflicting recovery command accepted")
			}
			amendmentWrite = original
			preview, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, true, validate)
			if err != nil || !preview.Recovery {
				t.Fatal("read-only recovery preview failed", err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("preview completed pending transaction")
			}
			if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); err != nil {
				t.Fatal("exact recovery failed", err)
			}
			next, err := Load(dir)
			if err != nil || !reflect.DeepEqual(next.Results, old.Results) || len(next.DependencyAmendments) != 1 {
				t.Fatal("recovery lost jobs/audit", err)
			}
		})
	}
}
func TestDependencyAmendmentRefusesStartedUnsafeChangedAndOwnedState(t *testing.T) {
	for _, scenario := range []string{"reserved", "files", "derived", "self", "duplicate", "missing", "unsafe", "cycle", "input", "definition", "symlink", "mode", "directory mode", "coordinator"} {
		t.Run(scenario, func(t *testing.T) {
			root, dir, state, validate := amendmentFixture(t)
			dependencies := []string{"01-a.md", "02-b.md"}
			child := "03-c.md"
			work := filepath.Dir(dir)
			switch scenario {
			case "reserved":
				state.Results[child] = Result{RunID: strings.Repeat("3", 24), State: "prepared"}
				Save(dir, &state)
			case "files":
				os.MkdirAll(filepath.Join(work, "runs", "03-c"), 0700)
			case "derived":
				os.MkdirAll(filepath.Join(dir, "derived", "offline-preparation"), 0700)
			case "self":
				dependencies = []string{child}
			case "duplicate":
				dependencies = []string{"01-a.md", "01-a.md"}
			case "missing":
				dependencies = []string{"04-unknown.md"}
			case "unsafe":
				child = "../03-c.md"
			case "cycle":
				parent := ticketMap(state.Plan.Tickets)["01-a.md"]
				parent.DependsOn = []string{child}
				for i := range state.Plan.Tickets {
					if state.Plan.Tickets[i].File == parent.File {
						state.Plan.Tickets[i] = parent
					}
				}
				validatePlan(&state.Plan)
				state.Plan.DefinitionSHA = definitionSHA(state.Plan)
				data, _ := completeAmendmentPlan(state.Plan)
				os.WriteFile(filepath.Join(work, "plan.json"), data, 0600)
				Save(dir, &state)
			case "input":
				os.WriteFile(filepath.Join(work, "tickets", child), []byte("changed offline input"), 0600)
			case "definition":
				os.WriteFile(filepath.Join(work, "plan.json"), []byte(`{"version":1,"tickets":{"03-c.md":{"priority":8}}}`), 0600)
			case "symlink":
				external := filepath.Join(t.TempDir(), "external")
				os.WriteFile(external, []byte("unchanged"), 0600)
				os.Symlink(external, filepath.Join(work, "plan.json"))
			case "mode":
				os.Chmod(filepath.Join(dir, "journal.json"), 0644)
			case "directory mode":
				os.Chmod(dir, 0777)
			case "coordinator":
				lock, err := filelock.AcquireExisting(filepath.Join(dir, "series.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			if _, err := AmendDependencies(context.Background(), root, "Example", child, dependencies, false, validate); err == nil {
				t.Fatal("unsafe amendment accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed checkpoint")
			}
			if _, err := os.Lstat(filepath.Join(dir, amendmentPending)); !os.IsNotExist(err) {
				t.Fatal("refusal began transaction")
			}
		})
	}
}
func TestDependencyAmendmentRecoveryRefusesConflictingCheckpointAndInputs(t *testing.T) {
	for _, scenario := range []string{"journal", "plan", "input", "transaction"} {
		t.Run(scenario, func(t *testing.T) {
			root, dir, _, validate := amendmentFixture(t)
			original := amendmentWrite
			amendmentWrite = func(path string, data []byte) error {
				err := original(path, data)
				if filepath.Base(path) == amendmentPending && err == nil {
					return errors.New("offline interruption")
				}
				return err
			}
			t.Cleanup(func() { amendmentWrite = original })
			AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate)
			amendmentWrite = original
			switch scenario {
			case "journal":
				data, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
				data = bytes.Replace(data, []byte("offline-pinned"), []byte("changed-pinned"), 1)
				os.WriteFile(filepath.Join(dir, "journal.json"), data, 0600)
			case "plan":
				os.WriteFile(filepath.Join(filepath.Dir(dir), "plan.json"), []byte(`{"version":1,"tickets":{}}`), 0600)
			case "input":
				os.WriteFile(filepath.Join(filepath.Dir(dir), "tickets", "03-c.md"), []byte("changed"), 0600)
			case "transaction":
				data, _ := os.ReadFile(filepath.Join(dir, amendmentPending))
				var tx amendmentTransaction
				json.Unmarshal(data, &tx)
				var state State
				json.Unmarshal(tx.NewJournal, &state)
				state.Results["01-a.md"] = Result{}
				tx.NewJournal, _ = json.Marshal(state)
				tx.NewJournalHash = amendmentHash(tx.NewJournal)
				data, _ = json.Marshal(tx)
				os.WriteFile(filepath.Join(dir, amendmentPending), data, 0600)
			}
			if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); err == nil {
				t.Fatal("conflicting recovery accepted")
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("conflict removed publication fence")
			}
		})
	}
}

func TestDirectTicketPreparationAndAmendmentOwnSameFeature(t *testing.T) {
	root, dir, _, validate := amendmentFixture(t)
	lease, err := OwnTicketPreparation(root, "Example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); !errors.Is(err, filelock.ErrBusy) {
		t.Fatal("direct preparation did not fence amendment", err)
	}
	lease.Close()
	original := amendmentWrite
	amendmentWrite = func(path string, data []byte) error {
		if filepath.Base(path) == amendmentPending {
			if lease, err := OwnTicketPreparation(root, "Example"); err == nil {
				lease.Close()
				t.Fatal("direct preparation entered locked amendment")
			}
		}
		return original(path, data)
	}
	t.Cleanup(func() { amendmentWrite = original })
	if _, err := AmendDependencies(context.Background(), root, "Example", "03-c.md", []string{"01-a.md", "02-b.md"}, false, validate); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}
