package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/storage"
)

func storageCheckout(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("storage filesystem boundary checks unavailable")
	}
	root := t.TempDir()
	if data, err := exec.Command("git", "init", "--initial-branch=main", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture checkout: %v %s", err, data)
	}
	t.Chdir(root)
	return root
}
func TestStorageCommandReadOnlyJSONAndOlderFilter(t *testing.T) {
	root := storageCheckout(t)
	directory := filepath.Join(root, ".sdlc", "work", "example", "runs", "01-example", "0123456789abcdef01234567")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "journal.json")
	before := []byte(`{"id":"0123456789abcdef01234567","state":"waiting_for_human","updated_at":"2020-01-01T00:00:00Z","instructions":"excluded-private-notes"}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := storageCommand(context.Background(), []string{"status", "--older-than", "7", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var report storage.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.TotalRuns != 1 || len(report.Runs) != 1 || report.Runs[0].State != "waiting_for_human" || report.Runs[0].Retention != "resumable" || strings.Contains(output.String(), "excluded-private-notes") {
		t.Fatalf("unsafe or wrong storage projection: %s", output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("scan changed checkpoint")
	}
	output.Reset()
	if err := storageCommand(context.Background(), nil, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"logical bytes", "waiting_for_human", "resumable", "Last activity:", "Nothing deleted"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q", want)
		}
	}
}
func TestStorageCommandReportsPartialMeasurementsAndRejectsBadOptions(t *testing.T) {
	root := storageCheckout(t)
	directory := filepath.Join(root, ".sdlc", "work", "example", "runs", "01-example", "0123456789abcdef01234567")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "journal.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := storageCommand(context.Background(), nil, &output); err == nil || !strings.Contains(output.String(), "Partial measurement") || !strings.Contains(output.String(), "Scan needs attention") {
		t.Fatal("partial scan looked successful")
	}
	for _, args := range [][]string{{"purge"}, {"--older-than", "-1"}, {"--older-than", "36501"}, {"--older-than", "abc"}, {"--unknown"}} {
		if err := storageCommand(context.Background(), args, &output); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
	output.Reset()
	if err := storageCommand(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "Usage: sdlc storage") {
		t.Fatal("help unavailable")
	}
}
