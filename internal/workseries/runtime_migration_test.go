package workseries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/featureguard"
	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func runtimeTransactionFixture(t *testing.T) (string, *State, string, workrun.Journal, runtimeimage.State) {
	t.Helper()
	ticket := "01-one.md"
	dir, state := testState(t, Ticket{File: ticket}, Ticket{File: "02-two.md", DependsOn: []string{ticket}})
	canonical, err := filepath.EvalSymlinks(state.Plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	state.Plan.Root = canonical
	state.Plan.DefinitionSHA = definitionSHA(state.Plan)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "series.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 24)
	runDir, err := workrun.RunDirectory(state.Plan.Root, state.Plan.Reference, ticket, id, true)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(runDir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	oldImage := "sha256:" + strings.Repeat("a", 64)
	settings := map[string]any{"ImageID": oldImage, "runtime_name": "old", "Headroom": map[string]any{"mode": "external", "proxy_url": "http://localhost:9090"}}
	state.Settings, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	j := workrun.Journal{Version: 1, ID: id, Workspace: workspace, State: "blocked", ResumeState: "ci", StopReason: "CI unavailable", SessionID: "00000000-0000-4000-8000-000000000001", ImageID: oldImage, Plan: workrun.Plan{Root: state.Plan.Root, Reference: state.Plan.Reference, Ticket: ticket, StartingSHA: sha("base"), SourceSHA: sha("base"), Base: "main", BaseSHA: sha("base"), Branch: "ticket-branch", Repository: "example/project", RuntimeName: "old"}, Publication: workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: sha("base"), HeadSHA: sha("head")}, Evidence: workrun.CheckEvidence{Head: sha("candidate"), Tree: sha("tree"), Passed: true}, Outcome: workrun.Outcome{Status: "implemented"}}
	j.Plan.Roles = workrun.DefaultModels().Codex
	j.Plan.Checks = [][]string{{"go", "test", "./..."}}
	if err := workrun.Save(runDir, &j); err != nil {
		t.Fatal(err)
	}
	// The feature result is intentionally stale, as with an independently resumed run.
	state.Results[ticket] = Result{RunID: id, State: "blocked", Base: "main", BaseSHA: sha("base")}
	if err := Save(dir, state); err != nil {
		t.Fatal(err)
	}
	target := runtimeimage.State{ImageID: "sha256:" + strings.Repeat("b", 64)}
	return dir, state, runDir, j, target
}

func runtimeNoopVerify(context.Context, State, []workrun.Journal) error { return nil }

func TestFeatureRuntimeMigrationPreviewAndApplyPreservePRAndRoute(t *testing.T) {
	dir, state, runDir, original, target := runtimeTransactionFixture(t)
	featurePath := filepath.Join(dir, "journal.json")
	runPath := filepath.Join(runDir, "journal.json")
	oldFeature, _ := os.ReadFile(featurePath)
	oldRun, _ := os.ReadFile(runPath)
	preview, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, true, false, runtimeNoopVerify)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Runs) != 1 || preview.Runs[0].Publication != original.Publication || preview.Runs[0].ResumeState != "runtime_recheck" {
		t.Fatalf("wrong preview: %+v", preview)
	}
	afterFeature, _ := os.ReadFile(featurePath)
	afterRun, _ := os.ReadFile(runPath)
	if !bytes.Equal(oldFeature, afterFeature) || !bytes.Equal(oldRun, afterRun) {
		t.Fatal("preview changed saved state")
	}
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, runtimeNoopVerify); err != nil {
		t.Fatal(err)
	}
	next, err := workrun.Load(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if next.Publication != original.Publication || next.ImageID != target.ImageID || next.ResumeState != "runtime_recheck" || next.Evidence.Passed || next.Plan.Headroom != original.Plan.Headroom {
		t.Fatal("migration changed PR or provider route, or retained old checks")
	}
	feature, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		ImageID        string
		Headroom       map[string]string
		TicketHeadroom map[string]map[string]string
	}
	if err := json.Unmarshal(feature.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ImageID != target.ImageID || settings.Headroom["mode"] != "external" {
		t.Fatal("feature runtime or future-ticket route changed incorrectly")
	}
	if _, ok := settings.TicketHeadroom["01-one.md"]; !ok {
		t.Fatal("started ticket direct route not explicitly retained")
	}
	result := feature.Results["01-one.md"]
	if result.HeadSHA != original.Publication.HeadSHA || result.PRNumber != 1 || result.Branch != original.Plan.Branch {
		t.Fatal("stale feature result not refreshed from the journal")
	}
	archives, err := filepath.Glob(filepath.Join(dir, "runtime-migration-*-completed.json"))
	if err != nil || len(archives) != 1 {
		t.Fatal("audit archive missing", err)
	}
}

func TestFeatureRuntimeMigrationInterruptedCanFinishOrRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "rollback"}[rollback], func(t *testing.T) {
			dir, state, runDir, original, target := runtimeTransactionFixture(t)
			featurePath := filepath.Join(dir, "journal.json")
			runPath := filepath.Join(runDir, "journal.json")
			oldFeature, _ := os.ReadFile(featurePath)
			oldRun, _ := os.ReadFile(runPath)
			previous := runtimeMigrationWrite
			defer func() { runtimeMigrationWrite = previous }()
			runtimeMigrationWrite = func(path string, data []byte) error {
				if path == featurePath {
					return errors.New("injected feature write interruption")
				}
				return writeAmendmentFile(path, data)
			}
			if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, runtimeNoopVerify); err == nil {
				t.Fatal("interruption not observed")
			}
			if _, err := os.Stat(filepath.Join(dir, featureguard.RuntimeMigrationPending)); err != nil {
				t.Fatal("pending marker missing")
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("feature resumed during partial migration")
			}
			if _, err := workrun.Load(runDir); err == nil {
				t.Fatal("run resumed during partial migration")
			}
			runtimeMigrationWrite = previous
			if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, rollback, runtimeNoopVerify); err != nil {
				t.Fatal(err)
			}
			gotFeature, _ := os.ReadFile(featurePath)
			gotRun, _ := os.ReadFile(runPath)
			if rollback && (!bytes.Equal(oldFeature, gotFeature) || !bytes.Equal(oldRun, gotRun)) {
				t.Fatal("rollback did not restore exact checkpoint bytes")
			}
			j, err := workrun.Load(runDir)
			if err != nil {
				t.Fatal(err)
			}
			if j.Publication != original.Publication {
				t.Fatal("recovery changed the published PR")
			}
			if !rollback && (j.ImageID != target.ImageID || j.ResumeState != "runtime_recheck") {
				t.Fatal("forward recovery did not complete")
			}
			if _, err := os.Stat(filepath.Join(dir, featureguard.RuntimeMigrationPending)); !os.IsNotExist(err) {
				t.Fatal("pending fence not cleared")
			}
		})
	}
}

func TestFeatureRuntimeMigrationRefusesActiveRun(t *testing.T) {
	dir, state, runDir, _, target := runtimeTransactionFixture(t)
	lock, err := filelock.AcquireExisting(filepath.Join(runDir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, runtimeNoopVerify); err == nil {
		t.Fatal("active run admitted")
	}
	if _, err := os.Stat(filepath.Join(dir, featureguard.RuntimeMigrationPending)); !os.IsNotExist(err) {
		t.Fatal("marker created while run owned")
	}
}

func TestFeatureRuntimeMigrationRollsBackAfterFeatureWrite(t *testing.T) {
	dir, state, runDir, _, target := runtimeTransactionFixture(t)
	featurePath := filepath.Join(dir, "journal.json")
	runPath := filepath.Join(runDir, "journal.json")
	oldFeature, _ := os.ReadFile(featurePath)
	oldRun, _ := os.ReadFile(runPath)
	previous := runtimeMigrationWrite
	defer func() { runtimeMigrationWrite = previous }()
	runtimeMigrationWrite = func(path string, data []byte) error {
		if err := writeAmendmentFile(path, data); err != nil {
			return err
		}
		if path == featurePath {
			return errors.New("crash after feature write")
		}
		return nil
	}
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, runtimeNoopVerify); err == nil {
		t.Fatal("interruption ignored")
	}
	runtimeMigrationWrite = previous
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, true, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := os.ReadFile(featurePath)
	r, _ := os.ReadFile(runPath)
	if !bytes.Equal(f, oldFeature) || !bytes.Equal(r, oldRun) {
		t.Fatal("rollback did not restore exact original bytes")
	}
}

func TestFeatureRuntimeMigrationRetainsFenceIfFinalVerificationFails(t *testing.T) {
	dir, state, runDir, _, target := runtimeTransactionFixture(t)
	oldFeature, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	oldRun, _ := os.ReadFile(filepath.Join(runDir, "journal.json"))
	calls := 0
	verify := func(context.Context, State, []workrun.Journal) error {
		calls++
		if calls == 2 {
			return errors.New("PR moved")
		}
		return nil
	}
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, verify); err == nil {
		t.Fatal("changed PR accepted")
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("feature was unfenced after verification failure")
	}
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, true, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	r, _ := os.ReadFile(filepath.Join(runDir, "journal.json"))
	if !bytes.Equal(f, oldFeature) || !bytes.Equal(r, oldRun) {
		t.Fatal("failed validation did not roll back exactly")
	}
}

func TestFeatureRuntimeMigrationRollsBackEveryStartedTicket(t *testing.T) {
	dir, state, firstDir, first, target := runtimeTransactionFixture(t)
	secondTicket := "02-two.md"
	secondID := strings.Repeat("2", 24)
	secondDir, err := workrun.RunDirectory(state.Plan.Root, state.Plan.Reference, secondTicket, secondID, true)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(secondDir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = secondID
	second.Workspace = workspace
	second.Plan.Ticket = secondTicket
	second.Plan.Branch = "second-branch"
	second.Publication.Number = 2
	second.Publication.URL = "https://github.com/example/project/pull/2"
	second.Publication.HeadSHA = sha("head-two")
	second.Evidence.Head = sha("candidate-two")
	if err := workrun.Save(secondDir, &second); err != nil {
		t.Fatal(err)
	}
	state.Results[secondTicket] = Result{RunID: secondID, State: "blocked", Base: "main", BaseSHA: sha("base")}
	if err := Save(dir, state); err != nil {
		t.Fatal(err)
	}
	oldFeature, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	oldFirst, _ := os.ReadFile(filepath.Join(firstDir, "journal.json"))
	oldSecond, _ := os.ReadFile(filepath.Join(secondDir, "journal.json"))
	previous := runtimeMigrationWrite
	defer func() { runtimeMigrationWrite = previous }()
	runtimeMigrationWrite = func(path string, data []byte) error {
		if path == filepath.Join(secondDir, "journal.json") {
			return errors.New("crash before second run")
		}
		return writeAmendmentFile(path, data)
	}
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, false, runtimeNoopVerify); err == nil {
		t.Fatal("interruption not observed")
	}
	runtimeMigrationWrite = previous
	if _, err := MigrateFeatureRuntime(context.Background(), state.Plan.Root, state.Plan.Reference, target, false, true, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	a, _ := os.ReadFile(filepath.Join(firstDir, "journal.json"))
	b, _ := os.ReadFile(filepath.Join(secondDir, "journal.json"))
	if !bytes.Equal(f, oldFeature) || !bytes.Equal(a, oldFirst) || !bytes.Equal(b, oldSecond) {
		t.Fatal("rollback did not restore every run")
	}
}

func TestRuntimeMigrationRecoversAfterArchiveLinkBeforeFenceRemoval(t *testing.T) {
	dir := t.TempDir()
	tx := runtimeMigrationTransaction{ID: strings.Repeat("a", 24)}
	pending := filepath.Join(dir, featureguard.RuntimeMigrationPending)
	archive := filepath.Join(dir, "runtime-migration-"+tx.ID+"-completed.json")
	if err := os.WriteFile(pending, []byte("recorded transaction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pending, archive); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeArchiveMode(dir, tx, true); err == nil {
		t.Fatal("rollback admitted after forward archive link")
	}
	if err := verifyRuntimeArchiveMode(dir, tx, false); err != nil {
		t.Fatal(err)
	}
	if err := finishRuntimeTransaction(dir, tx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("pending fence remained", err)
	}
	if data, err := os.ReadFile(archive); err != nil || string(data) != "recorded transaction" {
		t.Fatal("archive lost", err)
	}
}
