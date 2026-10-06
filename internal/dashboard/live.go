package dashboard

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tjpeel/sdlc/internal/runstatus"
)

// LiveSnapshot retains all scoped rows so notifications and paging use the same
// snapshot. The loader observes notifications before selecting or paging rows.
type LiveSnapshot struct {
	Views   []runstatus.View
	Now     time.Time
	Warning string
}
type LiveConfig struct {
	Load                   func() (LiveSnapshot, error)
	Interval               time.Duration
	Page                   int
	Run                    string
	Logs, Attention, Color bool
}
type liveResult struct {
	snapshot LiveSnapshot
	err      error
}
type liveTick struct{}
type LiveModel struct {
	config                      LiveConfig
	snapshot                    LiveSnapshot
	width, height, page, scroll int
	body, frozen                string
	paused                      bool
	Err                         error
}

func NewLiveModel(c LiveConfig) *LiveModel {
	return &LiveModel{config: c, width: 80, height: 24, page: max(1, c.Page), body: "Loading dashboard…"}
}
func (m *LiveModel) load() tea.Cmd {
	return func() tea.Msg { s, e := m.config.Load(); return liveResult{s, e} }
}
func (m *LiveModel) Init() tea.Cmd { return m.load() }
func (m *LiveModel) tick() tea.Cmd {
	return tea.Tick(m.config.Interval, func(time.Time) tea.Msg { return liveTick{} })
}
func (m *LiveModel) render() {
	var b bytes.Buffer
	s := m.snapshot
	if m.config.Run != "" {
		v, e := Select(s.Views, m.config.Run)
		if e != nil {
			m.Err = e
			return
		}
		m.Err = Detail(&b, v, s.Now, m.config.Logs)
	} else if m.config.Attention && len(s.Views) == 0 {
		fmt.Fprintln(&b, "No runs require human attention in this scope.")
	} else {
		_, p := Page(s.Views, m.page)
		m.page = p.Page
		m.Err = ListPage(&b, s.Views, s.Now, m.page)
	}
	if s.Warning != "" {
		fmt.Fprintln(&b, s.Warning)
	}
	m.body = StyleSnapshot(strings.TrimSuffix(b.String(), "\n"), m.config.Color)
}
func (m *LiveModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case liveResult:
		if msg.err != nil {
			m.Err = msg.err
			if m.paused {
				return m, nil
			}
			return m, tea.Quit
		}
		m.snapshot = msg.snapshot
		m.render()
		if m.Err != nil {
			if m.paused {
				return m, nil
			}
			return m, tea.Quit
		}
		return m, m.tick()
	case liveTick:
		if m.Err != nil {
			return m, nil
		}
		return m, m.load()
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
	case tea.MouseMsg:
		if m.paused {
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			return m, m.pause()
		}
		if msg.Button == tea.MouseButtonWheelUp {
			m.scroll -= 3
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.scroll += 3
		}
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" || key == "q" {
			return m, tea.Quit
		}
		if m.paused {
			if key == "esc" || key == "f2" {
				m.paused = false
				m.frozen = ""
				if m.Err != nil {
					return m, tea.Quit
				}
				return m, tea.EnableMouseCellMotion
			}
			return m, nil
		}
		switch key {
		case "f2", " ":
			return m, m.pause()
		case "n", "p":
			if m.config.Run == "" {
				if key == "n" {
					m.page++
				} else {
					m.page--
				}
				m.scroll = 0
				m.render()
			}
		case "up":
			m.scroll--
		case "down":
			m.scroll++
		case "pgup":
			m.scroll -= max(1, m.height-1)
		case "pgdown":
			m.scroll += max(1, m.height-1)
		case "home":
			m.scroll = 0
		case "end":
			m.scroll = max(0, len(Wrap(m.body, m.width))-max(0, m.height-1))
		}
	}
	if !m.paused {
		m.scroll = max(0, min(m.scroll, max(0, len(Wrap(m.body, m.width))-max(0, m.height-1))))
	}
	return m, nil
}
func (m *LiveModel) pause() tea.Cmd {
	lines := Wrap(m.body, m.width)
	available := max(0, m.height-1)
	start := max(0, min(m.scroll, max(0, len(lines)-available)))
	m.frozen = strings.Join(lines[start:min(len(lines), start+available)], "\n")
	m.paused = true
	return tea.DisableMouse
}
func (m *LiveModel) View() string {
	body := m.body
	if m.paused {
		body = m.frozen
	}
	lines := Wrap(body, m.width)
	available := max(0, m.height-1)
	start := max(0, min(m.scroll, max(0, len(lines)-available)))
	if m.paused {
		start = 0
	}
	end := min(len(lines), start+available)
	visible := append([]string(nil), lines[start:end]...)
	for len(visible) < available {
		visible = append(visible, "")
	}
	footer := "n/p page · ↑↓/PgUp/PgDn scroll · F2 copy · q quit"
	if m.paused {
		footer = "Paused · Esc/F2 resume · Drag and copy · Ctrl+C quit"
	}
	visible = append(visible, ansi.Truncate(footer, m.width, ""))
	return strings.Join(visible, "\n")
}
