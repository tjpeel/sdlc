package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runprogress"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/terminallaunch"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func recordProgress(t *testing.T, directory, id, source, provider, text string) {
	t.Helper()
	r, err := runprogress.Open(directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(runprogress.Event{RunID: id, Source: source, Provider: provider, Role: "implementation", Text: text}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProgressOptionsStayOffline(t *testing.T) {
	state := filepath.Join(t.TempDir(), "absent")
	t.Setenv("SDLC_STATE_DIR", state)
	for _, args := range [][]string{nil, {"--run", "abcdef", "--launch-id", "example"}, {"--run", "abcdef", "--once", "--follow"}, {"--run", "abcdef", "--scope", "other"}, {"--run", "abcdef", "--interval", "1ms"}, {"--help"}} {
		var out bytes.Buffer
		err := progressCommand(context.Background(), args, &out)
		if args != nil && args[0] == "--help" {
			if err != nil || !strings.Contains(out.String(), "--cursor") {
				t.Fatal(err, out.String())
			}
		} else if err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("option validation created private state")
	}
	if _, err := parseProgressOptions([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}

func TestProgressIncrementalLabelsQuestionsAndReadOnly(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	j := journals[1]
	directory := filepath.Dir(j.Workspace)
	recordProgress(t, directory, j.ID, "agent", "codex", "Inspecting the repository\nFound two cases")
	recordProgress(t, directory, j.ID, "checks", "", "2 checks passed")
	before := dashboardTree(t, root)
	o := progressOptions{run: j.ID, scope: "installation"}
	first, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o)
	if err != nil || !first.Done || first.Cursor == "" {
		t.Fatalf("batch %+v: %v", first, err)
	}
	var text strings.Builder
	for _, e := range first.Events {
		text.WriteString(runprogress.Format(e))
	}
	for _, want := range []string{"agent:codex implementation", "checks]", "sdlc]", "Question: Should missing items", "sdlc answer --run " + j.ID} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing %q: %s", want, text.String())
		}
	}
	o.cursor = first.Cursor
	second, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o)
	if err != nil || len(second.Events) != 0 || !second.Done {
		t.Fatalf("unchanged output repeated: %+v %v", second, err)
	}
	assertDashboardReadOnly(t, root, marker, before)
	recordProgress(t, directory, j.ID, "agent", "claude", "Review completed")
	third, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o)
	if err != nil || len(third.Events) != 1 || third.Events[0].Text != "Review completed" || third.Events[0].Provider != "claude" {
		t.Fatalf("append: %+v %v", third, err)
	}
	o.run = journals[0].ID
	if _, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o); err == nil {
		t.Fatal("cursor accepted for a different run")
	}
}

func TestProgressLegacyLiveAppendAndStructuredCLI(t *testing.T) {
	_, _, journals := dashboardFixture(t)
	j := journals[0]
	path := filepath.Join(filepath.Dir(j.Workspace), "events-1.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"item.completed","item":{"type":"agent_message","text":"First message"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := progressCommand(context.Background(), []string{"--run", j.ID, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var first runprogress.Batch
	if err := json.Unmarshal(output.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Done || len(first.Events) != 2 || first.Events[0].Text != "First message" {
		t.Fatalf("%+v", first)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"item.completed","item":{"type":"agent_message","text":"Second message"}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := progressCommand(context.Background(), []string{"--run", j.ID, "--once", "--json", "--cursor", first.Cursor}, &output); err != nil {
		t.Fatal(err)
	}
	var second runprogress.Batch
	if err := json.Unmarshal(output.Bytes(), &second); err != nil || len(second.Events) != 1 || second.Events[0].Text != "Second message" {
		t.Fatal(err, output.String())
	}
}

func TestProgressLaunchWaitsForClaimAndTracksAllRuns(t *testing.T) {
	root, _, journals := dashboardFixture(t)
	store, err := launchStore(os.Getenv("SDLC_STATE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := terminallaunch.NewID()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	request := terminallaunch.Request{ID: id, Root: root, Executable: executable, Args: []string{"run", "--reference", "example"}}
	if _, err := store.Launch(context.Background(), request, progressBackend{}); err != nil {
		t.Fatal(err)
	}
	o := progressOptions{launch: id, scope: "installation"}
	first, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o)
	if err != nil || first.Done || len(first.Events) != 1 || !strings.Contains(first.Events[0].Text, "dispatched") {
		t.Fatalf("launch batch %+v %v", first, err)
	}
	// A stopped run from before a resume cannot terminate the new launch feed.
	if _, err := store.Consume(id); err != nil {
		t.Fatal(err)
	}
	for _, j := range journals {
		if err := store.RecordRun(id, terminallaunch.RunIdentity{RunIDs: []string{j.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	o.cursor = first.Cursor
	second, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", o)
	if err != nil || second.Done {
		t.Fatalf("%+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, e := range second.Events {
		seen[e.RunID] = true
	}
	if !seen[journals[0].ID] || !seen[journals[1].ID] {
		t.Fatal("multi-ticket launch lost producer identity")
	}
}

type progressBackend struct{}

func (progressBackend) Launch(context.Context, string) error { return nil }

func TestProgressFollowCancellationLeavesPrivateStateUntouched(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	before := dashboardTree(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	if err := progressCommand(ctx, []string{"--run", journals[0].ID, "--follow", "--interval", "100ms"}, &output); err != nil {
		t.Fatal(err)
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestShellProgressUsesCLIAndResumedProjectReceipt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other-project")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "capture")
	t.Setenv("SDLC_PROGRESS_CAPTURE", capture)
	executable := filepath.Join(root, "fake-sdlc")
	script := "#!/bin/sh\npwd > \"$SDLC_PROGRESS_CAPTURE\"\nprintf '%s\\n' \"$@\" >> \"$SDLC_PROGRESS_CAPTURE\"\nprintf '%s\\n' '{\"cursor\":\"next\",\"events\":[],\"done\":false}'\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := &shellAdapter{executable: executable, launchID: "example-receipt", launchRoot: other}
	batch, err := a.progress(context.Background(), root, nil, "previous")
	if err != nil || batch.Cursor != "next" {
		t.Fatal(err, batch)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := other + "\nprogress\n--once\n--json\n--launch-id\nexample-receipt\n--cursor\nprevious\n"
	if string(got) != want {
		t.Fatalf("wrong CLI receipt request: %q", got)
	}
	if _, err := a.progress(context.Background(), root, []string{"dashboard", "--run", "abcdef123456", "--scope", "all"}, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(capture)
	if !strings.HasPrefix(string(got), root+"\n") || !strings.Contains(string(got), "--scope\ninstallation\n") || strings.Contains(string(got), "--launch-id") {
		t.Fatalf("explicit run did not use CLI selection: %q", got)
	}
	for _, args := range [][]string{{"progress", "--run", "abcdef123456", "--unknown"}, {"dashboard", "--run", "abcdef123456", "--logs", "--unknown"}} {
		if _, err := a.progress(context.Background(), root, args, ""); err == nil {
			t.Fatal("shell bypassed CLI validation", args)
		}
	}
}

func TestProgressFeatureBatchesFitShellAndDrainWithoutRepeats(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	state := os.Getenv("SDLC_STATE_DIR")
	store, err := launchStore(state)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := terminallaunch.NewID()
	executable, _ := filepath.EvalSymlinks(os.Args[0])
	if _, err := store.Launch(context.Background(), terminallaunch.Request{ID: id, Root: root, Executable: executable, Args: []string{"run", "--all"}}, progressBackend{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		j := journals[1]
		j.ID = fmt.Sprintf("%024x", i+100)
		directory := filepath.Join(root, "feature-runs", j.ID)
		j.Workspace = filepath.Join(directory, "workspace")
		if err := os.MkdirAll(j.Workspace, 0700); err != nil {
			t.Fatal(err)
		}
		if err := workrun.Save(directory, &j); err != nil {
			t.Fatal(err)
		}
		if err := runstatus.New(state).Register(directory, j); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 12; n++ {
			recordProgress(t, directory, j.ID, "agent", "codex", fmt.Sprintf("message-%02d: ", n)+strings.Repeat("<", 1300))
		}
		if err := store.RecordRun(id, terminallaunch.RunIdentity{RunIDs: []string{j.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Complete(id, nil); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, root)
	seen := map[string]bool{}
	counts := map[string]int{}
	o := progressOptions{launch: id, scope: "installation"}
	finished := false
	for i := 0; i < 20; i++ {
		batch, err := progressSnapshot(context.Background(), state, "", o)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(batch)
		if len(data) > 256*1024 {
			t.Fatalf("batch exceeds shell decoder: %d bytes", len(data))
		}
		for _, event := range batch.Events {
			if event.Source != "agent" {
				continue
			}
			key := event.RunID + "/" + event.Text[:11]
			if seen[key] {
				t.Fatal("duplicate output", key)
			}
			seen[key] = true
			counts[event.RunID]++
		}
		o.cursor = batch.Cursor
		if batch.Done {
			finished = true
			break
		}
	}
	if !finished || len(seen) < 8*6 {
		t.Fatalf("failed to drain every run's recent tail: finished=%v messages=%d", finished, len(seen))
	}
	if len(counts) != 8 {
		t.Fatalf("lost a whole run: %v", counts)
	}
	for run, count := range counts {
		if count < 6 {
			t.Fatalf("run %s lost recent records: %d", run, count)
		}
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestProgressCursorAcceptsSupportedLargeFeature(t *testing.T) {
	c := progressCursor{Version: 1, Selector: "selection", States: map[string]string{"launch": strings.Repeat("a", 64)}, Offsets: map[string]int64{}}
	for i := 0; i < 1024; i++ {
		id := fmt.Sprintf("%024x", i)
		c.States[id] = strings.Repeat("b", 64)
		c.Offsets[id] = int64(i + 1)
	}
	data, _ := json.Marshal(c)
	encoded := base64.RawURLEncoding.EncodeToString(data)
	got, err := decodeProgressCursor(encoded, "selection")
	if err != nil || len(got.States) != 1025 || len(got.Offsets) != 1024 {
		t.Fatalf("supported feature rejected its own cursor: states=%d offsets=%d %v", len(got.States), len(got.Offsets), err)
	}
}

func TestProgressStoppedLegacyCheckShowsFinalFragment(t *testing.T) {
	_, _, journals := dashboardFixture(t)
	j := journals[1]
	directory := filepath.Dir(j.Workspace)
	j.State, j.ResumeState, j.CheckAttempt = "failed", "checking", 1
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "checks-1.log"), []byte("final verification failure"), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := progressSnapshot(context.Background(), os.Getenv("SDLC_STATE_DIR"), "", progressOptions{run: j.ID, scope: "installation"})
	if err != nil || !batch.Done || len(batch.Events) == 0 || batch.Events[0].Source != "checks" || batch.Events[0].Text != "final verification failure" {
		t.Fatal(err, batch)
	}
}
