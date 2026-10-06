//go:build linux || darwin

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, root, path, body string, modified time.Time) int64 {
	t.Helper()
	path = filepath.Join(root, ".sdlc", path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return int64(len(body))
}
func fixtureRun(t *testing.T, root, id, state string, activity time.Time) string {
	t.Helper()
	path := filepath.Join("work", "example", "runs", "01-example", id)
	data, err := json.Marshal(map[string]any{"id": id, "state": state, "updated_at": activity, "instructions": "private instructions never projected"})
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, filepath.Join(path, "journal.json"), string(data), activity)
	return path
}
func TestScanCountsLogicalFilesAndSkipsSymlinksGitAndAuthentication(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	old := now.Add(-7 * 24 * time.Hour)
	path := fixtureRun(t, root, "0123456789abcdef01234567", "ready", old)
	put(t, root, filepath.Join(path, "workspace", "source.txt"), "source", old)
	put(t, root, filepath.Join(path, "native-implementation", "codex", "sessions", "example.jsonl"), "private session", old)
	put(t, root, filepath.Join(path, "logs", "output.log"), "private log", old)
	put(t, root, filepath.Join(path, "inputs", "ticket.md"), "private requirements", old)
	put(t, root, filepath.Join(path, "workspace", ".git", "private.json"), "not counted", old)
	put(t, root, filepath.Join(path, "native-implementation", "codex", "auth.json"), "not counted", old)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("not counted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".sdlc", path, "linked.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, ".sdlc", path, "workspace", "source.txt"), filepath.Join(root, ".sdlc", path, "workspace", "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || report.Totals.Files != 6 || len(report.Runs) != 1 || len(report.Skipped) != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}
	var total int64
	for _, file := range []string{"journal.json", "workspace/source.txt", "workspace/hardlink.txt", "native-implementation/codex/sessions/example.jsonl", "logs/output.log", "inputs/ticket.md"} {
		info, err := os.Stat(filepath.Join(root, ".sdlc", path, file))
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	run := report.Runs[0]
	if report.Totals.Bytes != total || run.Bytes != total || run.Retention != "completed; awaiting review" || run.AgeDays != 7 {
		t.Fatalf("bad totals or age: %+v", report)
	}
	encoded, _ := json.Marshal(report)
	for _, private := range []string{"private instructions", "private session", "private requirements", "private log", "not counted"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("artifact contents projected")
		}
	}
	if len(run.Categories) != 5 {
		t.Fatalf("categories not separated: %+v", run.Categories)
	}
}
func TestScanAgeFilterUsesNewestArtifactAndHeartbeatAndPreservesTotals(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	older := fixtureRun(t, root, "000000000000000000000001", "waiting_for_human", cutoff.Add(-time.Second))
	fixtureRun(t, root, "000000000000000000000002", "ready", cutoff)
	recent := fixtureRun(t, root, "000000000000000000000003", "implementing", cutoff.Add(-time.Hour))
	put(t, root, filepath.Join(recent, "activity.json"), `{"id":"000000000000000000000003","stopped":false,"heartbeat_at":"2026-01-19T23:59:55Z"}`, cutoff.Add(-time.Hour))
	artifact := fixtureRun(t, root, "000000000000000000000004", "blocked", cutoff.Add(-time.Hour))
	put(t, root, filepath.Join(artifact, "workspace", "fresh.txt"), "fresh", now)
	days := 7
	report, err := Scan(context.Background(), root, Options{Now: now, OlderThanDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalRuns != 4 || len(report.Runs) != 1 || report.Runs[0].Path != older || report.Runs[0].Retention != "resumable" {
		t.Fatalf("age boundary wrong: %+v", report)
	}
	all, err := Scan(context.Background(), root, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals != report.Totals || report.MatchingRuns.Bytes >= report.Totals.Bytes {
		t.Fatal("filter changed full totals")
	}
	for _, run := range all.Runs {
		if run.Path == recent && run.Controller != "recent heartbeat" {
			t.Fatal("active heartbeat not visible")
		}
	}
}
func TestScanReportsCorruptMetadataWithoutLeakingContents(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := fixtureRun(t, root, "000000000000000000000001", "blocked", now)
	put(t, root, filepath.Join(path, "journal.json"), `{"updated_at":"private material"}`, now)
	report, err := Scan(context.Background(), root, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || len(report.Errors) != 1 || report.Runs[0].State != "unknown" {
		t.Fatalf("invalid checkpoint hidden: %+v", report)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "private material") {
		t.Fatal("invalid private field leaked")
	}
}
func TestScanMissingDirectorySymlinkAndCancellation(t *testing.T) {
	root := t.TempDir()
	report, err := Scan(context.Background(), root, Options{})
	if err != nil || report.Exists || report.Totals.Bytes != 0 {
		t.Fatal("missing directory not empty")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".sdlc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), root, Options{}); err == nil {
		t.Fatal("linked storage root followed")
	}
	root = t.TempDir()
	put(t, root, "config.json", "{}", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, root, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not returned")
	}
	days := 36501
	if _, err := Scan(context.Background(), root, Options{OlderThanDays: &days}); err == nil {
		t.Fatal("overflowing age accepted")
	}
}

func TestScanDoesNotEnterAnotherFilesystemDevice(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	put(t, root, "mount/should-not-enter/journal.json", "invalid", now)
	put(t, root, "config.json", "{}", now)
	report, err := scan(context.Background(), root, Options{Now: now}, func(info os.FileInfo) (uint64, error) {
		if info.Name() == "mount" {
			return 2, nil
		}
		return 1, nil
	})
	if err != nil || !report.Complete || report.Totals.Files != 1 || len(report.Skipped) != 1 || report.Skipped[0].Reason != "different filesystem device" {
		t.Fatalf("device boundary not respected: %+v err=%v", report, err)
	}
}
