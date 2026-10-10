package runprogress

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func TestAppendCursorAndPartialRecord(t *testing.T) {
	dir := privateDir(t)
	r, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Record(Event{RunID: "1234567890", Source: "sdlc", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	events, cursor, err := Read(dir, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("%v %v", events, err)
	}
	data, _ := json.Marshal(Event{Version: 1, Source: "checks", Text: "passed"})
	f, err := os.OpenFile(filepath.Join(dir, "progress.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	events, next, err := Read(dir, cursor)
	if err != nil || len(events) != 0 || next != cursor {
		t.Fatalf("partial consumed: %v %d %v", events, next, err)
	}
	f.Write([]byte{'\n'})
	f.Close()
	events, next, err = Read(dir, cursor)
	if err != nil || len(events) != 1 || next <= cursor {
		t.Fatalf("completed line: %v %v", events, err)
	}
	if _, _, err = Read(dir, next+1); err == nil {
		t.Fatal("accepted cursor beyond file")
	}
	if _, _, err = Read(dir, 1); err == nil {
		t.Fatal("accepted cursor in record")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, "progress.jsonl"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestConcurrentSourcesAndTailBound(t *testing.T) {
	dir := privateDir(t)
	r, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, source := range []string{"sdlc", "checks", "agent"} {
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := r.Record(Event{Source: source, Text: strings.Repeat("x", 1000)}); err != nil {
					t.Error(err)
				}
			}
		}(source)
	}
	wg.Wait()
	r.Close()
	events, next, err := Read(dir, 0)
	if err != nil || len(events) == 0 || len(events) >= 300 {
		t.Fatalf("tail bound %d %v", len(events), err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Source] = true
	}
	if len(seen) < 1 {
		t.Fatal("lost sources")
	}
	pending, err := Pending(dir, next)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("tail left backlog")
	}
}
func TestRejectSymlinkAndPublicFile(t *testing.T) {
	dir := privateDir(t)
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("private"), 0600)
	os.Symlink(target, filepath.Join(dir, "progress.jsonl"))
	if _, err := Open(dir, nil); err == nil {
		t.Fatal("followed symlink")
	}
	if _, _, err := Read(dir, 0); err == nil {
		t.Fatal("read symlink")
	}
	os.Remove(filepath.Join(dir, "progress.jsonl"))
	os.WriteFile(filepath.Join(dir, "progress.jsonl"), nil, 0644)
	if _, err := Open(dir, nil); err == nil {
		t.Fatal("accepted public file")
	}
	if _, _, err := ReadLines(dir, "../target", 0); err == nil {
		t.Fatal("accepted unsafe log name")
	}
}
func TestFormatAndNativeSummaries(t *testing.T) {
	formatted := Format(Event{RunID: "abcdefghijk", Source: "agent", Provider: "codex", Role: "implementation", Text: "\x1b[31mhello\x1b[0m\r\nnext\x07"})
	if formatted != "[abcdefgh agent:codex implementation] hello\n[abcdefgh agent:codex implementation] next\n" {
		t.Fatal(formatted)
	}
	for _, tc := range []struct{ line, want string }{
		{`{"type":"item.completed","item":{"type":"agent_message","text":"Answer"}}`, "Answer"},
		{`{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"Check passed"}}`, "Command finished|Check passed"},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Working"},{"type":"tool_use","name":"Read","id":"opaque-secret"}]}}`, "Working|Tool: Read"},
		{`{"type":"user","message":{"content":[{"type":"tool_result","content":"Tool result"}]}}`, "Tool result"},
		{`{"type":"result","result":"Complete"}`, "Complete"},
		{`{"type":"stream_event","event":{"delta":{"text":"repeat"}}}`, ""},
		{`{"type":"item.started","item":{"type":"collab_tool_call","tool":"wait","prompt":"private-argument","receiver_thread_ids":["private-id"]}}`, "Agent tool started: wait"},
		{`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"wait","agents_states":{"private-id":{"message":"private-result"}}}}`, "Agent tool completed: wait"},
	} {
		if got := strings.Join(Summarize("codex", []byte(tc.line)), "|"); got != tc.want {
			t.Fatalf("%s: %s", tc.line, got)
		}
	}
	dir := privateDir(t)
	var live bytes.Buffer
	r, _ := Open(dir, &live)
	s := NewStream(r, Event{Source: "checks"}, false)
	s.Write([]byte("par"))
	s.Write([]byte("tial"))
	s.Finish()
	r.Close()
	if !strings.Contains(live.String(), "partial") {
		t.Fatal(live.String())
	}
}
func TestMissingAndTruncatedProgress(t *testing.T) {
	dir := privateDir(t)
	if _, _, err := Read(dir, 0); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	r, _ := Open(dir, nil)
	r.Record(Event{Source: "sdlc", Text: "one"})
	r.Close()
	_, next, _ := Read(dir, 0)
	os.Truncate(filepath.Join(dir, "progress.jsonl"), 0)
	if _, _, err := Read(dir, next); err == nil {
		t.Fatal("accepted truncated cursor")
	}
}

func TestReadBoundedDrainsRecentTailWithoutSkipping(t *testing.T) {
	dir := privateDir(t)
	r, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := r.Record(Event{Source: "sdlc", Text: strings.Repeat(string(rune('A'+i%26)), 2000)}); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	expected, final, err := Read(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var received []Event
	var cursor int64
	for n := 0; n < 200; n++ {
		events, next, err := ReadBounded(dir, cursor, 4500)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			break
		}
		if len(events) > 2 {
			t.Fatalf("budget exceeded: %d events", len(events))
		}
		if next <= cursor {
			t.Fatal("cursor did not advance")
		}
		received = append(received, events...)
		cursor = next
	}
	if len(received) != len(expected) || cursor != final {
		t.Fatalf("drain lost records: %d/%d cursor %d/%d", len(received), len(expected), cursor, final)
	}
	for i := range expected {
		if received[i] != expected[i] {
			t.Fatalf("record %d changed", i)
		}
	}
	// A tiny budget still advances by one whole record, without consuming the next.
	one, next, err := ReadBounded(dir, 0, 1)
	if err != nil || len(one) != 1 {
		t.Fatalf("tiny budget: %v %v", one, err)
	}
	two, _, err := ReadBounded(dir, next, 1)
	if err != nil || len(two) != 1 || two[0] != expected[1] {
		t.Fatalf("tiny budget skipped: %v %v", two, err)
	}
	if _, _, err := ReadBounded(dir, 0, 0); err == nil {
		t.Fatal("accepted zero budget")
	}
}
func TestReadLinesBoundedPreservesCursorAndPartialLine(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "checks-1.log")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird"), 0600); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := ReadLinesBounded(dir, "checks-1.log", 0, 8)
	if err != nil || len(first) != 1 || first[0] != "first" || cursor != 6 {
		t.Fatalf("first: %v %d %v", first, cursor, err)
	}
	second, next, err := ReadLinesBounded(dir, "checks-1.log", cursor, 1)
	if err != nil || len(second) != 1 || second[0] != "second" || next != 13 {
		t.Fatalf("second: %v %d %v", second, next, err)
	}
	partial, last, err := ReadLinesBounded(dir, "checks-1.log", next, 10)
	if err != nil || len(partial) != 0 || last != next {
		t.Fatalf("partial consumed: %v %d %v", partial, last, err)
	}
}
func TestRejectUncleanOrRelativeProgressDirectory(t *testing.T) {
	dir := privateDir(t)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", dir + "/.", link + "/..", dir + "/../" + filepath.Base(dir)} {
		if _, err := Open(path, nil); err == nil {
			t.Fatalf("accepted directory %q", path)
		}
		if _, _, err := Read(path, 0); err == nil {
			t.Fatalf("read directory %q", path)
		}
	}
}
func TestRejectForgedEventFields(t *testing.T) {
	for _, change := range []func(*Event){func(e *Event) { e.Source = "untrusted" }, func(e *Event) { e.RunID = strings.Repeat("x", 129) }, func(e *Event) { e.Provider = strings.Repeat("x", 65) }, func(e *Event) { e.Role = strings.Repeat("x", 65) }, func(e *Event) { e.Stage = strings.Repeat("x", 65) }, func(e *Event) { e.Text = strings.Repeat("x", textLimit+100) }} {
		dir := privateDir(t)
		event := Event{Version: 1, Source: "sdlc", Text: "okay"}
		change(&event)
		data, _ := json.Marshal(event)
		if err := os.WriteFile(filepath.Join(dir, "progress.jsonl"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadBounded(dir, 0, 1000); err == nil {
			t.Fatal("accepted forged event")
		}
	}
}

func TestReadLinesFinalConsumesStoppedFragmentOnly(t *testing.T) {
	dir := privateDir(t)
	os.WriteFile(filepath.Join(dir, "checks-1.log"), []byte("first\npartial"), 0600)
	lines, cursor, err := ReadLinesFinal(dir, "checks-1.log", 0, 100, false)
	if err != nil || len(lines) != 1 || cursor != 6 {
		t.Fatalf("live partial consumed: %v %d %v", lines, cursor, err)
	}
	lines, next, err := ReadLinesFinal(dir, "checks-1.log", cursor, 1, true)
	if err != nil || len(lines) != 1 || lines[0] != "partial" || next != 13 {
		t.Fatalf("stopped partial lost: %v %d %v", lines, next, err)
	}
	lines, _, err = ReadLinesFinal(dir, "checks-1.log", next, 100, true)
	if err != nil || len(lines) != 0 {
		t.Fatalf("fragment repeated: %v %v", lines, err)
	}
	lines, cursor, err = ReadLinesFinal(dir, "checks-1.log", 0, 6, true)
	if err != nil || len(lines) != 1 || cursor != 6 {
		t.Fatalf("budget consumed unreturned fragment: %v %d %v", lines, cursor, err)
	}
}
