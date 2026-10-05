package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type result struct {
	generation         uint64
	kind               string
	args               []string
	text, root, branch string
	dirty              bool
	err                error
}
type poll struct{}
type completed struct{ err error }

// Model owns view state only. Resize and background messages do not replace the draft.
type Model struct {
	busyKind, launchNotice                   string
	lastError                                bool
	config                                   Config
	ctx                                      context.Context
	cancel                                   context.CancelFunc
	root, branch, reference, scope, body     string
	dirty                                    bool
	draft                                    []rune
	cursor, width, height, scroll, selection int
	suggestions                              []Suggestion
	dismissed, busy, monitor                 bool
	generation                               uint64
	draftGeneration                          uint64
	monitorArgs                              []string
	review                                   []string
}

func NewModel(ctx context.Context, c Config) *Model {
	return &Model{config: c, ctx: ctx, root: c.Root, branch: c.Branch, dirty: c.Dirty, scope: "project", width: 80, height: 24, body: "SDLC " + safe(c.Version) + "\nType / for commands. Nothing starts automatically."}
}
func (m *Model) Init() tea.Cmd { return nil }
func (m *Model) command(args []string) (Command, bool) {
	var found Command
	ok := false
	for _, c := range m.config.Commands {
		words := strings.Fields(c.Name)
		if len(words) > len(args) {
			continue
		}
		if strings.Join(args[:len(words)], " ") == c.Name && (!ok || len(c.Name) > len(found.Name)) {
			found = c
			ok = true
		}
	}
	return found, ok
}
func (m *Model) help(query string) string {
	var b strings.Builder
	for _, c := range m.config.Commands {
		if query != "" && !strings.Contains(c.Name+" "+c.Summary, query) {
			continue
		}
		fmt.Fprintf(&b, "%s\n  %s\n", c.Usage, c.Summary)
		for _, f := range c.Flags {
			fmt.Fprintf(&b, "  %s  %s\n", f.Name, f.Summary)
		}
	}
	if b.Len() == 0 {
		return "No matching command."
	}
	return b.String()
}
func (m *Model) complete() {
	m.suggestions = nil
	m.selection = 0
	if m.dismissed {
		return
	}
	draft := string(m.draft)
	if strings.HasPrefix(draft, "/") {
		for _, c := range m.config.Commands {
			full := "/" + c.Name
			if strings.HasPrefix(full, draft) {
				m.suggestions = append(m.suggestions, Suggestion{c.Name, full, c.Summary})
			}
		}
	}
	if args, err := Parse(draft); err == nil && strings.HasSuffix(draft, " ") == false {
		if c, ok := m.command(args); ok {
			last := args[len(args)-1]
			if strings.HasPrefix(last, "--") && optionPosition(args, len(args)-1) {
				position := strings.LastIndex(draft, last)
				if position < 0 {
					return
				}
				prefix := draft[:position]
				for _, f := range c.Flags {
					if strings.HasPrefix(f.Name, last) {
						m.suggestions = append(m.suggestions, Suggestion{f.Name, prefix + f.Name, f.Summary})
					}
				}
			}
		}
	}
}
func (m *Model) externalCompletion() tea.Cmd {
	if m.config.Complete == nil || m.dismissed {
		return nil
	}
	draft, root, g := string(m.draft), m.root, m.draftGeneration
	return func() tea.Msg {
		s, err := m.config.Complete(m.ctx, root, draft)
		return suggestionsMsg{g, draft, s, err}
	}
}

type suggestionsMsg struct {
	generation uint64
	draft      string
	items      []Suggestion
	err        error
}

func (m *Model) changeDraft() { m.dismissed = false; m.draftGeneration++; m.complete() }
func (m *Model) insert() {
	s := m.suggestions[m.selection]
	m.draft = []rune(s.Insert)
	m.cursor = len(m.draft)
	m.suggestions = nil
	m.dismissed = true
	m.draftGeneration++
}
func (m *Model) read(args []string, kind string) tea.Cmd {
	m.busy = true
	m.busyKind = kind
	m.generation++
	g := m.generation
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	root := m.root
	branch, dirty := m.branch, m.dirty
	copied := append([]string(nil), args...)
	return func() tea.Msg {
		var text string
		var err error
		if m.config.Read == nil {
			err = fmt.Errorf("read adapter unavailable")
		} else {
			text, err = m.config.Read(ctx, root, copied)
		}
		if m.config.Prompt != nil {
			branch, dirty = m.config.Prompt(ctx, root)
		}
		return result{generation: g, kind: kind, args: copied, text: text, branch: branch, dirty: dirty, err: err}
	}
}
func (m *Model) dashboard() tea.Cmd {
	args := append([]string(nil), m.monitorArgs...)
	if len(args) == 0 {
		args = []string{"dashboard"}
	}
	if !hasOptionValue(args, "scope") {
		args = append(args, "--scope", m.scope)
	}
	if !hasOptionValue(args, "once") {
		args = append(args, "--once")
	}
	return m.read(args, "dashboard")
}
func (m *Model) submit() tea.Cmd {
	if len(m.suggestions) > 0 {
		m.insert()
		return nil
	}
	if m.busy {
		return nil
	}
	args, err := Parse(string(m.draft))
	if err != nil {
		m.lastError = true
		m.body = err.Error()
		return nil
	}
	m.draft = nil
	m.cursor = 0
	m.dismissed = true
	m.suggestions = nil
	m.review = nil
	m.monitor = false
	m.scroll = 0
	switch args[0] {
	case "exit":
		if len(args) != 1 {
			m.lastError = true
			m.body = "/exit takes no arguments"
			return nil
		}
		return tea.Quit
	case "clear":
		if len(args) != 1 {
			m.lastError = true
			m.body = "/clear takes no arguments"
			return nil
		}
		m.lastError = false
		m.body = ""
		return nil
	case "help":
		m.body = m.help(strings.Join(args[1:], " "))
		m.lastError = m.body == "No matching command."
		return nil
	case "scope":
		m.monitorArgs = nil
		if len(args) != 2 || (args[1] != "project" && args[1] != "all") {
			m.lastError = true
			m.body = "Usage: /scope project|all"
			return nil
		}
		m.scope = args[1]
		m.monitor = true
		return m.dashboard()
	case "reference":
		if len(args) == 2 {
			if strings.ContainsAny(args[1], "/\\") || args[1] == "." || args[1] == ".." {
				m.lastError = true
				m.body = "Reference must be a single local folder name"
				return nil
			}
			m.lastError = false
			m.reference = args[1]
			m.body = "Selected reference: " + safe(args[1]) + " (context only)"
			return nil
		}
		return m.read([]string{"work", "--references"}, "read")
	case "project":
		if len(args) >= 2 && args[1] == "select" {
			if len(args) != 3 || m.config.SelectProject == nil {
				m.lastError = true
				m.body = "Usage: /project select NAME (registered projects only)"
				return nil
			}
			m.busy = true
			m.generation++
			g := m.generation
			root, name := m.root, args[2]
			return func() tea.Msg {
				r, e := m.config.SelectProject(m.ctx, root, name)
				branch, dirty := "", false
				if e == nil && m.config.Prompt != nil {
					branch, dirty = m.config.Prompt(m.ctx, r)
				}
				return result{generation: g, kind: "project", root: r, branch: branch, dirty: dirty, err: e}
			}
		}
	case "run":
		if m.reference != "" && !hasOptionValue(args, "reference") && !hasTicketLocator(args) {
			args = append(args, "--reference", m.reference)
		}
		return m.read(args, "plan")
	case "work":
		if len(args) == 1 && m.reference != "" {
			args = append(args, "--reference", m.reference)
		}
	case "dashboard":
		if c, ok := m.command(args); ok && c.Native {
			break
		}
		m.monitor = true
		m.monitorArgs = append([]string(nil), args...)
		return m.dashboard()
	}
	command, known := m.command(args)
	if !known {
		m.lastError = true
		m.body = "Unknown command. Use /help."
		return nil
	}
	if known && !command.Native {
		return m.read(args, "read")
	}
	if m.config.Execute == nil {
		m.lastError = true
		m.body = "Native command adapter unavailable"
		return nil
	}
	commandToRun, err := m.config.Execute(m.ctx, m.root, args)
	if err != nil {
		m.lastError = true
		m.body = err.Error()
		return nil
	}
	if commandToRun == nil {
		m.lastError = true
		m.body = "Native command adapter returned no process"
		return nil
	}
	return tea.ExecProcess(commandToRun, func(err error) tea.Msg { return completed{err} })
}
func hasOptionValue(args []string, name string) bool { _, ok := optionValue(args, name); return ok }
func hasTicketLocator(args []string) bool {
	value, ok := optionValue(args, "ticket")
	return ok && strings.HasPrefix(value, "@")
}
func optionValue(args []string, name string) (string, bool) {
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		key, value, assigned := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key == name {
			if !assigned && i+1 < len(args) {
				value = args[i+1]
			}
			return value, true
		}
		if valueFlags[key] && !assigned {
			i++
		}
	}
	return "", false
}
func (m *Model) start() tea.Cmd {
	if m.busy || len(m.review) == 0 {
		return nil
	}
	args := append([]string(nil), m.review...)
	m.busy = true
	m.busyKind = "launch"
	m.generation++
	g := m.generation
	root := m.root
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	return func() tea.Msg {
		if m.config.Launch == nil {
			return result{generation: g, kind: "launch", args: args, err: fmt.Errorf("terminal launch adapter unavailable")}
		}
		text, err := m.config.Launch(ctx, root, args)
		return result{generation: g, kind: "launch", args: args, text: text, err: err}
	}
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		return m, nil
	case suggestionsMsg:
		if msg.generation == m.draftGeneration && msg.draft == string(m.draft) && !m.dismissed {
			m.suggestions = append(m.suggestions, msg.items...)
		}
		return m, nil
	case result:
		if msg.generation != m.generation {
			return m, nil
		}
		m.busy = false
		m.busyKind = ""
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if msg.err != nil {
			m.lastError = true
			m.body = "Error: " + safe(msg.err.Error()) + "\n" + safe(msg.text)
			m.monitor = false
			if msg.kind == "dashboard" && m.launchNotice != "" {
				m.body = m.launchNotice + "\n\n" + m.body
			}
			if msg.kind == "launch" {
				m.launchNotice = m.body
				if launchMayHaveStarted(msg.text) {
					m.review = nil
					m.monitor = true
					m.monitorArgs = nil
					return m, m.dashboard()
				}
				m.review = msg.args
			}
			return m, nil
		}
		m.lastError = false
		if msg.kind != "launch" {
			m.branch = msg.branch
			m.dirty = msg.dirty
		}
		m.body = safe(msg.text)
		if msg.kind == "dashboard" && m.launchNotice != "" {
			m.body = m.launchNotice + "\n\n" + m.body
		}
		if msg.kind != "dashboard" {
			m.scroll = 0
		}
		switch msg.kind {
		case "plan":
			if !hasOption(msg.args, "dry-run") {
				m.review = msg.args
				m.body += "\n\nReview this plan. Ctrl+S starts; Esc cancels. Nothing has started."
			}
		case "project":
			m.root = msg.root
			m.reference = ""
			m.body = "Selected project: " + safe(inlineSafe(filepath.Base(m.root))) + ". Existing runs keep their recorded project."
		case "launch":
			m.launchNotice = safe(msg.text)
			m.review = nil
			m.body += "\n\nBackground terminal request submitted. Keep its controller terminal open."
			m.monitor = true
			m.monitorArgs = nil
			return m, m.dashboard()
		case "dashboard":
			if m.monitor {
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return poll{} })
			}
		}
		return m, nil
	case poll:
		if m.monitor && !m.busy {
			return m, m.dashboard()
		}
		return m, nil
	case completed:
		m.lastError = msg.err != nil
		if msg.err != nil {
			m.body = "Native command failed: " + safe(msg.err.Error())
		} else {
			m.body = "Native command finished."
		}
		if m.config.Prompt != nil {
			m.branch, m.dirty = m.config.Prompt(m.ctx, m.root)
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.busy && m.busyKind == "launch" {
				m.draft = nil
				m.cursor = 0
				m.suggestions = nil
				m.dismissed = true
				m.body = "Waiting for terminal launch acknowledgment. Ctrl+C clears the draft without cancelling a dispatched launch."
				return m, nil
			}
			if len(m.draft) > 0 || m.busy || len(m.review) > 0 {
				m.draft = nil
				m.cursor = 0
				m.suggestions = nil
				m.review = nil
				m.dismissed = true
				if m.cancel != nil {
					m.cancel()
				}
				m.busy = false
				m.generation++
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+s":
			return m, m.start()
		case "esc":
			m.review = nil
			m.suggestions = nil
			m.dismissed = true
			return m, nil
		case "enter":
			return m, m.submit()
		case "tab":
			if len(m.suggestions) > 0 {
				m.insert()
			}
			return m, nil
		case "up":
			if len(m.suggestions) > 0 {
				m.selection = (m.selection + len(m.suggestions) - 1) % len(m.suggestions)
			}
			return m, nil
		case "down":
			if len(m.suggestions) > 0 {
				m.selection = (m.selection + 1) % len(m.suggestions)
			}
			return m, nil
		case "pgup":
			m.scroll += max(1, m.height/2)
			return m, nil
		case "pgdown":
			m.scroll = max(0, m.scroll-max(1, m.height/2))
			return m, nil
		case "left":
			m.cursor = max(0, m.cursor-1)
			return m, nil
		case "right":
			m.cursor = min(len(m.draft), m.cursor+1)
			return m, nil
		case "home", "ctrl+a":
			m.cursor = 0
			return m, nil
		case "end", "ctrl+e":
			m.cursor = len(m.draft)
			return m, nil
		case "backspace", "ctrl+h":
			if m.cursor > 0 {
				m.draft = append(m.draft[:m.cursor-1], m.draft[m.cursor:]...)
				m.cursor--
				m.changeDraft()
			}
			return m, m.externalCompletion()
		case "delete":
			if m.cursor < len(m.draft) {
				m.draft = append(m.draft[:m.cursor], m.draft[m.cursor+1:]...)
				m.changeDraft()
			}
			return m, m.externalCompletion()
		}
		if msg.Type == tea.KeyRunes {
			for _, r := range msg.Runes {
				if unicode.IsControl(r) {
					continue
				}
				m.draft = append(m.draft[:m.cursor], append([]rune{r}, m.draft[m.cursor:]...)...)
				m.cursor++
			}
			m.changeDraft()
			return m, m.externalCompletion()
		}
	}
	return m, nil
}
func safe(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}
func color(code, text string) string { return "\x1b[" + code + "m" + text + "\x1b[0m" }
func (m *Model) View() string {
	width, height := max(1, m.width), max(1, m.height)
	footer := "/ commands · Tab complete · Enter inserts then dispatches · PgUp/PgDn scroll"
	if m.busy {
		footer = "Working… Ctrl+C cancels this UI query, never a run"
	}
	if m.busy && m.busyKind == "launch" {
		footer = "Waiting for background terminal acknowledgment · Ctrl+C clears draft only"
	}
	if len(m.review) > 0 && !m.busy {
		footer = "Launch review · Ctrl+S Start · Esc Cancel"
	}
	arrowColor := "1;32"
	if m.lastError {
		arrowColor = "1;31"
	}
	prefix := color(arrowColor, "➜") + " " + color("36", inlineSafe(filepath.Base(m.root)))
	if m.branch != "" {
		prefix += " " + color("1;34", "git:(") + color("31", inlineSafe(m.branch)) + color("1;34", ")")
	}
	if m.dirty {
		prefix += " " + color("33", "✗")
	}
	prefix += " "
	draft := []rune(inlineSafe(string(m.draft)))
	cursor := min(m.cursor, len(draft))
	input := string(draft[:cursor]) + color("7", " ") + string(draft[cursor:])
	if cursor < len(draft) {
		input = string(draft[:cursor]) + color("7", string(draft[cursor])) + string(draft[cursor+1:])
	}
	prefix = ansi.Truncate(prefix, max(0, width-4), "")
	remaining := max(1, width-ansi.StringWidth(prefix))
	left := max(0, ansi.StringWidth(string(draft[:cursor]))-remaining+1)
	prompt := prefix + ansi.Truncate(ansi.TruncateLeft(input, left, ""), remaining, "")
	var bottom []string
	maxChoices := min(len(m.suggestions), max(0, min(6, height/4)))
	for i := 0; i < maxChoices; i++ {
		idx := (m.selection + i) % len(m.suggestions)
		s := m.suggestions[idx]
		mark := "  "
		if idx == m.selection {
			mark = "› "
		}
		line := mark + s.Label
		if width >= 90 {
			line += "  " + s.Description
		}
		bottom = append(bottom, ansi.Truncate(inlineSafe(line), width, ""))
	}
	bottom = append(bottom, prompt, ansi.Truncate(footer, width, ""))
	if len(bottom) > height {
		bottom = bottom[len(bottom)-height:]
	}
	available := max(0, height-len(bottom))
	body := ansi.Hardwrap(m.body, width, true)
	lines := strings.Split(body, "\n")
	end := max(0, len(lines)-min(m.scroll, len(lines)))
	start := max(0, end-available)
	visible := lines[start:end]
	if len(visible) > available {
		visible = visible[len(visible)-available:]
	}
	for len(visible) < available {
		visible = append(visible, "")
	}
	return strings.Join(append(visible, bottom...), "\n")
}

func optionPosition(args []string, position int) bool {
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			return false
		}
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		key, _, assigned := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if i == position {
			return true
		}
		if valueFlags[key] && !assigned {
			i++
		}
	}
	return false
}

func launchMayHaveStarted(text string) bool {
	var receipt struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(text), &receipt) != nil || receipt.ID == "" {
		return false
	}
	switch receipt.State {
	case "unknown", "dispatched", "started", "finished", "failed":
		return true
	}
	return false
}

func inlineSafe(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}
