package runstatus

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitPersistsWithoutLosingLivenessOrTelemetry(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	tracker.Activity([]byte("sample output"))
	tracker.NativeEvent([]byte(`{"type":"turn.completed","usage":{"input_tokens":100}}` + "\n"))
	if err := tracker.Wait("codex", "provider_busy"); err != nil {
		t.Fatal(err)
	}
	v := onlyView(t, r, time.Now())
	if !v.Live || v.Stale || v.Activity.WaitingProvider != "codex" || v.Activity.WaitReason != "provider_busy" || v.Activity.WaitingSince.IsZero() {
		t.Fatalf("wait missing: %+v", v)
	}
	if v.Activity.OutputBytes != 13 || value(t, v.Activity.Usage.Aggregate.InputTokens) != 100 {
		t.Fatal("wait erased activity or usage")
	}
	since := v.Activity.WaitingSince
	if err := tracker.Wait("codex", "provider_busy"); err != nil {
		t.Fatal(err)
	}
	if !onlyView(t, r, time.Now()).Activity.WaitingSince.Equal(since) {
		t.Fatal("repeated wait reset duration")
	}
	if err := tracker.ClearWait("codex"); err != nil {
		t.Fatal(err)
	}
	v = onlyView(t, r, time.Now())
	if v.Activity.WaitingProvider != "" || v.Activity.WaitReason != "" || !v.Activity.WaitingSince.IsZero() || !v.Live {
		t.Fatal("acquired wait not cleared immediately")
	}
	if value(t, v.Activity.Usage.Aggregate.InputTokens) != 100 {
		t.Fatal("clear erased telemetry")
	}
	if err := tracker.ClearWait("codex"); err != nil {
		t.Fatal(err)
	}
}

func TestWaitValidationAndProviderOwnership(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	for _, params := range [][2]string{{"", "provider_busy"}, {"other", "provider_busy"}, {"codex", ""}, {"codex", "other"}, {"claude", "provider_busy\n"}} {
		if err := tracker.Wait(params[0], params[1]); err == nil {
			t.Fatalf("invalid wait accepted: %v", params)
		}
	}
	if err := tracker.Wait("claude", "runtime_busy"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClearWait("codex"); err == nil {
		t.Fatal("different provider cleared pending wait")
	}
	if err := tracker.ClearWait("other"); err == nil {
		t.Fatal("invalid clear provider accepted")
	}
	v := onlyView(t, r, time.Now())
	if v.Activity.WaitingProvider != "claude" || v.Activity.WaitReason != "runtime_busy" {
		t.Fatal("invalid clear changed pending wait")
	}
	if err := tracker.ClearWait("claude"); err != nil {
		t.Fatal(err)
	}
}

func TestStoppedControllerPreservesPendingWait(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Wait("codex", "runtime_busy"); err != nil {
		tracker.Close()
		t.Fatal(err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	v := onlyView(t, r, time.Now())
	if !v.Stopped || v.Live || v.Activity.WaitReason != "runtime_busy" || v.Activity.WaitingSince.IsZero() {
		t.Fatalf("stopped wait erased: %+v", v)
	}
	if err := tracker.Wait("codex", "provider_busy"); err == nil {
		t.Fatal("wait after close accepted")
	}
	if err := tracker.ClearWait("codex"); err == nil {
		t.Fatal("clear after close accepted")
	}
}

func TestWaitRejectsSymlinkSnapshotAndPropagatesPersistenceError(t *testing.T) {
	_, dir, j := fixture(t)
	tracker, err := Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	path := filepath.Join(dir, "activity.json")
	target := filepath.Join(dir, "untouched.json")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Wait("codex", "provider_busy"); err == nil {
		t.Fatal("symlink snapshot accepted")
	}
	var snapshot Snapshot
	if err := readJSON(target, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.WaitReason != "" {
		t.Fatal("symlink target was changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, path); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClearWait("codex"); err == nil {
		t.Fatal("previous persistence error not propagated")
	}
}
