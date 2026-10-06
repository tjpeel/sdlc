package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func capturedModel(script string) *Model {
	return NewModel(context.Background(), Config{Commands: []Command{{Name: "test", Native: true}}, Execute: func(ctx context.Context, _ string, args []string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script, "test", args[len(args)-1]), nil
	}})
}
func captureMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("command update timed out")
		return nil
	}
}
func finishCapture(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		_, cmd = m.Update(captureMessage(t, cmd))
	}
}
func TestCapturedCommandRetainsBothStreamsAndResult(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			m := capturedModel(fmt.Sprintf("printf 'hello ✓\\n'; printf '\\033[31mwarning\\033[0m\\n' >&2; exit %d", code))
			finishCapture(t, m, enter(m, "/test 'two words'"))
			for _, want := range []string{"Command: /bin/sh", "'two words'", "hello ✓", "warning"} {
				if !strings.Contains(m.body, want) {
					t.Fatalf("missing %q: %s", want, m.body)
				}
			}
			if strings.Contains(m.body, "\x1b") || m.busy || m.lastError != (code != 0) {
				t.Fatalf("bad result: %q", m.body)
			}
			if code == 0 && !strings.Contains(m.commandOutcome, "exit 0") {
				t.Fatal("missing success result")
			}
			if code != 0 && !strings.Contains(m.commandOutcome, "exit status 7") {
				t.Fatal("missing failure result")
			}
		})
	}
}
func TestCapturedOutputStreamsWhileSelectionIsFrozen(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "release")
	m := capturedModel("printf 'first\\n'; while [ ! -f \"$1\" ]; do sleep 0.01; done; printf 'last\\n'")
	cmd := enter(m, "/test '"+gate+"'")
	_, cmd = m.Update(captureMessage(t, cmd))
	if !m.busy || !strings.Contains(m.body, "first") {
		t.Fatalf("no output before exit: %s", m.body)
	}
	m.draft, m.cursor = []rune("draft"), 2
	_, mouse := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 1, Y: 0})
	requireMouseCommand(t, mouse, tea.DisableMouse())
	frozen := m.View()
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	finishCapture(t, m, cmd)
	if m.View() != frozen || !strings.Contains(m.body, "last") {
		t.Fatal("selection stopped output collection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "last") || string(m.draft) != "draft" || m.cursor != 2 {
		t.Fatal("resume lost buffered output or draft")
	}
}
func TestCapturedCancellationRetainsCleanupAndRejectsStaleResult(t *testing.T) {
	m := capturedModel("trap 'printf cleanup; exit 0' INT; printf ready; while :; do sleep 0.05; done")
	cmd := enter(m, "/test")
	_, cmd = m.Update(captureMessage(t, cmd))
	if !strings.Contains(m.body, "ready") {
		t.Fatal("process did not start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	finishCapture(t, m, cmd)
	if m.busy || !strings.Contains(m.body, "cleanup") || !strings.Contains(m.commandOutcome, "cancelled") {
		t.Fatalf("cleanup/cancel result: %s", m.body)
	}
	old := m.body
	m.Update(captureResult{generation: m.generation - 1, text: "stale", done: true})
	if m.body != old {
		t.Fatal("stale process replaced output")
	}
}
func TestCapturedOutputRetentionIsBoundedAndUTF8Safe(t *testing.T) {
	s := &captureStream{ctx: context.Background(), notify: make(chan struct{}, 1)}
	s.Write(bytes.Repeat([]byte("✓"), captureLimit))
	s.Write([]byte("tail"))
	msg := s.wait(1)().(captureResult)
	if len(s.output) > captureLimit || !utf8.ValidString(msg.text) || !strings.HasPrefix(msg.text, "[Earlier output omitted]") || !strings.HasSuffix(msg.text, "tail") {
		t.Fatal("invalid bounded output")
	}
}
func TestPlainCapturedCommandRetainsStderrAndStatus(t *testing.T) {
	var output bytes.Buffer
	m := capturedModel("printf stdout; printf stderr >&2")
	c := m.config
	c.Input, c.Output = strings.NewReader("/test\n/exit\n"), &output
	if err := RunPlain(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Command: /bin/sh", "stdout", "stderr", "exit 0"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %s: %s", want, output.String())
		}
	}
}
func TestMouseSelectionOnlyStartsOnOutputPress(t *testing.T) {
	m := NewModel(context.Background(), Config{})
	for _, msg := range []tea.MouseMsg{
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, Y: 0},
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: m.height - 2},
		{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: m.height - 1},
		{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress, Y: 0},
	} {
		m.Update(msg)
		if m.selectionView != "" {
			t.Fatal("non-output press froze frame")
		}
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 0})
	if m.selectionView == "" {
		t.Fatal("output click did not enter selection")
	}
}

func TestCapturedTextWaitsForSplitUTF8AndStripsANSI(t *testing.T) {
	if got := capturedText([]byte("start \xe2\x9c"), false); got != "start " {
		t.Fatalf("split rune: %q", got)
	}
	if got := capturedText([]byte("start ✓\x1b[31mred\x1b[0m"), true); got != "start ✓red" {
		t.Fatalf("ANSI: %q", got)
	}
}
func TestPlainCaptureDecoderHandlesSplitControlSequences(t *testing.T) {
	var out bytes.Buffer
	w := newPlainCaptureWriter(&out)
	for _, part := range []string{"one \x1b[3", "1mred\x1b[0m \xe2", "\x9c\x93\n\x1b]52;c;", "ignored\aend\r\x00"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != "one red ✓\nend" {
		t.Fatalf("unsafe output: %q", got)
	}
}
func TestCapturedUpdatesKeepScrolledViewportAndRefreshPrompt(t *testing.T) {
	m := NewModel(context.Background(), Config{Prompt: func(context.Context, string) (string, bool) { return "changed", true }})
	m.height, m.width = 6, 60
	m.commandEcho = "Command: test"
	m.body = m.commandEcho + "\n" + strings.Repeat("old line\n", 20)
	m.scroll = 10
	before := strings.Split(m.View(), "\n")[0]
	m.Update(captureResult{generation: m.generation, text: strings.Repeat("old line\n", 20) + "later\n", stream: &captureStream{}})
	if got := strings.Split(m.View(), "\n")[0]; got != before || m.scroll != 11 {
		t.Fatalf("scrolled output moved: %q, scroll %d", got, m.scroll)
	}
	m.Update(captureResult{generation: m.generation, text: "finished", done: true})
	if m.branch != "changed" || !m.dirty {
		t.Fatal("prompt not refreshed")
	}
	if m.scroll < 0 {
		t.Fatal("scroll below retained output")
	}
}
func TestNewLocalCommandClearsReadEcho(t *testing.T) {
	for _, line := range []string{"/clear", "/help", "/reference example"} {
		m := NewModel(context.Background(), Config{})
		m.readEcho = "Command: previous"
		enter(m, line)
		if m.readEcho != "" {
			t.Fatalf("stale command after %s", line)
		}
	}
}

type observedOutput struct {
	mu    sync.Mutex
	text  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (o *observedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.text.Write(p)
	if strings.Contains(o.text.String(), "first output") {
		o.once.Do(func() { close(o.ready) })
	}
	return n, err
}
func TestPlainCapturedOutputStreamsBeforeExit(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "release")
	output := &observedOutput{ready: make(chan struct{})}
	m := capturedModel("printf 'first output\\n'; while [ ! -f \"$1\" ]; do sleep 0.01; done; printf 'last output\\n'")
	c := m.config
	c.Input, c.Output = strings.NewReader("/test '"+gate+"'\n/exit\n"), output
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunPlain(ctx, c) }()
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("plain output not streamed before exit")
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plain command did not finish")
	}
	output.mu.Lock()
	defer output.mu.Unlock()
	if !strings.Contains(output.text.String(), "last output") || !strings.Contains(output.text.String(), "exit 0") {
		t.Fatal("plain output/result missing")
	}
}

func TestInteractiveCommandOnlyHandsOffWhenHelpIsNotRequested(t *testing.T) {
	for _, tc := range []struct {
		line     string
		captured bool
	}{
		{"/auth login", false},
		{"/auth login --help", true},
		{"/auth login -h", true},
		{"/auth login --profile -h", false},
		{"/auth login -- -h", false},
	} {
		t.Run(tc.line, func(t *testing.T) {
			expected, err := Parse(tc.line)
			if err != nil {
				t.Fatal(err)
			}
			m := NewModel(context.Background(), Config{Commands: []Command{{Name: "auth login", Native: true, Interactive: true}}, Execute: func(ctx context.Context, _ string, args []string) (*exec.Cmd, error) {
				if !reflect.DeepEqual(args, expected) {
					t.Fatalf("adapter argv changed: %v", args)
				}
				return exec.CommandContext(ctx, "/bin/sh", "-c", "printf help"), nil
			}})
			cmd := enter(m, tc.line)
			if tc.captured {
				finishCapture(t, m, cmd)
				if !strings.Contains(m.body, "help") {
					t.Fatal("help was not captured")
				}
			} else {
				if _, ok := cmd().(captureResult); ok || m.busy {
					t.Fatal("interactive command was captured")
				}
			}
		})
	}
}

func TestCommandFailureStaysPinnedAbovePromptWhileReadingLongOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := capturedModel("i=0; while [ $i -lt 100 ]; do printf 'line %s\\n' \"$i\"; i=$((i+1)); done; printf '\\033[31mprivate failure\\033[0m\\n\\033]52;c;ignored\\a'; exit 7")
	m.width, m.height = 40, 9
	finishCapture(t, m, enter(m, "/test"))
	m.scroll = 50
	view := m.View()
	rows := strings.Split(view, "\n")
	if len(rows) != 9 || !strings.HasPrefix(rows[6], "\x1b[1;31mCommand failed: exit status 7") {
		t.Fatalf("failure not pinned: %q", view)
	}
	output := strings.TrimPrefix(m.body, m.commandEcho+"\n")
	if strings.Contains(output, "\x1b") || strings.Contains(output, "ignored") || strings.Contains(output, "exit status 7") {
		t.Fatal("unsafe output or duplicate final status")
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > 40 {
			t.Fatalf("physical overflow %q", row)
		}
	}
	m.draft = []rune("next draft")
	m.cursor = len(m.draft)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.commandOutcome == "" {
		t.Fatal("draft edit cleared outcome")
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Y: 6})
	if m.selectionView == "" {
		t.Fatal("pinned result cannot be copied")
	}
	frozen := m.View()
	m.Update(captureResult{generation: m.generation, text: "later", done: true})
	if m.View() != frozen {
		t.Fatal("capture update changed copy frame")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	enter(m, "/help")
	if m.commandView || m.commandOutcome != "" {
		t.Fatal("new view retained previous command outcome")
	}
}

func TestCommandOutcomeReserveColorAndClear(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := capturedModel("printf plain; exit 0")
	m.width, m.height = 18, 5
	cmd := enter(m, "/test")
	if m.outcomeHeight() != 1 {
		t.Fatal("running command did not reserve outcome row")
	}
	finishCapture(t, m, cmd)
	if m.outcomeHeight() != 1 || m.commandOutcomeKind != "success" {
		t.Fatal("completion moved reserved row")
	}
	if strings.Contains(m.commandOutcomeRow(m.width), "\x1b") || len(strings.Split(m.View(), "\n")) != 5 {
		t.Fatal("NO_COLOR or bounded geometry failed")
	}
	m.setCommandOutcome(nil, true)
	if m.commandOutcome != "Command cancelled." || m.commandOutcomeKind != "cancelled" {
		t.Fatal("cancellation metadata missing")
	}
	m.Update(result{generation: m.generation, kind: "dashboard", text: "dashboard"})
	if m.commandView || m.commandOutcome != "" {
		t.Fatal("direct view switch retained outcome")
	}
}
