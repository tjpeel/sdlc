package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func enter(m *Model, text string) tea.Cmd {
	m.draft = []rune(text)
	m.cursor = len(m.draft)
	m.dismissed = true
	m.suggestions = nil
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

func longHelpModel() *Model {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	for i := 0; i < 16; i++ {
		m.config.Commands = append(m.config.Commands, Command{
			Name: fmt.Sprintf("command-%02d", i), Usage: fmt.Sprintf("/command-%02d", i),
			Summary: strings.Repeat("Detailed command description ", 3),
			Flags:   []Flag{{Name: "--example", Summary: fmt.Sprintf("FLAG %02d", i)}},
		})
	}
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	enter(m, "/help")
	return m
}

func TestLongHelpStartsAtBeginning(t *testing.T) {
	m := longHelpModel()
	view := ansi.Strip(m.View())
	if !strings.HasPrefix(view, "/command-00\n") || strings.Contains(view, "FLAG 15") {
		t.Fatalf("help did not open at its beginning: %q", view)
	}
	if !strings.Contains(view, "Wheel/PgUp/PgDn scroll") {
		t.Fatal("narrow help view hides scroll controls")
	}
}

func TestSnapshotReadStartsAtSummary(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project", Commands: []Command{{Name: "version"}}, Read: func(context.Context, string, []string) (string, error) {
		return "Important summary\n" + strings.Repeat("Supporting evidence\n", 30), nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	m.Update(enter(m, "/version")())
	if !strings.HasPrefix(ansi.Strip(m.View()), "Important summary\n") {
		t.Fatalf("snapshot opened after its summary: %q", m.View())
	}
}

func TestDashboardRefreshKeepsReadingPosition(t *testing.T) {
	reads := 0
	m := NewModel(context.Background(), Config{Root: "/example/project", Read: func(context.Context, string, []string) (string, error) {
		reads++
		var body strings.Builder
		body.WriteString("Needs attention\n")
		for i := 0; i < 30+reads*5; i++ {
			fmt.Fprintf(&body, "Run detail %02d\n", i)
		}
		fmt.Fprintf(&body, "Latest snapshot %d", reads)
		return body.String(), nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	m.Update(enter(m, "/dashboard")())
	refresh := func() {
		t.Helper()
		_, cmd := m.Update(poll{})
		if cmd == nil {
			t.Fatal("dashboard refresh did not start")
		}
		m.Update(cmd())
	}
	if !strings.HasPrefix(ansi.Strip(m.View()), "Needs attention\n") {
		t.Fatal("initial dashboard hid its attention summary")
	}
	refresh()
	if !strings.HasPrefix(ansi.Strip(m.View()), "Needs attention\n") {
		t.Fatal("dashboard refresh moved away from its summary")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	first, _, _ := strings.Cut(ansi.Strip(m.View()), "\n")
	refresh()
	after, _, _ := strings.Cut(ansi.Strip(m.View()), "\n")
	if after != first {
		t.Fatalf("refresh moved the reading position: %q -> %q", first, after)
	}
	for i := 0; i < 30; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	refresh()
	if !strings.Contains(ansi.Strip(m.View()), "Latest snapshot 4") {
		t.Fatal("dashboard no longer follows its tail when scrolled to the end")
	}
}

func TestOnboardStartsAtHeadingAndScrollsWithoutLosingDraft(t *testing.T) {
	for _, wheel := range []bool{false, true} {
		t.Run(fmt.Sprintf("wheel=%v", wheel), func(t *testing.T) {
			reads := 0
			m := NewModel(context.Background(), Config{Root: "/example/project", Commands: []Command{{Name: "onboard"}}, Read: func(_ context.Context, _ string, args []string) (string, error) {
				reads++
				if !reflect.DeepEqual(args, []string{"onboard"}) {
					t.Fatalf("onboard args: %v", args)
				}
				var text strings.Builder
				text.WriteString("Project onboarding: example\n")
				for i := 1; i <= 8; i++ {
					fmt.Fprintf(&text, "\n%d. step\n  Status: unchecked\n  Purpose: review local setup\n  Next: step %d\n", i, i)
				}
				return text.String(), nil
			}})
			m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
			cmd := enter(m, "/onboard")
			if cmd == nil {
				t.Fatal("onboard did not start a read")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
			m.Update(tea.KeyMsg{Type: tea.KeyLeft})
			draft, cursor := string(m.draft), m.cursor
			_, next := m.Update(cmd())
			if next != nil || reads != 1 || m.monitor {
				t.Fatal("onboard started another read or monitor")
			}
			if view := ansi.Strip(m.View()); !strings.HasPrefix(view, "Project onboarding: example\n") || !strings.Contains(view, "1. step") {
				t.Fatalf("onboard did not open at heading: %q", view)
			}
			m.Update(tea.WindowSizeMsg{Width: 40, Height: 6})
			for i := 0; i < 100; i++ {
				if wheel {
					m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
				} else {
					m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
				}
			}
			if !strings.Contains(ansi.Strip(m.View()), "Next: step 8") || string(m.draft) != draft || m.cursor != cursor || reads != 1 {
				t.Fatalf("onboard scrolling lost final step or draft: %q", m.View())
			}
		})
	}
}

func TestHelpScrollControlsStayWithinOutput(t *testing.T) {
	for _, wheel := range []bool{false, true} {
		t.Run(fmt.Sprintf("wheel=%v", wheel), func(t *testing.T) {
			m := longHelpModel()
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/command-")})
			m.Update(tea.KeyMsg{Type: tea.KeyLeft})
			draft, cursor := string(m.draft), m.cursor
			scroll := func(down bool) {
				if wheel {
					button := tea.MouseButtonWheelUp
					if down {
						button = tea.MouseButtonWheelDown
					}
					m.Update(tea.MouseMsg{Button: button, Action: tea.MouseActionPress})
				} else {
					key := tea.KeyPgUp
					if down {
						key = tea.KeyPgDown
					}
					m.Update(tea.KeyMsg{Type: key})
				}
			}
			for i := 0; i < 150; i++ {
				scroll(true)
			}
			if !strings.Contains(m.View(), "FLAG 15") {
				t.Fatalf("cannot reach help end: %q", m.View())
			}
			for i := 0; i < 150; i++ {
				scroll(false)
			}
			if !strings.HasPrefix(m.View(), "/command-00\n") {
				t.Fatalf("scrolling past beginning hid output: %q", m.View())
			}
			if string(m.draft) != draft || m.cursor != cursor || !strings.Contains(m.View(), "› command-00") || !strings.Contains(m.View(), "\x1b[7m") {
				t.Fatal("scrolling changed draft, cursor or completion visibility")
			}
		})
	}
}

func TestHelpResizeClampsWrappedOutput(t *testing.T) {
	m := longHelpModel()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("unfinished draft")})
	draft, cursor := string(m.draft), m.cursor
	for i := 0; i < 150; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	if !strings.HasPrefix(m.View(), "/command-00\n") {
		t.Fatalf("resize left viewport beyond output: %q", m.View())
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 6})
	for i := 0; i < 150; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !strings.Contains(m.View(), "FLAG 15") || string(m.draft) != draft || m.cursor != cursor {
		t.Fatalf("resize lost output end or draft: %q", m.View())
	}
	if len(strings.Split(m.View(), "\n")) != 6 {
		t.Fatal("resized view does not fill terminal")
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatal("resized output exceeds width")
		}
	}
}

func TestAnswerMouseWheelKeepsEditorVisible(t *testing.T) {
	c := answerConfig()
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "recorded-id", Root: "/example/recorded", State: "waiting_for_human", Questions: []string{strings.Repeat("earlier context ", 100), "FINAL QUESTION"}}, nil
	}
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 8})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft stays")})
	draft, cursor := string(m.draft), m.cursor
	for _, button := range []tea.MouseButton{tea.MouseButtonWheelDown, tea.MouseButtonWheelUp} {
		for i := 0; i < 100; i++ {
			m.Update(tea.MouseMsg{Button: button, Action: tea.MouseActionPress})
		}
		view := m.View()
		marker := "State: waiting_for_human"
		if button == tea.MouseButtonWheelDown {
			marker = "FINAL QUESTION"
		}
		if !strings.Contains(view, marker) || !strings.Contains(view, "draft stays") || !strings.Contains(view, "Ctrl+S") || !strings.Contains(view, "\x1b[7m") || string(m.draft) != draft || m.cursor != cursor {
			t.Fatalf("wheel lost context or editor: %q", view)
		}
	}
}
func TestLaunchRequiresExplicitStartAndDryRunCannotStart(t *testing.T) {
	var launches int
	var received []string
	c := Config{Root: "/example/project", Read: func(_ context.Context, _ string, args []string) (string, error) {
		received = args
		return "offline plan", nil
	}, Launch: func(context.Context, string, []string) (string, error) { launches++; return "receipt", nil }}
	m := NewModel(context.Background(), c)
	m.reference = "DEMO-42"
	cmd := enter(m, "/run --ticket 01-count.md")
	m.Update(cmd())
	if launches != 0 || len(m.review) == 0 {
		t.Fatal("run launched without review")
	}
	want := []string{"run", "--ticket", "01-count.md", "--reference", "DEMO-42"}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("read argv: %#v", received)
	}
	cmd = m.start()
	m.Update(cmd())
	if launches != 1 {
		t.Fatal("explicit start did not launch")
	}
	m = NewModel(context.Background(), c)
	cmd = enter(m, "/run --ticket 01-count.md --dry-run")
	m.Update(cmd())
	if m.start() != nil || len(m.review) != 0 {
		t.Fatal("dry-run offered a launch")
	}
}
func TestResizeAndRefreshKeepDraftAndSelection(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	m.draft = []rune("/run --ticket @DEMO-42/0")
	m.cursor = 9
	m.selection = 1
	m.suggestions = []Suggestion{{Label: "one"}, {Label: "two"}}
	m.body = strings.Repeat("recorded output\n", 100)
	m.scroll = 10
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if string(m.draft) != "/run --ticket @DEMO-42/0" || m.cursor != 9 || m.selection != 1 || m.scroll != 10 {
		t.Fatal("resize lost view state")
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("overflow %q", line)
		}
	}
	if len(strings.Split(m.View(), "\n")) != 12 {
		t.Fatal("view does not fill terminal")
	}
	m.Update(result{generation: m.generation, kind: "dashboard", text: "fresh snapshot"})
	if string(m.draft) != "/run --ticket @DEMO-42/0" || m.cursor != 9 {
		t.Fatal("refresh lost draft")
	}
}
func TestCompletionEnterInsertsBeforeDispatch(t *testing.T) {
	reads := 0
	m := NewModel(context.Background(), Config{Root: "/example", Commands: []Command{{Name: "run"}, {Name: "runtime status"}}, Read: func(context.Context, string, []string) (string, error) { reads++; return "", nil }})
	m.draft = []rune("/ru")
	m.complete()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if string(m.draft) != "/run" || reads != 0 {
		t.Fatal("completion dispatched instead of inserting")
	}
}
func TestCtrlCCancelsViewWithoutLaunchingOrStopping(t *testing.T) {
	m := NewModel(context.Background(), Config{})
	m.draft = []rune("draft")
	m.review = []string{"run"}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || len(m.review) > 0 || len(m.draft) > 0 {
		t.Fatal("Ctrl+C did not clear view state")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("idle Ctrl+C did not exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("idle exit was not a UI quit")
	}
}
func TestLongestCommandChoosesNativeMutation(t *testing.T) {
	m := NewModel(context.Background(), Config{Commands: []Command{{Name: "dashboard"}, {Name: "dashboard forget", Native: true}}})
	c, ok := m.command([]string{"dashboard", "forget", "--run", "abcdef"})
	if !ok || !c.Native || c.Name != "dashboard forget" {
		t.Fatal("mutation matched read-only parent")
	}
}
func TestPlainPlanStartAndCancellation(t *testing.T) {
	launched := 0
	var out bytes.Buffer
	err := RunPlain(context.Background(), Config{Root: "/example", Input: strings.NewReader("/run --ticket one.md\n/cancel\n/start\n/run --ticket one.md\n/start\n/exit\n"), Output: &out, Read: func(context.Context, string, []string) (string, error) { return "plan", nil }, Launch: func(context.Context, string, []string) (string, error) { launched++; return "accepted", nil }})
	if err != nil || launched != 1 || !strings.Contains(out.String(), "No reviewed launch") {
		t.Fatalf("launches=%d err=%v output=%s", launched, err, out.String())
	}
}
func TestTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Fatal("/dev/null accepted as terminal")
	}
}
func TestProjectSelectionClearsReferenceNotJobIdentity(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/one", SelectProject: func(_ context.Context, root, name string) (string, error) {
		if root != "/example/one" || name != "two" {
			return "", fmt.Errorf("wrong selection")
		}
		return "/example/two", nil
	}})
	m.reference = "OLD"
	cmd := enter(m, "/project select two")
	m.Update(cmd())
	if m.root != "/example/two" || m.reference != "" {
		t.Fatal("project selection retained old reference")
	}
}

func TestTypingDuringReadDoesNotLoseResultOrLeaveBusy(t *testing.T) {
	m := NewModel(context.Background(), Config{Read: func(context.Context, string, []string) (string, error) { return "fresh snapshot", nil }})
	cmd := enter(m, "/dashboard --run abcdef --logs --page 2")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/help")})
	m.Update(cmd())
	if m.busy || string(m.draft) != "/help" || !strings.Contains(m.body, "fresh snapshot") {
		t.Fatalf("busy=%v draft=%q body=%q", m.busy, m.draft, m.body)
	}
}
func TestNativeDashboardMutationAndUnknownDoNotReachRead(t *testing.T) {
	reads, executions := 0, 0
	m := NewModel(context.Background(), Config{Commands: []Command{{Name: "dashboard"}, {Name: "dashboard forget", Native: true}}, Read: func(context.Context, string, []string) (string, error) { reads++; return "", nil }, Execute: func(context.Context, string, []string) (*exec.Cmd, error) {
		executions++
		return nil, fmt.Errorf("native boundary")
	}})
	enter(m, "/dashboard forget --run abcdef")
	enter(m, "/launch execute --id abcdef")
	if reads != 0 || executions != 1 || m.monitor {
		t.Fatalf("reads=%d native=%d monitor=%v", reads, executions, m.monitor)
	}
}
func TestMonitorPollKeepsSelectedRunAndFlags(t *testing.T) {
	var received [][]string
	m := NewModel(context.Background(), Config{Read: func(_ context.Context, _ string, args []string) (string, error) {
		received = append(received, append([]string(nil), args...))
		return fmt.Sprintf("snapshot %d", len(received)), nil
	}})
	m.width, m.height = 120, 12
	cmd := enter(m, "/dashboard --run abcdef --logs --page 2 --scope all")
	if !strings.Contains(m.View(), "Working…") {
		t.Fatal("initial dashboard read has no loading footer")
	}
	m.Update(cmd())
	m.draft = []rune("/help")
	m.cursor = 2
	footer := func() string {
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		return lines[len(lines)-1]
	}
	wantFooter := "F2 select · Wheel/PgUp/PgDn scroll · Click then drag to select · Esc resumes · / commands"
	if footer() != wantFooter {
		t.Fatalf("dashboard footer before refresh: %q", footer())
	}
	_, cmd = m.Update(poll{})
	if cmd == nil || footer() != wantFooter {
		t.Fatalf("pending refresh footer: %q", footer())
	}
	if _, duplicate := m.Update(poll{}); duplicate != nil {
		t.Fatal("duplicate poll started another read")
	}
	m.Update(cmd())
	if footer() != wantFooter || m.body != "snapshot 2" || string(m.draft) != "/help" || m.cursor != 2 {
		t.Fatalf("refresh lost view state: footer=%q body=%q draft=%q cursor=%d", footer(), m.body, m.draft, m.cursor)
	}
	want := []string{"dashboard", "--run", "abcdef", "--logs", "--page", "2", "--scope", "all", "--once"}
	if len(received) != 2 || !reflect.DeepEqual(received[0], want) || !reflect.DeepEqual(received[1], want) {
		t.Fatalf("poll argv: %#v", received)
	}
}

func TestCtrlCCancelsPendingDashboardRefreshAndIgnoresLateResult(t *testing.T) {
	var readContext context.Context
	m := NewModel(context.Background(), Config{Read: func(ctx context.Context, _ string, _ []string) (string, error) {
		readContext = ctx
		return "snapshot", nil
	}})
	cmd := enter(m, "/dashboard --run abcdef")
	m.Update(cmd())
	_, cmd = m.Update(poll{})
	late := cmd()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if readContext.Err() != context.Canceled || m.busy || m.monitor {
		t.Fatalf("refresh not cancelled: context=%v busy=%v monitor=%v", readContext.Err(), m.busy, m.monitor)
	}
	body := m.body
	if _, next := m.Update(late); next != nil || m.monitor || m.body != body {
		t.Fatalf("late refresh resumed monitoring: monitor=%v body=%q", m.monitor, m.body)
	}
}
func TestLongDraftCaretRemainsVisible(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	m.width = 40
	m.height = 8
	m.draft = []rune(strings.Repeat("x", 90) + "END")
	m.cursor = len(m.draft)
	lines := strings.Split(m.View(), "\n")
	prompt := lines[len(lines)-2]
	if !strings.Contains(ansi.Strip(prompt), "END") || !strings.Contains(prompt, "\x1b[7m") {
		t.Fatalf("cursor tail invisible: %q", prompt)
	}
}
func TestLaunchFailureKeepsReviewedDraftAndManualReceipt(t *testing.T) {
	m := NewModel(context.Background(), Config{Launch: func(context.Context, string, []string) (string, error) {
		return "MANUAL: sdlc run --reference DEMO-42", fmt.Errorf("background terminal unavailable")
	}})
	m.review = []string{"run", "--reference", "DEMO-42"}
	cmd := m.start()
	m.Update(cmd())
	if m.busy || len(m.review) == 0 || !strings.Contains(m.body, "MANUAL:") {
		t.Fatalf("failed launch lost review: %#v %q", m.review, m.body)
	}
}
func TestCompletionDoesNotTurnQuotedInputValueIntoFlag(t *testing.T) {
	m := NewModel(context.Background(), Config{Commands: []Command{{Name: "run", Flags: []Flag{{Name: "--dry-run"}}}}})
	m.draft = []rune(`/run --input "--d"`)
	m.complete()
	if len(m.suggestions) != 0 {
		t.Fatal("literal input value received command flag completion")
	}
}
func TestCtrlCAfterStartRetainsAsyncLaunchReceipt(t *testing.T) {
	release := make(chan struct{})
	responses := make(chan tea.Msg, 1)
	m := NewModel(context.Background(), Config{Launch: func(ctx context.Context, _ string, _ []string) (string, error) {
		<-release
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return `{"id":"example-launch","state":"started"}`, nil
	}})
	m.review = []string{"run", "--ticket", "01-count.md"}
	cmd := m.start()
	go func() { responses <- cmd() }()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.busy {
		t.Fatal("pending launch was cancelled")
	}
	close(release)
	m.Update(<-responses)
	if !m.monitor || !strings.Contains(m.launchNotice, "example-launch") {
		t.Fatalf("lost independent launch receipt: monitor=%v notice=%q", m.monitor, m.launchNotice)
	}
}
func TestAmbiguousLaunchErrorStillMonitorsReceipt(t *testing.T) {
	m := NewModel(context.Background(), Config{Launch: func(context.Context, string, []string) (string, error) {
		return `{"id":"example-launch","state":"unknown","manual_command":"manual instruction"}`, fmt.Errorf("acknowledgment unavailable")
	}})
	m.review = []string{"run"}
	m.Update(m.start()())
	if !m.monitor || len(m.review) != 0 || !strings.Contains(m.launchNotice, "manual instruction") {
		t.Fatal("ambiguous dispatched launch became retryable failure")
	}
}
func TestPromptAndPastedDraftCannotEmitTerminalControls(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/name\x1b[2J\n", Branch: "main\x1b]52;c;secret\x07"})
	m.draft = []rune("/help\x1b]52;c;secret\x07\ninjected")
	m.cursor = len(m.draft)
	view := m.View()
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]52") || strings.ContainsRune(view, '\a') {
		t.Fatalf("terminal controls escaped into view: %q", view)
	}
	if len(strings.Split(view, "\n")) != m.height {
		t.Fatal("inline controls changed view height")
	}
	m.draft = nil
	m.cursor = 0
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/help\x1b\n")})
	if string(m.draft) != "/help" {
		t.Fatalf("pasted controls retained: %q", m.draft)
	}
}

func TestSelectedReferenceIgnoresLiteralInputFlagAndLocatorValues(t *testing.T) {
	for _, input := range []string{"--reference", "@notes"} {
		t.Run(input, func(t *testing.T) {
			var received []string
			m := NewModel(context.Background(), Config{Read: func(_ context.Context, _ string, args []string) (string, error) {
				received = append([]string(nil), args...)
				return "offline plan", nil
			}})
			m.reference = "DEMO-42"
			cmd := enter(m, "/run --input '"+input+"' --ticket 01-count-items.md")
			m.Update(cmd())
			want := []string{"run", "--input", input, "--ticket", "01-count-items.md", "--reference", "DEMO-42"}
			if !reflect.DeepEqual(received, want) {
				t.Fatalf("literal input changed selected reference: got %#v want %#v", received, want)
			}
		})
	}
}
func TestSelectedReferenceDoesNotOverrideActualReferenceOrTicketLocator(t *testing.T) {
	for _, draft := range []string{"/run --input @notes --ticket @OTHER/01-count-items.md", "/run --input '--reference' --ticket 01-count-items.md --reference OTHER"} {
		t.Run(draft, func(t *testing.T) {
			var received []string
			m := NewModel(context.Background(), Config{Read: func(_ context.Context, _ string, args []string) (string, error) {
				received = append([]string(nil), args...)
				return "offline plan", nil
			}})
			m.reference = "DEMO-42"
			cmd := enter(m, draft)
			m.Update(cmd())
			if strings.Contains(strings.Join(received, " "), "DEMO-42") {
				t.Fatalf("implicit reference overrode explicit target: %#v", received)
			}
		})
	}
}

func TestRobbyRussellArrowReflectsCommandFailureAndSuccess(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project", Commands: []Command{{Name: "help", Usage: "/help"}}})
	enter(m, `/help "unfinished`)
	if !strings.Contains(m.View(), "\x1b[1;31m➜") {
		t.Fatal("parse failure did not turn arrow bold red")
	}
	enter(m, "/help ")
	if !strings.Contains(m.View(), "\x1b[1;32m➜") {
		t.Fatal("successful command did not restore bold green arrow")
	}
	m.Update(result{generation: m.generation, kind: "read", err: fmt.Errorf("read failed")})
	if !strings.Contains(m.View(), "\x1b[1;31m➜") {
		t.Fatal("read failure did not turn arrow red")
	}
	m.Update(result{generation: m.generation, kind: "read", text: "read succeeded"})
	if !strings.Contains(m.View(), "\x1b[1;32m➜") {
		t.Fatal("read success did not restore green arrow")
	}
	m.Update(completed{err: fmt.Errorf("exit status 2")})
	if !strings.Contains(m.View(), "\x1b[1;31m➜") {
		t.Fatal("native failure did not turn arrow red")
	}
	m.Update(completed{})
	if !strings.Contains(m.View(), "\x1b[1;32m➜") {
		t.Fatal("native success did not restore green arrow")
	}
}
