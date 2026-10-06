package shell

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func answerConfig() Config {
	return Config{Root: "/example/selected", ResolveRun: func(_ context.Context, root, id string) (RunAction, error) {
		return RunAction{ID: "recorded-id", Root: "/example/recorded", State: "waiting_for_human", Checkpoint: "requirements", Questions: []string{"Which format?", "What delimiter?"}}, nil
	}}
}
func TestAnswerEditorPreservesRawTextAndDispatchesOnce(t *testing.T) {
	c := answerConfig()
	calls := 0
	var answer string
	var received RunAction
	var monitoring []string
	c.RespondRun = func(_ context.Context, a RunAction, text string) (string, error) {
		calls++
		received = a
		answer = text
		return `{"id":"terminal-receipt"}`, nil
	}
	c.Read = func(_ context.Context, root string, args []string) (string, error) {
		if root != c.Root {
			t.Fatal("selected root changed")
		}
		monitoring = args
		return "snapshot", nil
	}
	m := NewModel(context.Background(), c)
	cmd := enter(m, "/answer requested-id")
	m.Update(cmd())
	if !strings.Contains(m.View(), "Which format?") || !strings.Contains(m.View(), "/example/recorded") {
		t.Fatal("recorded context hidden")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`/exit "quoted" --flag @literal`)})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second line")})
	raw := `/exit "quoted" --flag @literal` + "\nsecond line"
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
	m.Update(result{generation: m.generation, kind: "dashboard", text: "refreshed"})
	if string(m.draft) != raw || !strings.Contains(m.View(), "What delimiter?") {
		t.Fatal("refresh or resize lost editor context")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	_, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if duplicate != nil {
		t.Fatal("duplicate submission")
	}
	_, next := m.Update(cmd())
	m.Update(next())
	if calls != 1 || answer != raw || received.Root != "/example/recorded" || received.ID != "recorded-id" {
		t.Fatalf("calls=%d answer=%q action=%+v", calls, answer, received)
	}
	if !reflect.DeepEqual(monitoring, []string{"dashboard", "--run", "recorded-id", "--scope", "all", "--once"}) {
		t.Fatalf("monitor: %#v", monitoring)
	}
}
func TestAnswerFailureRetainsDraftAndCancellationIgnoresStaleResult(t *testing.T) {
	c := answerConfig()
	c.RespondRun = func(context.Context, RunAction, string) (string, error) { return "", fmt.Errorf("offline failure") }
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("literal answer")})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if m.action == nil || string(m.draft) != "literal answer" || !strings.Contains(m.View(), "offline failure") {
		t.Fatal("failed answer lost draft")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || m.action != nil {
		t.Fatal("Ctrl+C exited instead of discarding editor")
	}
	cmd = enter(m, "/answer id")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(cmd())
	if m.action != nil {
		t.Fatal("stale resolver opened editor")
	}
}
func TestAnswerTargetsSelectedRunAndRejectsOtherStates(t *testing.T) {
	c := answerConfig()
	var id string
	c.ResolveRun = func(_ context.Context, _ string, target string) (RunAction, error) {
		id = target
		return RunAction{ID: target, State: "finished"}, nil
	}
	m := NewModel(context.Background(), c)
	if enter(m, "/answer") != nil || !strings.Contains(m.body, "/dashboard --run") {
		t.Fatal("missing target accepted")
	}
	m.monitorArgs = []string{"dashboard", "--run", "unique-prefix"}
	m.Update(enter(m, "/answer")())
	if id != "unique-prefix" || m.action != nil || !strings.Contains(m.body, "waiting_for_human") {
		t.Fatal("selection or state validation failed")
	}
}
func TestAnswerBoundsAndWhitespace(t *testing.T) {
	m := NewModel(context.Background(), answerConfig())
	m.Update(enter(m, "/answer id")())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" \t")})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil {
		t.Fatal("blank answer dispatched")
	}
	m.draft = nil
	m.cursor = 0
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("é", maxAnswerBytes/2))})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(string(m.draft)) != maxAnswerBytes {
		t.Fatal("bound changed draft")
	}
}
func TestPlainAnswerEscapingAndRemainingInput(t *testing.T) {
	c := answerConfig()
	var out bytes.Buffer
	var answer string
	calls := 0
	c.Input = strings.NewReader("/answer id\n/exit \"quote\" --flag\n..\n//cancel\nsecond line\n.\n/help\n/exit\n")
	c.Output = &out
	c.Commands = []Command{{Name: "help", Usage: "/help", Summary: "Help marker"}}
	c.RespondRun = func(_ context.Context, a RunAction, text string) (string, error) {
		calls++
		answer = text
		return "accepted", nil
	}
	c.Read = func(context.Context, string, []string) (string, error) { return "one snapshot", nil }
	if err := RunPlain(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || answer != "/exit \"quote\" --flag\n.\n/cancel\nsecond line" {
		t.Fatalf("calls=%d answer=%q", calls, answer)
	}
	for _, text := range []string{"Which format?", "What delimiter?", "Help marker", "one snapshot"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q: %s", text, out.String())
		}
	}
}
func TestPlainAnswerCancellationEOFAndRetry(t *testing.T) {
	for _, suffix := range []string{"/cancel\n/exit\n", "unfinished"} {
		c := answerConfig()
		var out bytes.Buffer
		c.Input = strings.NewReader("/answer id\nanswer\n" + suffix)
		c.Output = &out
		c.RespondRun = func(context.Context, RunAction, string) (string, error) {
			t.Fatal("cancelled answer submitted")
			return "", nil
		}
		if err := RunPlain(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	c := answerConfig()
	var out bytes.Buffer
	calls := 0
	c.Input = strings.NewReader("/answer id\nraw text\n.\n.\n/exit\n")
	c.Output = &out
	c.RespondRun = func(_ context.Context, _ RunAction, text string) (string, error) {
		calls++
		if text != "raw text" {
			t.Fatal("retry lost answer")
		}
		if calls == 1 {
			return "", fmt.Errorf("temporary")
		}
		return "accepted", nil
	}
	c.Read = func(context.Context, string, []string) (string, error) { return "snapshot", nil }
	if err := RunPlain(context.Background(), c); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestResumeNeedsExplicitConfirmationRichAndPlain(t *testing.T) {
	c := answerConfig()
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "recorded-id", Root: "/example/recorded", State: "awaiting_reviewer", Questions: []string{"stale question"}}, nil
	}
	calls := 0
	c.ResumeRun = func(_ context.Context, a RunAction) (string, error) {
		calls++
		if a.ID != "recorded-id" {
			t.Fatal("wrong run")
		}
		return "accepted", nil
	}
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/resume id")())
	if calls != 0 || !strings.Contains(m.View(), "awaiting_reviewer") || strings.Contains(m.View(), "stale question") {
		t.Fatal("resume skipped confirmation")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if calls != 1 {
		t.Fatal("resume did not dispatch")
	}
	var out bytes.Buffer
	c.Input = strings.NewReader("/resume id\n/start\n/exit\n")
	c.Output = &out
	c.Read = func(context.Context, string, []string) (string, error) { return "snapshot", nil }
	if err := RunPlain(context.Background(), c); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v output=%s", calls, err, out.String())
	}
}

func TestDispatchedAnswerCancellationDoesNotCancelCallback(t *testing.T) {
	c := answerConfig()
	c.RespondRun = func(ctx context.Context, _ RunAction, _ string) (string, error) {
		if ctx.Err() != nil {
			t.Fatal("dispatched action cancelled")
		}
		return `{"id":"terminal","state":"unknown"}`, fmt.Errorf("acknowledgment unavailable")
	}
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("answer")})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(cmd())
	if m.action != nil || !m.monitor || !reflect.DeepEqual(m.monitorArgs, []string{"dashboard", "--run", "recorded-id", "--scope", "all"}) {
		t.Fatal("dispatched action became retryable or lost monitoring")
	}
}
func TestPlainAnswerRejectsOversizedAndInvalidUTF8Lines(t *testing.T) {
	c := answerConfig()
	var out bytes.Buffer
	c.Output = &out
	c.Input = strings.NewReader("/answer id\n" + strings.Repeat("x", maxAnswerBytes+2) + "\n\xff\nvalid\n.\n/exit\n")
	c.RespondRun = func(_ context.Context, _ RunAction, text string) (string, error) {
		if text != "valid" {
			t.Fatalf("rejected input retained: %q", text)
		}
		return "accepted", nil
	}
	c.Read = func(context.Context, string, []string) (string, error) { return "snapshot", nil }
	if err := RunPlain(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "UTF-8") || !strings.Contains(out.String(), "64 KiB") {
		t.Fatal("input validation not reported")
	}
}
func TestAttentionShortcutKeepsMonitorFilter(t *testing.T) {
	var args []string
	m := NewModel(context.Background(), Config{Read: func(_ context.Context, _ string, a []string) (string, error) {
		args = a
		return "attention snapshot", nil
	}})
	m.Update(enter(m, "/attention --scope all")())
	if !reflect.DeepEqual(args, []string{"dashboard", "--attention", "--scope", "all", "--once"}) {
		t.Fatalf("args=%#v", args)
	}
}

func TestPlainNewPlanReplacesPendingResume(t *testing.T) {
	c := answerConfig()
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "recorded-id", State: "awaiting_reviewer"}, nil
	}
	var out bytes.Buffer
	launches := 0
	c.Output = &out
	c.Input = strings.NewReader("/resume id\n/run --ticket one.md\n/start\n/exit\n")
	c.ResumeRun = func(context.Context, RunAction) (string, error) {
		t.Fatal("old resume took precedence over new plan")
		return "", nil
	}
	c.Launch = func(context.Context, string, []string) (string, error) { launches++; return "accepted", nil }
	c.Read = func(context.Context, string, []string) (string, error) { return "plan", nil }
	if err := RunPlain(context.Background(), c); err != nil || launches != 1 {
		t.Fatalf("launches=%d err=%v", launches, err)
	}
}

func TestResumeWaitingForHumanPointsToAnswer(t *testing.T) {
	m := NewModel(context.Background(), answerConfig())
	m.Update(enter(m, "/resume id")())
	if m.action != nil || !strings.Contains(m.body, "/answer recorded-id") {
		t.Fatal("waiting run offered bare resume")
	}
}

func TestAnswerContextAndDraftStripTerminalAndFormatControls(t *testing.T) {
	c := answerConfig()
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "id", Root: "/example/recorded", State: "waiting_for_human", Questions: []string{"Format?\x1b[2J\u202e"}}, nil
	}
	var raw string
	c.RespondRun = func(_ context.Context, _ RunAction, text string) (string, error) { raw = text; return "accepted", nil }
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("raw\u202eanswer")})
	for _, view := range []string{m.actionContext(), m.View()} {
		if strings.ContainsRune(view, '\u202e') || strings.Contains(view, "\x1b[2J") {
			t.Fatalf("unsafe rendered text: %q", view)
		}
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if raw != "raw\u202eanswer" {
		t.Fatal("sanitization altered submitted answer")
	}
}

func TestLongQuestionsCanPageWithoutHidingAnswerOrControls(t *testing.T) {
	c := answerConfig()
	questions := []string{}
	for i := 0; i < 12; i++ {
		questions = append(questions, fmt.Sprintf("Question %02d: %s", i, strings.Repeat("context ", 15)))
	}
	questions = append(questions, "FINAL QUESTION: choose the delimiter")
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "recorded-id", Root: "/example/recorded", State: "waiting_for_human", Questions: questions}, nil
	}
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 8})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft stays")})
	assertEditor := func() {
		t.Helper()
		view := m.View()
		if !strings.Contains(view, "draft stays") || !strings.Contains(view, "\x1b[7m") || !strings.Contains(view, "Ctrl+S") || !strings.Contains(view, "PgUp/PgDn") {
			t.Fatalf("editor controls hidden: %q", view)
		}
		if len(strings.Split(view, "\n")) != m.height {
			t.Fatal("wrong viewport height")
		}
	}
	assertEditor()
	for i := 0; i < 100; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !strings.Contains(m.View(), "FINAL QUESTION") {
		t.Fatalf("last question inaccessible: %q", m.View())
	}
	assertEditor()
	m.Update(tea.WindowSizeMsg{Width: 46, Height: 6})
	assertEditor()
	if string(m.draft) != "draft stays" {
		t.Fatal("question scroll changed draft")
	}
	for i := 0; i < 100; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if !strings.Contains(m.View(), "Run recorded-id") {
		t.Fatal("cannot return to context start")
	}
}
func TestMultilineAnswerViewportFollowsCursor(t *testing.T) {
	m := NewModel(context.Background(), answerConfig())
	m.Update(enter(m, "/answer id")())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
	raw := "FIRST LINE\nsecond\nthird\nfourth\nfifth\nLAST LINE"
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(raw)})
	if !strings.Contains(m.View(), "LAST LINE") {
		t.Fatal("editor did not follow end cursor")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if !strings.Contains(ansi.Strip(m.View()), "FIRST LINE") || !strings.Contains(m.View(), "\x1b[7mF") {
		t.Fatal("editor did not follow start cursor")
	}
	if string(m.draft) != raw {
		t.Fatal("moving cursor changed raw text")
	}
}

func TestEveryQuestionIsReachableInFourRowTerminal(t *testing.T) {
	c := answerConfig()
	questions := []string{"FIRST QUESTION", "SECOND QUESTION", "THIRD QUESTION"}
	c.ResolveRun = func(context.Context, string, string) (RunAction, error) {
		return RunAction{ID: "recorded-id", State: "waiting_for_human", Questions: questions}, nil
	}
	m := NewModel(context.Background(), c)
	m.Update(enter(m, "/answer id")())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 4})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("reply")})
	var views strings.Builder
	for i := 0; i < 6; i++ {
		view := m.View()
		if !strings.Contains(view, "reply") || !strings.Contains(view, "Ctrl+S") {
			t.Fatal("question paging hid the answer or submit control")
		}
		views.WriteString(view)
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	for _, question := range questions {
		if !strings.Contains(views.String(), question) {
			t.Fatalf("question skipped by paging: %s", question)
		}
	}
}

func TestTypedCommandAndAnswerKeepSpaceKeyEvents(t *testing.T) {
	c := answerConfig()
	var requested, reply string
	c.ResolveRun = func(_ context.Context, _ string, id string) (RunAction, error) {
		requested = id
		return RunAction{ID: id, State: "waiting_for_human", Questions: []string{"Which status?"}}, nil
	}
	c.RespondRun = func(_ context.Context, _ RunAction, text string) (string, error) {
		reply = text
		return "accepted", nil
	}
	m := NewModel(context.Background(), c)
	typeText := func(text string) {
		for _, r := range text {
			if r == ' ' {
				m.Update(tea.KeyMsg{Type: tea.KeySpace})
			} else {
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
		}
	}
	typeText("/answer abcdef")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("typed answer command was not dispatched: %s", m.body)
	}
	m.Update(cmd())
	if requested != "abcdef" {
		t.Fatalf("space did not separate run ID: %q", requested)
	}
	typeText("Return 404 for missing records.")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("typed answer was not submitted")
	}
	m.Update(cmd())
	if reply != "Return 404 for missing records." {
		t.Fatalf("space keys changed free text: %q", reply)
	}
}
