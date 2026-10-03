package dashboard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestOrderedPutsQuestionsAndBrokenControllersBeforeReadyAndActiveRuns(t *testing.T) {
	now := time.Now().UTC()
	views := []runstatus.View{
		{ID: "active", Available: true, Live: true, State: "implementing", UpdatedAt: now},
		{ID: "ready", Available: true, Stopped: true, NeedsAttention: true, State: "ready", UpdatedAt: now},
		{ID: "blocked-old", Available: true, NeedsAttention: true, State: "blocked", UpdatedAt: now.Add(-time.Hour)},
		{ID: "question", Available: true, NeedsAttention: true, State: "waiting_for_human", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "stale", Available: true, Stale: true, NeedsAttention: true, State: "ci", UpdatedAt: now},
		{ID: "unavailable", NeedsAttention: true, UpdatedAt: now.Add(-time.Minute)},
	}
	ordered := Ordered(views)
	var ids []string
	for _, view := range ordered {
		ids = append(ids, view.ID)
	}
	want := []string{"question", "stale", "unavailable", "blocked-old", "ready", "active"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("attention order: %v, want %v", ids, want)
	}
	if views[0].ID != "active" {
		t.Fatal("sorting mutated caller's registry snapshot")
	}
}

func TestListDistinguishesQuietLiveControllerFromStaleHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	quiet := runstatus.View{ID: "quiet-live", Available: true, Live: true, State: "ci", Root: "/example/quiet", LastActivityAt: now.Add(-time.Hour), HeartbeatAt: now, StartedAt: now.Add(-2 * time.Hour)}
	stale := quiet
	stale.ID, stale.Root, stale.Live, stale.Stale, stale.NeedsAttention = "stale-run", "/example/stale", false, true, true
	stale.HeartbeatAt = now.Add(-time.Minute)
	var output bytes.Buffer
	if err := List(&output, []runstatus.View{quiet, stale}, now); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "2 runs  1 live  1 need attention") || !strings.Contains(text, "heartbeat is stale") {
		t.Fatalf("liveness summary missing: %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "quiet-live") && (!strings.Contains(line, "live") || strings.Contains(line, "stale")) {
			t.Fatalf("quiet controller mislabeled: %s", line)
		}
		if strings.Contains(line, "stale-run") && !strings.Contains(line, "stale") {
			t.Fatalf("stale controller mislabeled: %s", line)
		}
	}
}

func TestSelectRequiresUnambiguousPrefixOrExactID(t *testing.T) {
	views := []runstatus.View{{ID: "abcdef000000000000000001"}, {ID: "abcdef111111111111111111"}, {ID: "123456000000000000000002"}}
	for _, id := range []string{"abcdef0", views[0].ID, "123456"} {
		got, err := Select(views, id)
		if err != nil || !strings.HasPrefix(got.ID, id) {
			t.Fatalf("Select(%q): %+v, %v", id, got, err)
		}
	}
	for _, id := range []string{"", "1", "12345", "abcdef", "999999"} {
		if _, err := Select(views, id); err == nil {
			t.Fatalf("unsafe/ambiguous/missing selector %q accepted", id)
		}
	}
}

func assertNoTerminalControls(t *testing.T, text string) {
	t.Helper()
	for _, char := range text {
		if char != '\n' && char != '\t' && (unicode.IsControl(char) || unicode.In(char, unicode.Cf)) {
			t.Fatalf("unsafe terminal rune U+%04X in %q", char, text)
		}
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepositoryQuestionFindingAndLogsCannotControlTerminal(t *testing.T) {
	directory := realTemp(t)
	payload := "visible\x1b[31m\x1b]0;title\a\u202e\u200b\rhidden"
	if err := os.WriteFile(filepath.Join(directory, "events-1.jsonl"), []byte(payload+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v := runstatus.View{ID: "0123456789abcdef01234567", Available: true, State: "waiting_for_human", Root: "/example/" + payload, Directory: directory, Reference: payload, Ticket: payload + ".md", StopReason: payload,
		Questions: []string{payload}, Findings: []workrun.Finding{{Path: payload, Scenario: payload, Recommendation: payload}}, Journal: &workrun.Journal{Attempt: 1}}
	for _, render := range []func(*bytes.Buffer) error{
		func(out *bytes.Buffer) error { return List(out, []runstatus.View{v}, time.Now()) },
		func(out *bytes.Buffer) error { return Detail(out, v, time.Now(), true) },
	} {
		var out bytes.Buffer
		if err := render(&out); err != nil {
			t.Fatal(err)
		}
		assertNoTerminalControls(t, out.String())
		if !strings.Contains(out.String(), "visible") {
			t.Fatal("sanitizing discarded ordinary readable content")
		}
	}
}

func TestTailBoundsBytesAndLinesAndRejectsUnsafePaths(t *testing.T) {
	directory := realTemp(t)
	var source strings.Builder
	source.WriteString("outside-tail-marker\n" + strings.Repeat("x", 20*1024) + "\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&source, "line-%02d\n", i)
	}
	path := filepath.Join(directory, "events-1.jsonl")
	if err := os.WriteFile(path, []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	lines, err := Tail(directory, "events-1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 12 || lines[0] != "line-18" || lines[11] != "line-29" || strings.Contains(strings.Join(lines, "\n"), "outside-tail-marker") {
		t.Fatalf("unbounded or wrong tail: %v", lines)
	}
	for _, name := range []string{"../events-1.jsonl", path, ".", ""} {
		if _, err := Tail(directory, name); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	if err := os.Symlink(path, filepath.Join(directory, "linked.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Tail(directory, "linked.jsonl"); err == nil {
		t.Fatal("symlink log accepted")
	}
	link := filepath.Join(directory, "linked-directory")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Tail(link, "events-1.jsonl"); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestTailRejectsSymlinkHiddenByParentTraversal(t *testing.T) {
	root := realTemp(t)
	for _, name := range []string{"checked", "checked/run", "other", "run"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "checked/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run/events-1.jsonl"), []byte("outside-validated-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preserve the raw path: filepath.Join would hide the component under test.
	if tail, err := Tail(root+"/checked/link/../run", "events-1.jsonl"); err == nil {
		t.Fatalf("read outside validated directory: %v", tail)
	}
}

func TestBlockedChecksDetailUsesCheckFailureOutput(t *testing.T) {
	directory := realTemp(t)
	for name, text := range map[string]string{"events-1.jsonl": "earlier-provider-output", "checks-1.log": "check-failure-diagnostic"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j := &workrun.Journal{Attempt: 1, CheckAttempt: 1, ResumeState: "checking", Evidence: workrun.CheckEvidence{Log: "checks-1.log"}}
	v := runstatus.View{Available: true, State: "blocked", Directory: directory, Journal: j}
	var output bytes.Buffer
	if err := Detail(&output, v, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "check-failure-diagnostic") || strings.Contains(output.String(), "earlier-provider-output") {
		t.Fatalf("wrong diagnostic selected: %s", output.String())
	}
}

func TestDetailShowsContextPercentageOnlyForReportedMatchingWindow(t *testing.T) {
	tokens, window, zero, aggregate := int64(200), int64(1000), int64(0), int64(5000)
	for _, scenario := range []struct {
		name       string
		usage      runstatus.Usage
		want       string
		percentage bool
	}{
		{name: "no telemetry", want: "Context: unknown"},
		{name: "native aggregate is not context", usage: runstatus.Usage{ModelMatches: true, Aggregate: &runstatus.TokenUsage{InputTokens: &aggregate}}, want: "Context: unknown | Native totals: input 5000, cached unknown, output unknown"},
		{name: "context without reported window", usage: runstatus.Usage{ModelMatches: true, Context: &runstatus.ContextUsage{Tokens: &tokens}}, want: "Context: 200 tokens"},
		{name: "reported model differs", usage: runstatus.Usage{ModelMatches: false, Context: &runstatus.ContextUsage{Tokens: &tokens, Window: &window}}, want: "Context: 200 tokens"},
		{name: "zero window", usage: runstatus.Usage{ModelMatches: true, Context: &runstatus.ContextUsage{Tokens: &tokens, Window: &zero}}, want: "Context: 200 tokens"},
		{name: "reported matching window", usage: runstatus.Usage{ModelMatches: true, Context: &runstatus.ContextUsage{Tokens: &tokens, Window: &window}}, want: "Context: 200 tokens / 1000 (20.0%)", percentage: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			view := runstatus.View{Provider: "codex", Model: "gpt-6.1-sol", Activity: runstatus.Snapshot{Usage: scenario.usage}}
			var output bytes.Buffer
			if err := Detail(&output, view, time.Now(), false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), scenario.want) {
				t.Fatalf("usage detail omitted %q: %s", scenario.want, output.String())
			}
			if strings.Contains(output.String(), "%") != scenario.percentage {
				t.Fatalf("invented or missing context percentage: %s", output.String())
			}
		})
	}
}
