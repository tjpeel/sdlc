package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func dashboardFixture(t *testing.T) (string, string, []workrun.Journal) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "installation")
	t.Setenv("SDLC_STATE_DIR", state)
	marker := forbidConnectedRunCommands(t, root)
	registry := runstatus.New(state)
	var journals []workrun.Journal
	for index, status := range []string{"implementing", "waiting_for_human"} {
		id := fmt.Sprintf("0123456789abcdef0123456%d", index)
		repository := filepath.Join(root, fmt.Sprintf("example-repository-%d", index))
		directory := filepath.Join(repository, ".sdlc", "work", "example", "runs", "01-example", id)
		if err := os.MkdirAll(filepath.Join(directory, "workspace"), 0700); err != nil {
			t.Fatal(err)
		}
		journal := workrun.Journal{Version: 1, ID: id, State: status, Workspace: filepath.Join(directory, "workspace"),
			StartedAt: time.Now().UTC().Add(-time.Hour), Attempt: 1, Instructions: "private-implementation-instructions",
			Plan:     workrun.Plan{Root: repository, Reference: "example", Ticket: ".sdlc/work/example/tickets/01-example.md", StartingSHA: strings.Repeat("a", 40), SourceSHA: strings.Repeat("b", 40), Roles: workrun.DefaultModels().Codex},
			Evidence: workrun.CheckEvidence{Tree: strings.Repeat("c", 40), Passed: true}, CI: workrun.CIResult{Status: "passed", Details: "unit checks"},
			Publication: workrun.Publication{URL: "https://github.com/example/project/pull/1", Number: 1},
		}
		if status == "waiting_for_human" {
			journal.PendingRole = "review"
			journal.Outcome.Questions = []string{"Should missing items return 404?"}
			journal.StopReason = "Answer the recorded question."
		}
		if err := workrun.Save(directory, &journal); err != nil {
			t.Fatal(err)
		}
		if err := registry.Register(directory, journal); err != nil {
			t.Fatal(err)
		}
		activity := runstatus.Snapshot{Version: 1, ID: id, ControllerID: strings.Repeat("d", 24), HeartbeatAt: time.Now().UTC(), LastActivityAt: time.Now().UTC().Add(-30 * time.Minute), Stopped: index == 1}
		data, err := json.Marshal(activity)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "activity.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "events-1.jsonl"), []byte("private-recent-event\n"), 0600); err != nil {
			t.Fatal(err)
		}
		journals = append(journals, journal)
	}
	return root, marker, journals
}

type dashboardFileState struct {
	Mode     fs.FileMode
	Modified time.Time
	Hash     [32]byte
}

func dashboardTree(t *testing.T, root string) map[string]dashboardFileState {
	t.Helper()
	state := make(map[string]dashboardFileState)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := dashboardFileState{Mode: info.Mode(), Modified: info.ModTime()}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Hash = sha256.Sum256(data)
		}
		state[path] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertDashboardReadOnly(t *testing.T, root, marker string, before map[string]dashboardFileState) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dashboard invoked a connected command: %v", err)
	}
	if after := dashboardTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("dashboard changed private state, logs or installation files")
	}
}

func TestDashboardHelpAndOptionsDoNotInitializeRuntime(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "absent-installation")
	t.Setenv("SDLC_STATE_DIR", state)
	marker := forbidConnectedRunCommands(t, root)
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sdlc dashboard") || !strings.Contains(output.String(), "--run") {
		t.Fatalf("help missing: %s", output.String())
	}
	if _, err := parseDashboardOptions([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help parse: %v", err)
	}
	for _, args := range [][]string{{"extra"}, {"--watch", "--once"}, {"--watch", "--json"}, {"--logs"}, {"--logs", "--run", "123456", "--json"}, {"--run", "12345"}, {"--run", "ABCDEF"}, {"--interval", "1ms"}, {"--interval", "2m"}, {"--unknown"}, {"--dry-run"}} {
		if err := dashboardCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
	for _, args := range [][]string{{}, {"--once"}, {"--json"}, {"--watch", "--interval", "250ms"}, {"--run", "123456", "--logs"}} {
		if _, err := parseDashboardOptions(args); err != nil {
			t.Fatalf("valid options rejected: %v: %v", args, err)
		}
	}
	if err := dashboardCommand(context.Background(), []string{"--once"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No registered runs") {
		t.Fatal("missing installation not rendered as an empty dashboard")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("read-only dashboard initialized runtime: %v", err)
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardJSONIncludesAllRepositoriesWithoutPrivateJournal(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int              `json:"version"`
		Runs    []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || len(document.Runs) != 2 {
		t.Fatalf("all-repository JSON: %s", output.String())
	}
	if document.Runs[0]["id"] != journals[1].ID || document.Runs[0]["state"] != "waiting_for_human" {
		t.Fatal("JSON did not prioritize the pending question")
	}
	if document.Runs[1]["live"] != true || document.Runs[1]["stale"] != false {
		t.Fatal("quiet live controller classified incorrectly")
	}
	for _, row := range document.Runs {
		for _, key := range []string{"journal", "instructions", "plan", "feedback", "questions"} {
			if _, exists := row[key]; exists {
				t.Fatalf("private journal field %q included", key)
			}
		}
	}
	if strings.Contains(output.String(), journals[0].Instructions) || strings.Contains(output.String(), "private-recent-event") {
		t.Fatal("JSON copied private prompts or log output")
	}
	if document.Runs[0]["root"] == document.Runs[1]["root"] {
		t.Fatal("distinct repositories collapsed")
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardJSONExposesQueueReason(t *testing.T) {
	_, _, journals := dashboardFixture(t)
	j := journals[0]
	path := filepath.Join(filepath.Dir(j.Workspace), "activity.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var activity runstatus.Snapshot
	if err := json.Unmarshal(data, &activity); err != nil {
		t.Fatal(err)
	}
	activity.WaitingProvider, activity.WaitReason, activity.WaitingSince = "codex", "provider_busy", time.Now().UTC()
	data, _ = json.Marshal(activity)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--json", "--run", j.ID}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Runs []struct {
			Live            bool      `json:"live"`
			WaitReason      string    `json:"wait_reason"`
			WaitingProvider string    `json:"waiting_provider"`
			WaitingSince    time.Time `json:"waiting_since"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 || !result.Runs[0].Live || result.Runs[0].WaitReason != "provider_busy" || result.Runs[0].WaitingProvider != "codex" || result.Runs[0].WaitingSince.IsZero() {
		t.Fatal(output.String())
	}
}

func TestDashboardSelectedRunShowsQuestionsPRChecksAndBoundedLogs(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--run", journals[1].ID, "--logs", "--once"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{journals[1].ID, "Should missing items return 404?", "https://github.com/example/project/pull/1", "CI: passed unit checks", "Local checks: passed", "private-recent-event", "--answer-file", "--resume"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("detail omitted %q: %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), journals[0].ID) || strings.Contains(output.String(), journals[0].Instructions) {
		t.Fatal("detail included another run or implementation instructions")
	}
	output.Reset()
	if err := dashboardCommand(context.Background(), []string{"--run", "012345", "--once"}, &output); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix selection: %v", err)
	}
	output.Reset()
	if err := dashboardCommand(context.Background(), []string{"--json", "--run", journals[1].ID}, &output); err != nil {
		t.Fatal(err)
	}
	var selected struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &selected); err != nil || len(selected.Runs) != 1 || selected.Runs[0]["id"] != journals[1].ID {
		t.Fatalf("selected JSON: %s %v", output.String(), err)
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardRetainsCorruptEntriesBesideHealthyRuns(t *testing.T) {
	root, marker, _ := dashboardFixture(t)
	entry := filepath.Join(root, "installation", "runs", "abcdefabcdefabcdefabcdef.json")
	if err := os.WriteFile(entry, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil || len(document.Runs) != 3 {
		t.Fatalf("corrupt entry discarded: %s %v", output.String(), err)
	}
	var unavailable bool
	for _, row := range document.Runs {
		if row["id"] == "abcdefabcdefabcdefabcdef" {
			unavailable = row["available"] == false && row["needs_attention"] == true && row["error"] != ""
		}
	}
	if !unavailable {
		t.Fatal("corrupt entry not marked unavailable and needing attention")
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardWatchCancelsWithoutControllingRunsOrMutatingFiles(t *testing.T) {
	root, marker, _ := dashboardFixture(t)
	before := dashboardTree(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	if err := dashboardCommand(ctx, []string{"--watch", "--interval", "250ms"}, &output); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("watch did not promptly honor cancellation")
	}
	if strings.Count(output.String(), "SDLC  ") < 2 {
		t.Fatalf("forced watch did not refresh redirected output: %s", output.String())
	}
	if strings.Contains(output.String(), "\x1b") {
		t.Fatal("redirected watch emitted terminal cursor controls")
	}
	assertDashboardReadOnly(t, root, marker, before)
}
