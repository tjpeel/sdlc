package workrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func TestRuntimeMigrationPreservesRunAndCreatesSessionBoundary(t *testing.T) {
	oldImage := "sha256:" + strings.Repeat("a", 64)
	newImage := "sha256:" + strings.Repeat("b", 64)
	j := Journal{ID: "a1b2c3", State: "blocked", ResumeState: "implementing", ImageID: oldImage, SessionID: "old-session", Attempt: 3, Plan: Plan{RuntimeName: "old"}}
	next, err := PrepareRuntimeMigration(j, "new", runtimeimage.State{Name: "new", ImageID: newImage})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != j.ID || next.Attempt != j.Attempt || next.ImageID != newImage || next.Plan.RuntimeName != "new" || next.SessionID != "" {
		t.Fatalf("migration changed wrong fields: %+v", next)
	}
	if len(next.RuntimeMigrations) != 1 || next.RuntimeMigrations[0].PreviousSessionID != "old-session" || next.RuntimeMigrations[0].PreviousImageID != oldImage {
		t.Fatal("migration lineage missing")
	}
	if j.SessionID != "old-session" || j.ImageID != oldImage {
		t.Fatal("original checkpoint changed")
	}
}

func TestRuntimeMigrationRejectsPublishedOrExtendedRuns(t *testing.T) {
	image := "sha256:" + strings.Repeat("b", 64)
	j := Journal{State: "blocked", ResumeState: "implementing", ImageID: "sha256:" + strings.Repeat("a", 64), SessionID: "old-session"}
	j.Publication.Number = 1
	if _, err := PrepareRuntimeMigration(j, "new", runtimeimage.State{Name: "new", ImageID: image}); err == nil {
		t.Fatal("published run accepted")
	}
	j.Publication.Number = 0
	j.SessionTurnExtension = &SessionTurnExtension{}
	if _, err := PrepareRuntimeMigration(j, "new", runtimeimage.State{Name: "new", ImageID: image}); err == nil {
		t.Fatal("session grant accepted")
	}
}

func TestRuntimeMigrationPreservesWorkspaceAndRecoversAfterFailedSave(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	if err := os.Mkdir(filepath.Join(dir, "native-implementation"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native-implementation", "session"), []byte("prior session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Workspace, "new-file"), []byte("uncommitted work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	original, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := PrepareRuntimeMigration(original, "new", runtimeimage.State{Name: "new", ImageID: "sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	invalid := next
	invalid.RuntimeMigrations = append([]RuntimeMigration(nil), next.RuntimeMigrations...)
	invalid.RuntimeMigrations[0].SessionDirectory = "../../escape"
	if err := SaveRuntimeMigration(dir, original, &invalid); err == nil {
		t.Fatal("invalid migration saved")
	}
	afterFailure, err := Load(dir)
	if err != nil || afterFailure.ImageID != original.ImageID || afterFailure.SessionID != original.SessionID {
		t.Fatal("failed migration changed checkpoint")
	}
	if err := SaveRuntimeMigration(dir, original, &next); err != nil {
		t.Fatal(err)
	}
	if backup, err := os.ReadFile(filepath.Join(dir, "runtime-migration-1.before.json")); err != nil || !bytes.Equal(backup, originalBytes) {
		t.Fatal("original checkpoint bytes not preserved")
	}
	saved, err := Load(dir)
	if err != nil || saved.ImageID != next.ImageID || saved.SessionID != "" {
		t.Fatalf("migration not retained: %v", err)
	}
	for path, expected := range map[string]string{filepath.Join(dir, "native-implementation", "session"): "prior session", filepath.Join(j.Workspace, "new-file"): "uncommitted work"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatalf("retained artifact changed: %v", err)
		}
	}
}

func TestFormatterMigrationRequiresCorroborationAndRejectsProviderHolds(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", testNative
	j.StopReason = "Restore .NET SDK in the formatter worker"
	j.Outcome.Status = "blocked"
	if err := os.WriteFile(filepath.Join(j.Workspace, "global.json"), []byte(`{"sdk":{"version":"10.0.403"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := runtimeimage.State{Inventory: &runtimeimage.Inventory{Dependencies: []runtimeimage.Dependency{{Kind: "dotnet-sdk", Version: "10.0.403"}}}}
	if err := VerifyFormatterSDKHold(dir, *j, target); err == nil {
		t.Fatal("missing diagnostic accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "precommit-format-1.log"), []byte("A compatible .NET SDK was not found.\nRequested SDK version: 10.0.403\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFormatterSDKHold(dir, *j, target); err != nil {
		t.Fatal(err)
	}
	wrong := runtimeimage.State{Inventory: &runtimeimage.Inventory{Dependencies: []runtimeimage.Dependency{{Kind: "dotnet-sdk", Version: "10.0.401"}}}}
	if err := VerifyFormatterSDKHold(dir, *j, wrong); err == nil {
		t.Fatal("wrong target SDK accepted")
	}
	j.Outcome.Limitations = []string{"Provider usage limit reached"}
	if err := VerifyFormatterSDKHold(dir, *j, target); err == nil {
		t.Fatal("provider limit bypassed")
	}
}

func TestRuntimeMigrationRetainsPreviousImageReference(t *testing.T) {
	j := Journal{ImageID: "new", RuntimeMigrations: []RuntimeMigration{{PreviousImageID: "old"}}}
	if !ReferencesRuntimeImage(j, "old") || !ReferencesRuntimeImage(j, "new") || ReferencesRuntimeImage(j, "other") {
		t.Fatal("runtime lineage reference incorrect")
	}
}

func TestMigratedRunStartsFreshNativeSessionWithPriorAttemptsRetained(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.Attempt = 3
	j.StopReason = "Restore .NET SDK in the formatter worker"
	next, err := PrepareRuntimeMigration(*j, "new", runtimeimage.State{Name: "new", ImageID: "sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("blocked")}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if _, err := runner.session(context.Background(), dir, &next, "implementation", next.Workspace, next.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.calls[0].ResumeID != "" || provider.calls[0].Directory != filepath.Join(dir, "native-implementation-migrated-1") {
		t.Fatal("migration did not start its fresh native session")
	}
	if next.Attempt != 4 || !strings.Contains(provider.calls[0].Prompt, "formatter SDK hold") {
		t.Fatal("migration lost attempt accounting or recovery context")
	}
}

func TestInterruptedMigratedSessionDoesNotStartAnotherSession(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.Attempt = 1
	next, err := PrepareRuntimeMigration(*j, "new", runtimeimage.State{Name: "new", ImageID: "sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	next.Attempt++ // dispatch was checkpointed but native identity was not recorded
	if err := Save(dir, &next); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("blocked")}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := runner.Run(context.Background(), dir, &next, ""); err == nil {
		t.Fatal("interrupted session was restarted")
	}
	if len(provider.calls) != 0 {
		t.Fatal("another native session was started")
	}
}

func TestMigratedRunContinuesThroughChecksAndIndependentReview(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.Attempt = 1
	j.StopReason = "Restore .NET SDK in the formatter worker"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	original, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	next, err := PrepareRuntimeMigration(original, "new", runtimeimage.State{Name: "new", ImageID: "sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRuntimeMigration(dir, original, &next); err != nil {
		t.Fatal(err)
	}
	newSession := "00000000-0000-4000-8000-000000000002"
	provider := &fakeProvider{resultSessionID: newSession, outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &fakeChecker{}
	runner := fakeRunner(provider, checker, &fakePublisher{}, &fakeRepository{})
	if err := runner.Run(context.Background(), dir, &next, ""); err != nil {
		t.Fatal(err)
	}
	if next.State != "ready" || next.ID != original.ID || checker.calls != 1 || len(provider.calls) != 3 {
		t.Fatal("migrated run did not complete its required checks and review")
	}
	if provider.calls[0].ResumeID != "" || provider.calls[1].ResumeID != newSession || provider.calls[2].ResumeID != "" {
		t.Fatal("migrated implementation or review session lineage changed")
	}
	if next.RuntimeMigrations[0].PreviousSessionID != testNative || next.SessionID != newSession {
		t.Fatal("old or new session identity lost")
	}
}
