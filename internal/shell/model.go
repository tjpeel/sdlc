package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runprogress"
)

type result struct {
	action             RunAction
	generation         uint64
	kind               string
	args               []string
	text, root, branch string
	dirty              bool
	err                error
}
type poll struct{}
type progressPoll struct{ generation uint64 }
type progressResult struct {
	generation uint64
	batch      runprogress.Batch
	err        error
}
type completed struct {
	err   error
	label string
}

// Model owns view state only. Resize and background messages do not replace the draft.
type Model struct {
	progressView, progressDone, progressPending bool
	progressCursor                              string
	progressArgs                                []string
	selectedRun                                 string
	progressRuns                                map[string]bool
	action                                      *RunAction
	answerMode                                  bool
	busyKind, launchNotice                      string
	lastError                                   bool
	config                                      Config
	ctx                                         context.Context
	cancel                                      context.CancelFunc
	root, branch, reference, scope, body        string
	dirty                                       bool
	draft                                       []rune
	cursor, width, height, scroll, selection    int
	suggestions                                 []Suggestion
	dismissed, busy, monitor                    bool
	generation                                  uint64
	draftGeneration                             uint64
	monitorArgs                                 []string
	review                                      []string
	selectionContent                            string
	dashboardView                               bool
	selectionView                               string
	commandEcho                                 string
	commandOutcome, commandOutcomeKind          string
	commandView                                 bool
	readEcho                                    string
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
	if m.action != nil || m.config.Complete == nil || m.dismissed {
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

func (m *Model) changeDraft() {
	m.draftGeneration++
	if m.action != nil {
		m.suggestions = nil
		return
	}
	m.dismissed = false
	m.complete()
}
func (m *Model) insert() {
	s := m.suggestions[m.selection]
	m.draft = []rune(s.Insert)
	m.cursor = len(m.draft)
	m.suggestions = nil
	m.dismissed = true
	m.draftGeneration++
}
func (m *Model) read(args []string, kind string) tea.Cmd {
	m.readEcho = commandLabel(&exec.Cmd{Args: append([]string{"sdlc"}, args...)})
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

// progress captures all callback inputs so background polling never reads view state.
func (m *Model) progress() tea.Cmd {
	if m.progressPending || !m.monitor {
		return nil
	}
	m.progressPending = true
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	g, root, cursor := m.generation, m.root, m.progressCursor
	args := append([]string(nil), m.progressArgs...)
	callback := m.config.Progress
	return func() tea.Msg {
		if callback == nil {
			return progressResult{generation: g, err: fmt.Errorf("progress adapter unavailable")}
		}
		batch, err := callback(ctx, root, args, cursor)
		return progressResult{generation: g, batch: batch, err: err}
	}
}
func (m *Model) followProgress() tea.Cmd {
	args := append([]string(nil), m.monitorArgs...)
	if !hasOptionValue(args, "scope") {
		args = append(args, "--scope", m.scope)
	}
	return m.beginProgress(args)
}
func (m *Model) beginProgress(args []string) tea.Cmd {
	m.progressArgs = append([]string(nil), args...)
	m.progressRuns = make(map[string]bool)
	m.selectedRun = ""
	m.progressView, m.progressDone, m.progressPending = true, false, false
	m.progressCursor = ""
	return m.progress()
}
func (m *Model) followLaunch() tea.Cmd {
	if m.config.Progress != nil {
		return m.beginProgress(nil)
	}
	return m.dashboard()
}
func (m *Model) stopMonitoring() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.generation++
	m.monitor = false
	m.progressPending = false
}
func (m *Model) appendProgress(text string) {
	if text == "" {
		return
	}
	before := len(dashboard.Wrap(m.body, max(1, m.width)))
	if m.body != "" {
		m.body += "\n"
	}
	m.body += strings.TrimRight(safe(text), "\n")
	after := len(dashboard.Wrap(m.body, max(1, m.width)))
	if m.scroll > 0 {
		m.scroll += after - before
	}
	lines := strings.Split(m.body, "\n")
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
	}
	for len(lines) > 1 && len(strings.Join(lines, "\n")) > 256*1024 {
		lines = lines[1:]
	}
	m.body = strings.Join(lines, "\n")
	if len(m.body) > 256*1024 {
		m.body = string([]rune(m.body)[max(0, len([]rune(m.body))-65536):])
	}
	m.clampScroll()
}
func (m *Model) submit() tea.Cmd {
	if len(m.suggestions) > 0 {
		m.insert()
		return nil
	}
	if m.busy {
		return nil
	}
	m.dashboardView = false
	m.clearCommandOutcome()
	args, err := Parse(string(m.draft))
	if err != nil {
		m.lastError = true
		m.body = err.Error()
		return nil
	}
	m.stopMonitoring()
	m.progressView = false
	m.readEcho = ""
	m.action = nil
	m.draft = nil
	m.cursor = 0
	m.dismissed = true
	m.suggestions = nil
	m.review = nil
	m.monitor = false
	m.scroll = 0
	switch args[0] {
	case "answer", "resume":
		return m.resolveAction(args)
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
		lines, available := m.scrollViewport()
		m.scroll = max(0, len(lines)-available)
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
		if len(args) >= 2 && args[1] == "archive" && m.reference != "" && !hasOptionValue(args, "reference") {
			args = append(args, "--reference", m.reference)
		}
	case "attention":
		args = append([]string{"dashboard", "--attention"}, args[1:]...)
		m.monitor = true
		m.monitorArgs = append([]string(nil), args...)
		return m.dashboard()
	case "progress":
		if hasOption(args, "json") {
			if !hasOptionValue(args, "scope") {
				args = append(args, "--scope", m.scope)
			}
			return m.read(args, "read")
		}
		m.monitor = !hasOption(args, "once")
		m.monitorArgs = append([]string(nil), args...)
		m.body = ""
		// Fetch once even when continuous following was explicitly disabled.
		once := !m.monitor
		m.monitor = true
		cmd := m.followProgress()
		if once {
			m.monitor = false
		}
		return cmd
	case "dashboard":
		if c, ok := m.command(args); ok && c.Native {
			for i, arg := range args {
				if arg == "--scope=all" {
					args[i] = "--scope=installation"
				} else if i > 0 && arg == "all" && args[i-1] == "--scope" {
					args[i] = "installation"
				}
			}
			if len(args) > 1 && (args[1] == "remove" || args[1] == "forget") && !hasOptionValue(args, "scope") {
				scope := m.scope
				if scope == "all" {
					scope = "installation"
				}
				args = append(args, "--scope", scope)
			}
			break
		}
		m.monitor = true
		m.monitorArgs = append([]string(nil), args...)
		if m.config.Progress != nil && hasOptionValue(args, "run") && hasOption(args, "logs") && !hasOption(args, "json") {
			m.body = ""
			cmd := m.followProgress()
			if hasOption(args, "once") {
				m.monitor = false
			}
			return cmd
		}
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
	if !command.Interactive || hasHelp(args) {
		return m.capture(args)
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
	return tea.ExecProcess(commandToRun, func(err error) tea.Msg { return completed{err: err, label: commandLabel(commandToRun)} })
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
	case progressResult:
		if msg.generation != m.generation || !m.progressView {
			return m, nil
		}
		m.progressPending = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if msg.err != nil {
			m.appendProgress("Progress error: " + msg.err.Error())
			m.lastError = true
			m.monitor = false
			return m, nil
		}
		for _, event := range msg.batch.Events {
			if event.RunID != "" {
				if m.progressRuns == nil {
					m.progressRuns = make(map[string]bool)
				}
				m.progressRuns[event.RunID] = true
			}
			m.appendProgress(runprogress.Format(event))
		}
		if len(m.progressRuns) == 1 {
			for id := range m.progressRuns {
				m.selectedRun = id
			}
		} else {
			m.selectedRun = ""
		}
		m.progressCursor = msg.batch.Cursor
		m.progressDone = msg.batch.Done
		if msg.batch.Done {
			m.monitor = false
		}
		if m.monitor {
			g := m.generation
			return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return progressPoll{g} })
		}
		return m, nil
	case progressPoll:
		if msg.generation == m.generation && m.progressView && m.monitor {
			return m, m.progress()
		}
		return m, nil
	case tea.WindowSizeMsg:
		sameSize := m.width == max(1, msg.Width) && m.height == max(1, msg.Height)
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.clampScroll()
		if m.selectionView != "" && !sameSize {
			m.reflowSelection()
		}
		return m, nil
	case tea.MouseMsg:
		if m.selectionView != "" {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionPress && msg.Y >= 0 && msg.Y < m.outputHeight() {
				return m, m.enterSelection()
			}
		case tea.MouseButtonWheelUp:
			m.scrollBy(-3)
		case tea.MouseButtonWheelDown:
			m.scrollBy(3)
		}
		return m, nil
	case suggestionsMsg:
		if m.action == nil && msg.generation == m.draftGeneration && msg.draft == string(m.draft) && !m.dismissed {
			m.suggestions = append(m.suggestions, msg.items...)
		}
		return m, nil
	case result:
		if msg.generation != m.generation {
			return m, nil
		}
		m.dashboardView = msg.kind == "dashboard"
		m.clearCommandOutcome()
		viewStart, followDashboardTail := 0, false
		if msg.kind == "dashboard" && m.busyKind == "dashboard-refresh" {
			lines, available := m.scrollViewport()
			limit := max(0, len(lines)-available)
			followDashboardTail = limit > 0 && m.scroll == 0
			viewStart = max(0, limit-m.scroll)
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
			if msg.kind == "run-action" && launchMayHaveStarted(msg.text) {
				m.launchNotice = m.body
				m.action = nil
				m.draft = nil
				m.cursor = 0
				m.monitor = true
				m.monitorArgs = []string{"dashboard", "--run", msg.action.ID, "--scope", "all"}
				return m, m.followLaunch()
			}
			if msg.kind == "launch" {
				m.launchNotice = m.body
				if launchMayHaveStarted(msg.text) {
					m.review = nil
					m.monitor = true
					m.monitorArgs = nil
					return m, m.followLaunch()
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
		if msg.kind == "read" || (msg.kind == "dashboard" && !followDashboardTail) {
			lines, available := m.scrollViewport()
			m.scroll = max(0, len(lines)-available-viewStart)
		}
		switch msg.kind {
		case "resolve-answer", "resolve-resume":
			if msg.kind == "resolve-answer" && msg.action.State != "waiting_for_human" {
				m.lastError = true
				m.body = "Run is " + safe(msg.action.State) + "; answers require waiting_for_human."
				return m, nil
			}
			if msg.kind == "resolve-resume" && msg.action.State == "waiting_for_human" {
				m.lastError = true
				m.body = "This run needs an answer. Use /answer " + safe(msg.action.ID) + " to answer and resume."
				return m, nil
			}
			m.action = &msg.action
			m.answerMode = msg.kind == "resolve-answer"
			m.draft = nil
			m.cursor = 0
			m.suggestions = nil
		case "run-action":
			m.launchNotice = safe(msg.text)
			m.action = nil
			m.draft = nil
			m.cursor = 0
			m.monitor = true
			id := msg.action.ID
			m.monitorArgs = []string{"dashboard", "--run", id, "--scope", "all"}
			return m, m.followLaunch()
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
			return m, m.followLaunch()
		case "dashboard":
			if m.monitor {
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return poll{} })
			}
		}
		return m, nil
	case poll:
		if m.monitor && !m.busy && !m.progressView {
			cmd := m.dashboard()
			m.busyKind = "dashboard-refresh"
			return m, cmd
		}
		return m, nil
	case captureResult:
		if msg.generation != m.generation {
			return m, nil
		}
		m.commandView = true
		oldLines, _ := m.scrollViewport()
		m.body = m.commandEcho + "\n" + msg.text
		if m.scroll > 0 {
			newLines, _ := m.scrollViewport()
			m.scroll += len(newLines) - len(oldLines)
		}
		m.clampScroll()
		if !msg.done {
			return m, msg.stream.wait(msg.generation)
		}
		m.busy, m.busyKind, m.lastError = false, "", msg.err != nil || msg.cancelled
		m.setCommandOutcome(msg.err, msg.cancelled)
		m.clampScroll()
		if m.config.Prompt != nil {
			m.branch, m.dirty = m.config.Prompt(m.ctx, m.root)
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m, nil
	case completed:
		m.clearCommandOutcome()
		m.lastError = msg.err != nil
		if msg.err != nil {
			m.body = "Native command failed: " + safe(msg.err.Error())
		} else {
			m.body = "Native command finished."
		}
		if msg.label != "" {
			m.body = msg.label + "\n" + m.body
		}
		if m.config.Prompt != nil {
			m.branch, m.dirty = m.config.Prompt(m.ctx, m.root)
		}
		return m, m.mouseMode()
	case tea.ResumeMsg:
		return m, m.mouseMode()
	case tea.KeyMsg:
		if m.selectionView != "" {
			switch msg.String() {
			case "f2", "esc", "ctrl+c":
				return m, m.leaveSelection()
			}
			return m, nil
		}
		if msg.String() == "f2" {
			return m, m.enterSelection()
		}
		if m.action != nil {
			return m.answerKey(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			if m.busy && m.busyKind == "command" {
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			if m.busy && (m.busyKind == "launch" || m.busyKind == "run-action") {
				m.draft = nil
				m.cursor = 0
				m.suggestions = nil
				m.dismissed = true
				m.body = "Waiting for terminal launch acknowledgment. Ctrl+C clears the draft without cancelling a dispatched launch."
				return m, nil
			}
			if len(m.draft) > 0 || m.busy || len(m.review) > 0 || m.monitor {
				m.stopMonitoring()
				m.draft = nil
				m.cursor = 0
				m.suggestions = nil
				m.review = nil
				m.dismissed = true
				m.busy = false
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+s":
			return m, m.start()
		case "esc":
			if m.progressView {
				m.stopMonitoring()
			}
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
			m.scrollBy(-m.scrollPageSize())
			return m, nil
		case "pgdown":
			m.scrollBy(m.scrollPageSize())
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
			if msg.String() == "end" && m.progressView {
				m.scroll = 0
			}
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
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			runes := msg.Runes
			if msg.Type == tea.KeySpace {
				runes = []rune{' '}
			}
			for _, r := range runes {
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
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}
func color(code, text string) string { return "\x1b[" + code + "m" + text + "\x1b[0m" }
func (m *Model) completionHeight() int {
	return min(len(m.suggestions), max(0, min(6, max(1, m.height)/4)))
}

// scrollViewport uses the same wrapped lines and reserved controls as View.
func (m *Model) scrollViewport() ([]string, int) {
	width, height := max(1, m.width), max(1, m.height)
	if m.action != nil {
		_, header, _ := strings.Cut(m.actionContext(), "\n")
		if m.lastError {
			header += "\n" + m.body
		}
		lines := dashboard.Wrap(header, width)
		return lines, min(len(lines), max(0, min(height/2, height-3)))
	}
	body := m.body
	if m.dashboardView {
		body = dashboard.StyleSnapshot(body, os.Getenv("NO_COLOR") == "")
	}
	lines := dashboard.Wrap(body, width)
	return lines, max(0, height-m.completionHeight()-2-m.echoHeight()-m.outcomeHeight())
}

func (m *Model) clampScroll() {
	lines, available := m.scrollViewport()
	m.scroll = max(0, min(m.scroll, max(0, len(lines)-available)))
}

// Positive deltas move toward later output. Ordinary output stores distance
// from the tail; answer context stores distance from the beginning.
func (m *Model) scrollBy(delta int) {
	lines, available := m.scrollViewport()
	limit := max(0, len(lines)-available)
	m.scroll = max(0, min(m.scroll, limit))
	if m.action != nil {
		m.scroll += delta
	} else {
		m.scroll -= delta
	}
	m.scroll = max(0, min(m.scroll, limit))
}

func (m *Model) scrollPageSize() int {
	_, available := m.scrollViewport()
	if m.action != nil {
		return max(1, available)
	}
	return max(1, available/2)
}

func (m *Model) View() string {
	if m.selectionView != "" {
		return m.selectionView
	}
	if m.action != nil {
		return m.actionView()
	}
	width, height := max(1, m.width), max(1, m.height)
	footer := "Wheel/PgUp/PgDn scroll · Click then drag to select · Esc resumes · / commands"
	if m.progressView {
		state := "Following"
		if m.scroll > 0 {
			state = "Paused scrolling"
		}
		if m.progressDone {
			state = "Tail caught up"
		}
		if !m.monitor && !m.progressDone {
			state = "Following stopped"
		}
		footer = state + " · Wheel/PgUp pause · PgDn/End tail · /answer ID · /resume ID · Esc stop view"
	}
	if m.busy && m.busyKind != "dashboard-refresh" {
		footer = "Working… Ctrl+C cancels this UI query, never a run"
	}
	if m.busy && (m.busyKind == "launch" || m.busyKind == "run-action") {
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
	maxChoices := m.completionHeight()
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
	if m.readEcho != "" {
		bottom = append(bottom, ansi.Truncate(m.readEcho, width, ""))
	}
	if m.commandView {
		bottom = append(bottom, m.commandOutcomeRow(width))
	}
	bottom = append(bottom, prompt, ansi.Truncate("F2 select · "+footer, width, ""))
	if len(bottom) > height {
		bottom = bottom[len(bottom)-height:]
	}
	lines, available := m.scrollViewport()
	end := len(lines) - max(0, min(m.scroll, max(0, len(lines)-available)))
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

func (m *Model) echoHeight() int {
	if m.readEcho != "" {
		return 1
	}
	return 0
}
func (m *Model) outputHeight() int {
	if m.action != nil {
		_, available := m.scrollViewport()
		return min(m.height-1, available+1)
	}
	return max(0, m.height-m.completionHeight()-2-m.echoHeight())
}
func (m *Model) enterSelection() tea.Cmd {
	lines := strings.Split(m.View(), "\n")
	m.selectionContent = strings.Join(lines[:len(lines)-1], "\n")
	lines[len(lines)-1] = m.selectionFooter()
	m.selectionView = strings.Join(lines, "\n")
	return tea.DisableMouse
}
func (m *Model) selectionFooter() string {
	return ansi.Truncate("F2/Esc resume · Drag to select · Use terminal copy", max(1, m.width), "")
}
func (m *Model) reflowSelection() {
	lines := dashboard.Wrap(m.selectionContent, m.width)
	available := max(0, m.height-1)
	lines = lines[:min(len(lines), available)]
	for len(lines) < available {
		lines = append(lines, "")
	}
	m.selectionView = strings.Join(append(lines, m.selectionFooter()), "\n")
}

func (m *Model) leaveSelection() tea.Cmd {
	m.selectionView = ""
	m.selectionContent = ""
	return tea.EnableMouseCellMotion
}

func (m *Model) mouseMode() tea.Cmd {
	if m.selectionView != "" {
		return tea.DisableMouse
	}
	return tea.EnableMouseCellMotion
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
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}

const maxAnswerBytes = 64 * 1024

func validateAnswer(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("answer must be valid UTF-8")
	}
	if len(text) > maxAnswerBytes {
		return fmt.Errorf("answer exceeds 64 KiB")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("answer must contain non-whitespace text")
	}
	return nil
}
func (m *Model) resolveAction(args []string) tea.Cmd {
	id := ""
	if len(args) == 2 {
		id = args[1]
	} else if len(args) == 1 {
		id, _ = optionValue(m.monitorArgs, "run")
		if id == "" {
			id = m.selectedRun
		}
	} else {
		m.lastError = true
		m.body = "Usage: /" + args[0] + " RUN_ID"
		return nil
	}
	if id == "" {
		m.lastError = true
		m.body = "Specify a run: /" + args[0] + " RUN_ID, or select one with /dashboard --run RUN_ID."
		return nil
	}
	m.busy = true
	m.busyKind = "resolve"
	m.generation++
	g, root, kind := m.generation, m.root, "resolve-"+args[0]
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	return func() tea.Msg {
		if m.config.ResolveRun == nil {
			return result{generation: g, kind: kind, err: fmt.Errorf("run resolver unavailable")}
		}
		action, err := m.config.ResolveRun(ctx, root, id)
		return result{generation: g, kind: kind, action: action, err: err}
	}
}
func (m *Model) dispatchAction() tea.Cmd {
	if m.busy || m.action == nil {
		return nil
	}
	text := string(m.draft)
	if m.answerMode {
		if err := validateAnswer(text); err != nil {
			m.lastError = true
			m.body = err.Error()
			return nil
		}
	}
	action, answering := *m.action, m.answerMode
	m.busy = true
	m.busyKind = "run-action"
	m.generation++
	g := m.generation
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	return func() tea.Msg {
		var receipt string
		var err error
		if answering {
			if m.config.RespondRun == nil {
				err = fmt.Errorf("answer adapter unavailable")
			} else {
				receipt, err = m.config.RespondRun(ctx, action, text)
			}
		} else {
			if m.config.ResumeRun == nil {
				err = fmt.Errorf("resume adapter unavailable")
			} else {
				receipt, err = m.config.ResumeRun(ctx, action)
			}
		}
		return result{generation: g, kind: "run-action", action: action, text: receipt, err: err}
	}
}
func (m *Model) discardAction() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.generation++
	m.busy = false
	m.busyKind = ""
	m.action = nil
	m.draft = nil
	m.cursor = 0
	m.body = "Run action cancelled."
}
func (m *Model) answerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy {
		if m.busyKind == "run-action" && (msg.String() == "ctrl+c" || msg.String() == "esc") {
			m.action = nil
			m.draft = nil
			m.cursor = 0
			m.body = "Waiting for run action acknowledgment. The dispatched action continues."
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.discardAction()
		return m, nil
	case "ctrl+s":
		return m, m.dispatchAction()
	case "pgup", "pgdown":
		if msg.String() == "pgup" {
			m.scrollBy(-m.scrollPageSize())
		} else {
			m.scrollBy(m.scrollPageSize())
		}
	case "left":
		m.cursor = max(0, m.cursor-1)
	case "right":
		m.cursor = min(len(m.draft), m.cursor+1)
	case "home", "ctrl+a":
		m.cursor = 0
	case "end", "ctrl+e":
		m.cursor = len(m.draft)
	case "backspace", "ctrl+h":
		if m.cursor > 0 {
			m.draft = append(m.draft[:m.cursor-1], m.draft[m.cursor:]...)
			m.cursor--
		}
	case "delete":
		if m.cursor < len(m.draft) {
			m.draft = append(m.draft[:m.cursor], m.draft[m.cursor+1:]...)
		}
	default:
		if !m.answerMode {
			return m, nil
		}
		var runes []rune
		if msg.Type == tea.KeyEnter {
			runes = []rune{'\n'}
		} else if msg.Type == tea.KeySpace {
			runes = []rune{' '}
		} else if msg.Type == tea.KeyRunes {
			runes = msg.Runes
		} else {
			return m, nil
		}
		next := append([]rune(nil), m.draft[:m.cursor]...)
		next = append(next, runes...)
		next = append(next, m.draft[m.cursor:]...)
		if len(string(next)) > maxAnswerBytes {
			m.lastError = true
			m.body = "Answer exceeds 64 KiB"
			return m, nil
		}
		m.draft = next
		m.cursor += len(runes)
	}
	return m, nil
}
func (m *Model) actionContext() string {
	a := m.action
	header := "Run " + safe(a.ID) + " · project " + safe(a.Root) + "\nState: " + safe(a.State)
	if a.Ticket != "" {
		header += "\nTicket: " + safe(a.Reference) + "/" + safe(a.Ticket)
	}
	if a.State == "waiting_for_human" {
		for _, question := range a.Questions {
			header += "\nQuestion: " + safe(question)
		}
	}
	if a.InputAction != "" {
		header += "\nMissing file? Esc, then " + safe(a.InputAction)
	}
	return header
}
func (m *Model) actionView() string {
	width, height := max(1, m.width), max(1, m.height)
	if height == 1 {
		return ansi.Truncate("Ctrl+S submit · Esc cancel", width, "")
	}
	identity, _, _ := strings.Cut(m.actionContext(), "\n")
	// Reserve the editor and controls even when questions wrap over many lines.
	contextLines, contextHeight := m.scrollViewport()
	contextStart := max(0, min(m.scroll, max(0, len(contextLines)-contextHeight)))
	lines := []string{ansi.Truncate(identity, width, "…")}
	lines = append(lines, contextLines[contextStart:contextStart+contextHeight]...)
	editorHeight := max(0, height-contextHeight-2)
	cursor := min(m.cursor, len(m.draft))
	before := safe(string(m.draft[:cursor]))
	after := safe(string(m.draft[cursor:]))
	caret := color("7", " ")
	if after != "" {
		next := []rune(after)
		caret = color("7", string(next[0]))
		after = string(next[1:])
	}
	draftLines := strings.Split(ansi.Hardwrap(before+caret+after, width, true), "\n")
	cursorLine := len(strings.Split(ansi.Hardwrap(before+caret, width, true), "\n")) - 1
	editorStart := max(0, cursorLine-editorHeight+1)
	editorEnd := min(len(draftLines), editorStart+editorHeight)
	lines = append(lines, draftLines[editorStart:editorEnd]...)
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	instruction := "Ctrl+S resume · Wheel/PgUp/PgDn context · Esc cancel"
	if m.answerMode {
		instruction = "Ctrl+S submit · Wheel/PgUp/PgDn questions · Enter newline · Esc cancel"
	}
	if m.busy {
		instruction = "Submitting run action…"
	}
	lines = append(lines, ansi.Truncate("F2 select · "+instruction, width, ""))
	return strings.Join(lines, "\n")
}
