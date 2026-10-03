package runstatus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func value(t *testing.T, n *int64) int64 {
	t.Helper()
	if n == nil {
		t.Fatal("unknown counter")
	}
	return *n
}

func TestCodexUsageIsLatestSessionAggregate(t *testing.T) {
	var o usageObserver
	var u Usage
	data := []byte("plain check log\n" + `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":15}}` + "\n")
	for _, b := range data {
		o.feed([]byte{b}, "codex", "example-model", &u)
	}
	if u.Aggregate == nil || u.Aggregate.Scope != "session_total" || value(t, u.Aggregate.InputTokens) != 100 || u.Context != nil || u.ModelMatches {
		t.Fatalf("bad aggregate: %+v", u)
	}
	o.feed([]byte(`{"type":"turn.completed","usage":{"input_tokens":150,"cached_input_tokens":100,"output_tokens":20}}`+"\n"), "codex", "example-model", &u)
	if value(t, u.Aggregate.InputTokens) != 150 || value(t, u.Aggregate.OutputTokens) != 20 {
		t.Fatal("aggregate added across resumes")
	}
	o.feed([]byte(`{"type":"turn.completed","usage":{"input_tokens":150,"cached_input_tokens":100,"output_tokens":20}}`+"\n"), "codex", "example-model", &u)
	if value(t, u.Aggregate.InputTokens) != 150 {
		t.Fatal("duplicate event added usage")
	}
}

func TestClaudeMessageDuplicatesMergeAndNewRequestReplaces(t *testing.T) {
	var o usageObserver
	var u Usage
	feed := func(s string) { o.feed([]byte(s+"\n"), "claude", "example-model", &u) }
	feed(`{"type":"assistant","parent_tool_use_id":null,"message":{"id":"message-1","model":"example-model","content":[{"type":"text","text":"private text"}],"usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":50,"output_tokens":1}}}`)
	if !u.ModelMatches || u.ReportedModel != "example-model" || value(t, u.Context.Tokens) != 350 || u.Aggregate != nil || u.Context.Window != nil {
		t.Fatalf("bad request context: %+v", u)
	}
	feed(`{"type":"assistant","message":{"id":"message-1","model":"example-model","usage":{"input_tokens":110,"output_tokens":12}}}`)
	if value(t, u.Context.Tokens) != 360 || value(t, u.Context.CachedInputTokens) != 200 {
		t.Fatal("duplicate message added or lost known fields")
	}
	feed(`{"type":"assistant","parent_tool_use_id":"tool-child","message":{"id":"child-message","model":"other-model","usage":{"input_tokens":999,"cache_read_input_tokens":999,"cache_creation_input_tokens":999}}}`)
	if !u.ModelMatches || value(t, u.Context.Tokens) != 360 {
		t.Fatal("subagent contaminated primary usage")
	}
	feed(`{"type":"result","usage":{"input_tokens":700,"cache_read_input_tokens":900,"cache_creation_input_tokens":60,"output_tokens":150},"modelUsage":{"example-model":{"contextWindow":200000},"other-model":{"contextWindow":1000000}}}`)
	if value(t, u.Context.Tokens) != 360 || value(t, u.Context.Window) != 200000 || u.Aggregate.Scope != "reported_total" || value(t, u.Aggregate.InputTokens) != 700 {
		t.Fatal("result aggregate replaced current request context")
	}
	feed(`{"type":"assistant","message":{"id":"message-2","model":"example-model","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}}`)
	if value(t, u.Context.Tokens) != 30 || value(t, u.Context.Window) != 200000 {
		t.Fatal("new request accumulated old context")
	}
	feed(`{"type":"assistant","message":{"id":"message-3","model":"example-model"}}`)
	if u.Context.Tokens != nil || u.Context.InputTokens != nil || value(t, u.Context.Window) != 200000 {
		t.Fatal("new request without usage retained old request counts")
	}
}

func TestClaudeUnknownAndMismatchedWindow(t *testing.T) {
	for _, reported := range []string{"", "other-model", "example-model"} {
		t.Run(reported, func(t *testing.T) {
			var o usageObserver
			var u Usage
			if reported != "" {
				o.feed([]byte(`{"type":"assistant","message":{"id":"message-1","model":"`+reported+`","usage":{"input_tokens":10}}}`+"\n"), "claude", "example-model", &u)
			}
			o.feed([]byte(`{"type":"result","usage":{"input_tokens":9000},"modelUsage":{"example-model":{"contextWindow":200000}}}`+"\n"), "claude", "example-model", &u)
			if u.Context != nil && u.Context.Tokens != nil {
				t.Fatal("missing cache counts inferred as zero")
			}
			if reported != "example-model" && u.Context != nil && u.Context.Window != nil {
				t.Fatal("window inferred for unknown or mismatched model")
			}
			if reported == "example-model" && value(t, u.Context.Window) != 200000 {
				t.Fatal("reported matching window lost")
			}
		})
	}
}

func TestMalformedCountsAndOversizedLinesAreIgnored(t *testing.T) {
	var o usageObserver
	var u Usage
	valid := []byte(`{"type":"turn.completed","usage":{"input_tokens":50,"cached_input_tokens":40,"output_tokens":5}}` + "\n")
	o.feed(valid, "codex", "example-model", &u)
	for _, bad := range []string{"-1", "1.5", "1e3", "true", `"100"`, "1000000000001", "9223372036854775808"} {
		o.feed([]byte(`{"type":"turn.completed","usage":{"input_tokens":`+bad+`,"output_tokens":6}}`+"\n"), "codex", "example-model", &u)
		if value(t, u.Aggregate.InputTokens) != 50 || value(t, u.Aggregate.OutputTokens) != 5 {
			t.Fatalf("invalid count %s accepted", bad)
		}
	}
	for _, bad := range []string{"{", `{"type":"turn.completed","usage":[]}`, `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":2}}`, "ordinary check output"} {
		o.feed([]byte(bad+"\n"), "codex", "example-model", &u)
		if value(t, u.Aggregate.InputTokens) != 50 {
			t.Fatal("malformed telemetry changed counters")
		}
	}
	o.feed(bytes.Repeat([]byte("x"), maxEventLine+1), "codex", "example-model", &u)
	if len(o.line) != 0 || !o.dropping {
		t.Fatal("oversized line retained")
	}
	o.feed(valid, "codex", "example-model", &u) // Rest of oversize line, not a new event.
	if len(o.line) != 0 || o.dropping {
		t.Fatal("oversized line did not recover at newline")
	}
	o.feed(valid, "codex", "example-model", &u)
	if value(t, u.Aggregate.InputTokens) != 50 || len(o.line) != 0 {
		t.Fatal("following event was lost")
	}
	if bytes.Contains(o.line[:cap(o.line)], []byte("turn.completed")) {
		t.Fatal("consumed transcript bytes retained")
	}
}

func TestSessionBoundaryClearsNativeUsage(t *testing.T) {
	var o usageObserver
	var u Usage
	o.feed([]byte(`{"type":"thread.started","thread_id":"session-one"}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":100}}`+"\n"), "codex", "example-model", &u)
	o.feed([]byte(`{"type":"thread.started","thread_id":"session-one"}`+"\n"), "codex", "example-model", &u)
	if u.Aggregate == nil {
		t.Fatal("duplicate session start cleared aggregate")
	}
	o.feed([]byte(`{"type":"thread.started","thread_id":"session-two"}`+"\n"), "codex", "example-model", &u)
	if u.Aggregate != nil || u.Context != nil {
		t.Fatal("new session retained aggregate")
	}
}

func TestClaudeForeignSessionIsIgnored(t *testing.T) {
	var o usageObserver
	var u Usage
	o.feed([]byte(`{"type":"system","subtype":"init","session_id":"primary","model":"example-model"}`+"\n"), "claude", "example-model", &u)
	o.feed([]byte(`{"type":"assistant","session_id":"foreign","message":{"id":"message-1","model":"other-model","usage":{"input_tokens":500}}}`+"\n"), "claude", "example-model", &u)
	if u.Context != nil || !u.ModelMatches || u.ReportedModel != "example-model" {
		t.Fatal("foreign session contaminated current usage")
	}
}

func TestTrackerResetsTelemetryAtAttemptAndRoleBoundaries(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	event := []byte(`{"type":"turn.completed","usage":{"input_tokens":100}}` + "\n")
	tracker.NativeEvent(event)
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if onlyView(t, r, time.Now()).Activity.Usage.Aggregate == nil {
		t.Fatal("state update cleared current usage")
	}
	j.SessionID = "newly-learned-session"
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if onlyView(t, r, time.Now()).Activity.Usage.Aggregate == nil {
		t.Fatal("first journal session ID cleared observed usage")
	}
	j.Attempt++
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if onlyView(t, r, time.Now()).Activity.Usage.Aggregate != nil {
		t.Fatal("attempt retained old telemetry")
	}
	tracker.NativeEvent(event)
	j.SessionID = "different-session"
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if onlyView(t, r, time.Now()).Activity.Usage.Aggregate != nil {
		t.Fatal("changed session retained old telemetry")
	}
	tracker.NativeEvent(event)
	j.State = "reviewing"
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	u := onlyView(t, r, time.Now()).Activity.Usage
	if u.Aggregate != nil || u.Context != nil || u.ReportedModel != "" {
		t.Fatal("review inherited implementation telemetry")
	}
}

func TestUsageSnapshotContainsNoTranscriptAndFinalLineIsFlushed(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	private := "private-example-ticket-text-never-store-in-activity"
	tracker.NativeEvent([]byte(`{"type":"item.completed","item":{"text":"` + private + `"}}` + "\n"))
	tracker.NativeEvent([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"extra":"` + private + `"}}`))
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(private)) {
		t.Fatal("snapshot contains transcript")
	}
	if value(t, onlyView(t, r, time.Now()).Activity.Usage.Aggregate.InputTokens) != 100 {
		t.Fatal("final partial line not flushed")
	}
}

func TestHeartbeatFlushesTelemetryDuringConcurrentUpdates(t *testing.T) {
	r, _, j := fixture(t)
	tracker, err := r.Begin(filepath.Join(j.Plan.Root, j.ID), j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				tracker.NativeEvent([]byte(`{"type":"turn.completed","usage":{"input_tokens":100}}` + "\n"))
				if err := tracker.Update(j); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	before := onlyView(t, r, time.Now()).Activity.HeartbeatAt
	deadline := time.Now().Add(HeartbeatInterval + 3*time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		v := onlyView(t, r, time.Now())
		if v.Activity.HeartbeatAt.After(before) {
			if value(t, v.Activity.Usage.Aggregate.InputTokens) != 100 {
				t.Fatal("heartbeat lost telemetry")
			}
			return
		}
	}
	t.Fatal("background heartbeat did not persist telemetry")
}

func TestUsageJSONRemainsCompactAndUnknownIsAbsent(t *testing.T) {
	data, err := json.Marshal(Usage{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "input_tokens") || strings.Contains(string(data), "context") {
		t.Fatalf("unknown usage represented as zero: %s", data)
	}
}

func TestOnlyNativeTransportCanUpdateUsageWithoutDoubleCounting(t *testing.T) {
	r, dir, j := fixture(t)
	tracker, err := r.Begin(dir, j)
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	event := []byte(`{"type":"turn.completed","usage":{"input_tokens":100}}` + "\n")
	if _, err := tracker.Writer(nil).Write(event); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	before := onlyView(t, r, time.Now()).Activity
	if before.Usage.Aggregate != nil || before.Usage.Context != nil {
		t.Fatal("generic JSON log changed native usage")
	}
	if before.OutputBytes != uint64(len(event)) {
		t.Fatal("generic output bytes missing")
	}
	tracker.NativeEvent(event)
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	after := onlyView(t, r, time.Now()).Activity
	if value(t, after.Usage.Aggregate.InputTokens) != 100 {
		t.Fatal("native event not observed")
	}
	if after.OutputBytes != before.OutputBytes || !after.LastActivityAt.Equal(before.LastActivityAt) {
		t.Fatal("native event counted output activity twice")
	}
	tracker.Activity([]byte(`{"type":"turn.completed","usage":{"input_tokens":900}}` + "\n"))
	if err := tracker.Update(j); err != nil {
		t.Fatal(err)
	}
	if value(t, onlyView(t, r, time.Now()).Activity.Usage.Aggregate.InputTokens) != 100 {
		t.Fatal("later generic JSON log changed native usage")
	}
}
