package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tjpeel/sdlc/internal/runprogress"
)

func progressEvent(text string) runprogress.Event {
	return runprogress.Event{RunID: "example-run", Source: "agent", Provider: "codex", Role: "implementer", Text: text}
}

func TestProgressFollowsLaunchAndResume(t *testing.T) {
	for _, kind := range []string{"launch", "run-action"} {
		t.Run(kind, func(t *testing.T) {
			var selectors []string
			m := NewModel(context.Background(), Config{Progress: func(_ context.Context, _ string, args []string, cursor string) (runprogress.Batch, error) {
				selectors = append([]string(nil), args...)
				if cursor != "" {
					t.Fatalf("initial cursor %q", cursor)
				}
				return runprogress.Batch{Cursor: "one", Events: []runprogress.Event{progressEvent("started")}}, nil
			}})
			_, cmd := m.Update(result{kind: kind, generation: m.generation, action: RunAction{ID: "example-run"}, text: "launch receipt"})
			if cmd == nil {
				t.Fatal("no automatic feed")
			}
			m.Update(cmd())
			if !strings.Contains(m.body, "agent:codex implementer") || !strings.Contains(m.body, "started") || !m.monitor {
				t.Fatalf("feed: %q", m.body)
			}
			if len(selectors) != 0 {
				t.Fatalf("selectors: %v", selectors)
			}
			if kind == "launch" && m.selectedRun != "example-run" {
				t.Fatal("automatic feed did not select its sole run")
			}
			if kind == "run-action" && !reflect.DeepEqual(m.monitorArgs, []string{"dashboard", "--run", "example-run", "--scope", "all"}) {
				t.Fatalf("lost selected run: %v", m.monitorArgs)
			}
		})
	}
}

func TestProgressScrollDraftAndCancellation(t *testing.T) {
	m := NewModel(context.Background(), Config{Progress: func(context.Context, string, []string, string) (runprogress.Batch, error) {
		return runprogress.Batch{Cursor: "first", Events: []runprogress.Event{progressEvent(strings.Repeat("earlier output\n", 40))}}, nil
	}})
	cmd := enter(m, "/progress --run example-run")
	m.Update(cmd())
	m.draft = []rune("/answer example-run")
	m.cursor = 4
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	before := m.scroll
	pausedView := m.View()
	m.Update(progressResult{generation: m.generation, batch: runprogress.Batch{Cursor: "second", Events: []runprogress.Event{progressEvent("new output")}}})
	if m.View() != pausedView {
		t.Fatal("new output moved the paused viewport")
	}
	if m.scroll <= before || string(m.draft) != "/answer example-run" || m.cursor != 4 {
		t.Fatal("append moved paused view or replaced draft")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.scroll != 0 {
		t.Fatal("End did not follow tail")
	}
	old := m.generation
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	body := m.body
	m.Update(progressResult{generation: old, batch: runprogress.Batch{Events: []runprogress.Event{progressEvent("stale")}}})
	if m.body != body || m.monitor {
		t.Fatal("cancelled feed accepted stale output")
	}
	if cmd := enter(m, "/answer"); cmd == nil || m.busyKind != "resolve" {
		t.Fatal("selected run not available for default answer")
	}
}

func TestProgressDoneOnceAndDashboardRouting(t *testing.T) {
	for _, command := range []string{"/progress --run example-run --once", "/dashboard --run example-run --logs --once"} {
		m := NewModel(context.Background(), Config{Progress: func(context.Context, string, []string, string) (runprogress.Batch, error) {
			return runprogress.Batch{Done: true, Events: []runprogress.Event{progressEvent("finished")}}, nil
		}})
		cmd := enter(m, command)
		if cmd == nil {
			t.Fatal("no progress query")
		}
		_, next := m.Update(cmd())
		if next != nil || m.monitor || !strings.Contains(m.View(), "Tail caught up") {
			t.Fatalf("completed view: %s", m.View())
		}
	}
	m := NewModel(context.Background(), Config{})
	m.appendProgress(strings.Repeat("line\n", 2000))
	if len(strings.Split(m.body, "\n")) > 1000 || len(m.body) > 256*1024 {
		t.Fatal("unbounded scrollback")
	}
}

func TestProgressUsesShellScopeAndJSONUsesCLISnapshot(t *testing.T) {
	var selected []string
	progressCalls := 0
	c := Config{Progress: func(_ context.Context, _ string, args []string, _ string) (runprogress.Batch, error) {
		progressCalls++
		selected = args
		return runprogress.Batch{Done: true}, nil
	}, Read: func(_ context.Context, _ string, args []string) (string, error) {
		selected = args
		return `{"events":[],"cursor":"example","done":true}`, nil
	}}
	m := NewModel(context.Background(), c)
	cmd := enter(m, "/progress --run abcdef123456")
	m.Update(cmd())
	if scope, _ := optionValue(selected, "scope"); scope != "project" || selected[0] != "progress" {
		t.Fatalf("shell context lost: %v", selected)
	}
	cmd = enter(m, "/progress --run abcdef123456 --json")
	m.Update(cmd())
	if progressCalls != 1 || !hasOption(selected, "json") || !strings.Contains(m.body, `"events":[]`) || m.monitor {
		t.Fatalf("JSON did not use a CLI snapshot: %v %s", selected, m.body)
	}
}

type progressOutput struct {
	mu      sync.Mutex
	text    strings.Builder
	arrived chan struct{}
	once    sync.Once
}

func (w *progressOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.text.Write(p)
	if strings.Contains(w.text.String(), "second increment") {
		w.once.Do(func() { close(w.arrived) })
	}
	return len(p), nil
}
func TestPlainProgressArrivesBeforeNextCommand(t *testing.T) { testPlainProgressBeforeCommand(t, "") }
func TestPlainTyposKeepFollowingProgress(t *testing.T) {
	for _, typo := range []string{"/unknown\n", "/progress 'unfinished\n"} {
		t.Run(strings.TrimSpace(typo), func(t *testing.T) { testPlainProgressBeforeCommand(t, typo) })
	}
}
func testPlainProgressBeforeCommand(t *testing.T, typo string) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	output := &progressOutput{arrived: make(chan struct{})}
	done := make(chan error, 1)
	var cursors []string
	var mu sync.Mutex
	go func() {
		done <- RunPlain(context.Background(), Config{Input: input, Output: output, Progress: func(_ context.Context, _ string, _ []string, cursor string) (runprogress.Batch, error) {
			mu.Lock()
			cursors = append(cursors, cursor)
			mu.Unlock()
			if cursor == "" {
				return runprogress.Batch{Cursor: "one", Events: []runprogress.Event{progressEvent("first increment")}}, nil
			}
			return runprogress.Batch{Cursor: "two", Done: true, Events: []runprogress.Event{progressEvent("second increment")}}, nil
		}})
	}()
	if _, err := io.WriteString(writer, "/progress --run example-run\n"); err != nil {
		t.Fatal(err)
	}
	if typo != "" {
		if _, err := io.WriteString(writer, typo); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-output.arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("progress waited for another command")
	}
	if _, err := io.WriteString(writer, "/exit\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("plain monitor did not join")
	}
	output.mu.Lock()
	text := output.text.String()
	output.mu.Unlock()
	if strings.Count(text, "first increment") != 1 || strings.Count(text, "second increment") != 1 {
		t.Fatalf("duplicate feed: %s", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(cursors, []string{"", "one"}) {
		t.Fatalf("cursors: %v", cursors)
	}
}

func TestAutomaticMultiRunFeedRequiresExplicitAnswer(t *testing.T) {
	m := NewModel(context.Background(), Config{Progress: func(context.Context, string, []string, string) (runprogress.Batch, error) {
		other := progressEvent("other run")
		other.RunID = "another-run"
		return runprogress.Batch{Done: true, Events: []runprogress.Event{progressEvent("first run"), other}}, nil
	}})
	_, cmd := m.Update(result{kind: "launch", generation: m.generation, text: "receipt"})
	m.Update(cmd())
	if m.selectedRun != "" {
		t.Fatal("multi-run feed selected an arbitrary answer target")
	}
	if cmd := enter(m, "/answer"); cmd != nil || !strings.Contains(m.body, "Specify a run") {
		t.Fatal("ambiguous feed accepted a default answer")
	}
}

// A real child observes both descriptors; merely comparing assigned Go writers
// would miss os/exec replacing a wrapper with a pipe.
func TestPlainNativeOutputDescriptorHelper(t *testing.T) {
	path := os.Getenv("SDLC_TEST_NATIVE_OUTPUT")
	if path == "" {
		return
	}
	original, err := os.Stat(path)
	if err != nil {
		os.Exit(2)
	}
	for _, file := range []*os.File{os.Stdout, os.Stderr} {
		actual, err := file.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(original, actual) {
			os.Exit(3)
		}
		fmt.Fprintln(file, "native descriptor preserved")
	}
	os.Exit(0)
}
func TestPlainNativeCommandRetainsOutputDescriptors(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "native-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	err = RunPlain(context.Background(), Config{Input: strings.NewReader("/native\n/exit\n"), Output: output, Commands: []Command{{Name: "native", Native: true}}, Execute: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPlainNativeOutputDescriptorHelper$")
		child.Env = append(os.Environ(), "SDLC_TEST_NATIVE_OUTPUT="+output.Name())
		return child, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(text), "native descriptor preserved") != 2 {
		t.Fatalf("native stdout/stderr were redirected: %s", text)
	}
}
