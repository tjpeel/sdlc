package shell

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tjpeel/sdlc/internal/runprogress"
)

func TestSelectionFreezesDashboardWhileRefreshCompletes(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	m.body, m.monitor, m.busy = "snapshot one", true, true
	m.busyKind = "dashboard-refresh"
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(result{generation: m.generation, kind: "dashboard", text: "snapshot two"})
	if m.body != "snapshot two" || !m.monitor {
		t.Fatal("selection stopped background dashboard updates")
	}
	if m.View() != frozen {
		t.Fatal("dashboard refresh replaced the selected frame")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.View() == frozen {
		t.Fatal("leaving selection did not reveal the latest dashboard")
	}
}

func requireMouseCommand(t *testing.T, cmd tea.Cmd, want tea.Msg) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing mouse mode command")
	}
	if got := cmd(); reflect.TypeOf(got) != reflect.TypeOf(want) {
		t.Fatalf("mouse command = %T, want %T", got, want)
	}
}

func TestSelectionKeepsDraftAndConsumesInput(t *testing.T) {
	for _, mode := range []string{"command", "answer", "resume"} {
		t.Run(mode, func(t *testing.T) {
			m := NewModel(context.Background(), Config{Root: "/example/project"})
			if mode != "command" {
				m.action = &RunAction{ID: "example-run"}
				m.answerMode = mode == "answer"
			}
			m.draft, m.cursor = []rune("draft\nanswer"), 3
			m.review, m.monitor = []string{"run"}, true
			cancelled := false
			m.cancel = func() { cancelled = true }
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF2})
			requireMouseCommand(t, cmd, tea.DisableMouse())
			frozen := m.View()
			if !strings.Contains(frozen, "F2/Esc resume") || !strings.Contains(frozen, "Drag to select") {
				t.Fatalf("missing selection instructions: %q", frozen)
			}
			for _, msg := range []tea.Msg{
				tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hidden")},
				tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyCtrlS},
				tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyLeft},
				tea.KeyMsg{Type: tea.KeyPgUp},
				tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress},
			} {
				if _, cmd := m.Update(msg); cmd != nil {
					t.Fatalf("selection dispatched a command for %T", msg)
				}
			}
			if string(m.draft) != "draft\nanswer" || m.cursor != 3 || m.View() != frozen {
				t.Fatal("selection input changed the draft, cursor or visible frame")
			}
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			requireMouseCommand(t, cmd, tea.EnableMouseCellMotion())
			if !m.monitor || cancelled || len(m.review) == 0 || (mode != "command" && m.action == nil) {
				t.Fatal("leaving selection cancelled work or discarded the action")
			}
			if string(m.draft) != "draft\nanswer" || m.cursor != 3 {
				t.Fatal("leaving selection lost the draft")
			}
		})
	}
}

func TestSelectionCollectsProgressAndF2RevealsIt(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	m.progressView, m.monitor = true, true
	m.progressRuns = make(map[string]bool)
	m.body = "earlier progress"
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	_, cmd := m.Update(progressResult{generation: m.generation, batch: runprogress.Batch{
		Cursor: "next", Events: []runprogress.Event{{RunID: "example-run", Text: "later progress"}},
	}})
	if cmd == nil || m.progressCursor != "next" || !strings.Contains(m.body, "later progress") || !m.monitor {
		t.Fatal("selection stopped collecting progress")
	}
	if m.View() != frozen {
		t.Fatal("new progress changed the selected frame")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	requireMouseCommand(t, cmd, tea.EnableMouseCellMotion())
	if !strings.Contains(m.View(), "later progress") {
		t.Fatal("F2 did not reveal collected progress")
	}
}

func TestSelectionResizeAndTerminalRestore(t *testing.T) {
	m := NewModel(context.Background(), Config{Root: "/example/project"})
	for _, msg := range []tea.Msg{completed{}, tea.ResumeMsg{}} {
		_, cmd := m.Update(msg)
		requireMouseCommand(t, cmd, tea.EnableMouseCellMotion())
		m.Update(tea.KeyMsg{Type: tea.KeyF2})
		_, cmd = m.Update(msg)
		requireMouseCommand(t, cmd, tea.DisableMouse())
		_, cmd = m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
		requireMouseCommand(t, cmd, tea.EnableMouseCellMotion())
		if m.selectionView != "" || m.width != 40 || m.height != 8 || len(strings.Split(m.View(), "\n")) > 8 {
			t.Fatal("resize retained the frozen viewport")
		}
	}
}
