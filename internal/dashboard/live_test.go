package dashboard

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func assertGeometry(t *testing.T, text string, width, height int) {
	t.Helper()
	rows := strings.Split(text, "\n")
	if len(rows) != height {
		t.Fatalf("got %d rows, want %d", len(rows), height)
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > width || strings.ContainsRune(row, '\t') {
			t.Fatalf("physical overflow: %q", row)
		}
	}
}
func TestLiveSelectionCollectsAndReflowsWithoutResuming(t *testing.T) {
	m := NewLiveModel(LiveConfig{Interval: time.Second})
	m.body = "original\t界 status\n" + strings.Repeat("history\n", 20)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	_, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if cmd == nil || fmt.Sprintf("%T", cmd()) != fmt.Sprintf("%T", tea.DisableMouse()) {
		t.Fatal("copy did not release mouse")
	}
	frozen := m.View()
	m.Update(liveResult{snapshot: LiveSnapshot{Now: time.Now(), Warning: "latest status"}})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	if m.View() != frozen {
		t.Fatal("refresh changed selection")
	}
	m.Update(tea.WindowSizeMsg{Width: 18, Height: 5})
	if !m.paused || !strings.Contains(m.View(), "original") || strings.Contains(m.View(), "latest status") {
		t.Fatal("resize lost frozen content")
	}
	assertGeometry(t, m.View(), 18, 5)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.paused || !strings.Contains(m.body, "latest status") {
		t.Fatal("resume lost latest snapshot")
	}
}
func TestLivePageDetailAndFailure(t *testing.T) {
	var rows []runstatus.View
	for i := 0; i < 12; i++ {
		rows = append(rows, runstatus.View{ID: fmt.Sprintf("%024d", i), Available: true, State: "ready"})
	}
	m := NewLiveModel(LiveConfig{Interval: time.Second})
	m.Update(liveResult{snapshot: LiveSnapshot{Views: rows, Now: time.Now()}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.page != 2 || !strings.Contains(m.body, "Page 2/2") {
		t.Fatal("next page failed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.page != 1 {
		t.Fatal("previous page failed")
	}
	m.Update(tea.WindowSizeMsg{Width: 25, Height: 4})
	assertGeometry(t, m.View(), 25, 4)
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	assertGeometry(t, m.View(), 25, 4)
	detail := NewLiveModel(LiveConfig{Run: rows[0].ID})
	detail.Update(liveResult{snapshot: LiveSnapshot{Views: rows, Now: time.Now(), Warning: "notification warning"}})
	if detail.Err != nil || !strings.Contains(detail.body, rows[0].ID) || !strings.Contains(detail.body, "notification warning") {
		t.Fatal("detail or warning missing")
	}
	_, cmd := m.Update(liveResult{err: errors.New("cannot read status")})
	if cmd == nil || m.Err == nil {
		t.Fatal("load failure not surfaced")
	}
}
func TestDashboardPresentationIsBoundedAndRestrained(t *testing.T) {
	text := "SDLC overview\n== Needs human input ==\n     Question: answer me\n== Ready for review ==\n     Next: review me\n== Recorded runs ==\nfinished\t界"
	plain := StyleSnapshot(text, false)
	if plain != text {
		t.Fatal("disabled color changed text")
	}
	styled := StyleSnapshot(text, true)
	if !strings.Contains(styled, "\x1b[90mfinished") || strings.Contains(styled, "\x1b[90m     Next: review me") || strings.Contains(styled, "\x1b[90m     Question:") {
		t.Fatal("attention was muted")
	}
	for _, row := range Wrap(styled, 9) {
		if ansi.StringWidth(row) > 9 || strings.ContainsRune(row, '\t') {
			t.Fatalf("row overflow %q", row)
		}
		if strings.Contains(row, "\x1b[") && !strings.HasSuffix(row, "\x1b[0m") {
			t.Fatalf("style leaks between rows %q", row)
		}
	}
}

func TestPausedDashboardDefersLoadAndSelectionErrors(t *testing.T) {
	for _, kind := range []string{"load", "selection"} {
		t.Run(kind, func(t *testing.T) {
			row := runstatus.View{ID: "0123456789abcdef01234567", Available: true, State: "ready"}
			m := NewLiveModel(LiveConfig{Run: row.ID, Interval: time.Second})
			m.Update(liveResult{snapshot: LiveSnapshot{Views: []runstatus.View{row}, Now: time.Now()}})
			m.Update(tea.KeyMsg{Type: tea.KeyF2})
			frozen := m.View()
			next := liveResult{snapshot: LiveSnapshot{Now: time.Now()}}
			if kind == "load" {
				next.err = errors.New("cannot read registry")
			}
			_, cmd := m.Update(next)
			if cmd != nil || m.Err == nil || m.View() != frozen {
				t.Fatal("background error interrupted selection")
			}
			_, cmd = m.Update(liveTick{})
			if cmd != nil {
				t.Fatal("polling continued after failure")
			}
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if cmd == nil || fmt.Sprintf("%T", cmd()) != fmt.Sprintf("%T", tea.Quit()) || m.Err == nil {
				t.Fatal("resume did not report saved error and close")
			}
		})
	}
}

func TestPausedDashboardResizeKeepsVisibleHistoryAnchor(t *testing.T) {
	m := NewLiveModel(LiveConfig{})
	var history strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&history, "row-%03d %s\n", i, strings.Repeat("detail", 6))
	}
	m.body = strings.TrimSuffix(history.String(), "\n")
	m.width, m.height, m.scroll = 80, 10, 42
	anchor := strings.Fields(strings.Split(m.View(), "\n")[0])[0]
	m.pause()
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	if !strings.HasPrefix(m.View(), anchor) || strings.Contains(m.View(), "row-072") {
		t.Fatal("narrow resize substituted unrelated history")
	}
	assertGeometry(t, m.View(), 20, 10)
}

func TestCompletedRunSummaryMutedButReviewActionReadable(t *testing.T) {
	row := runstatus.View{ID: "0123456789abcdef01234567", Available: true, Stopped: true, NeedsAttention: true, State: "ready", Root: "/example/project", Ticket: "01-example.md", Model: "example-model", PR: workrun.Publication{URL: "https://github.com/example/project/pull/1"}}
	var b bytes.Buffer
	if err := ListPage(&b, []runstatus.View{row}, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	styled := StyleSnapshot(b.String(), true)
	summary, action, metadata := false, false, false
	for _, line := range strings.Split(styled, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, row.ID[:12]) && strings.Contains(plain, "ready") {
			summary = strings.HasPrefix(line, "\x1b[90m")
		}
		if strings.Contains(plain, "example-model") {
			metadata = strings.HasPrefix(line, "\x1b[90m")
		}
		if strings.Contains(plain, "Next: Review PR:") {
			action = !strings.HasPrefix(line, "\x1b[90m")
		}
	}
	if !summary || !metadata || !action {
		t.Fatal("completed run did not separate muted history from review action")
	}
}
