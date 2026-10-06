package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const captureLimit = 256 * 1024

type captureResult struct {
	generation      uint64
	text            string
	done, cancelled bool
	err             error
	stream          *captureStream
}

type captureStream struct {
	parser    *ansi.Parser
	mu        sync.Mutex
	output    []byte
	truncated bool
	done      bool
	err       error
	ctx       context.Context
	notify    chan struct{}
}

func (s *captureStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.parser == nil {
		s.parser = ansi.NewParser()
		s.parser.SetHandler(ansi.Handler{
			Print: func(r rune) {
				if !unicode.IsControl(r) {
					s.output = utf8.AppendRune(s.output, r)
				}
			},
			Execute: func(b byte) {
				if b == '\n' || b == '\t' {
					s.output = append(s.output, b)
				}
			},
		})
	}
	s.parser.Parse(p)
	if len(s.output) > captureLimit {
		s.output = append([]byte(nil), s.output[len(s.output)-captureLimit:]...)
		for len(s.output) > 0 && !utf8.RuneStart(s.output[0]) {
			s.output = s.output[1:]
		}
		s.truncated = true
	}
	s.mu.Unlock()
	s.signal()
	return len(p), nil
}
func (s *captureStream) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
func (s *captureStream) wait(g uint64) tea.Cmd {
	return func() tea.Msg {
		<-s.notify
		s.mu.Lock()
		defer s.mu.Unlock()
		text := capturedText(s.output, s.done)
		if s.truncated {
			text = "[Earlier output omitted]\n" + text
		}
		return captureResult{generation: g, text: text, done: s.done, cancelled: s.ctx.Err() != nil, err: s.err, stream: s}
	}
}

// A pipe write can split a UTF-8 rune; wait for its remaining bytes before
// presenting that suffix. Invalid complete sequences are replaced safely.
func capturedText(raw []byte, done bool) string {
	if !done && len(raw) > 0 {
		start := len(raw) - 1
		for start > 0 && !utf8.RuneStart(raw[start]) && len(raw)-start < utf8.UTFMax {
			start--
		}
		if !utf8.FullRune(raw[start:]) {
			raw = raw[:start]
		}
	}
	return safe(ansi.Strip(strings.ToValidUTF8(string(raw), "�")))
}

func prepareCapture(process *exec.Cmd, output interface{ Write([]byte) (int, error) }) {
	process.Stdout, process.Stderr = output, output
	// Let the CLI's signal handler clean up before CommandContext's bounded kill.
	if process.Cancel != nil {
		process.Cancel = func() error { return process.Process.Signal(os.Interrupt) }
		process.WaitDelay = 2 * time.Second
	}
}
func commandLabel(process *exec.Cmd) string {
	parts := make([]string, len(process.Args))
	for i, arg := range process.Args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"\\$`;&|<>()*?[]{}!#~") {
			parts[i] = arg
		} else {
			parts[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}
	return "Command: " + safe(strings.Join(parts, " "))
}
func commandStatus(err error, cancelled bool) string {
	if cancelled {
		return "Command cancelled."
	}
	if err != nil {
		return "Command failed: " + safe(err.Error())
	}
	return "Command finished (exit 0)."
}

// Command outcome styling comes from process metadata, never captured log text.
func (m *Model) setCommandOutcome(err error, cancelled bool) {
	m.commandView = true
	m.commandOutcome = commandStatus(err, cancelled)
	m.commandOutcomeKind = "success"
	if cancelled {
		m.commandOutcomeKind = "cancelled"
	} else if err != nil {
		m.commandOutcomeKind = "failed"
	}
}
func (m *Model) clearCommandOutcome() {
	m.commandView = false
	m.commandOutcome, m.commandOutcomeKind = "", ""
}
func (m *Model) outcomeHeight() int {
	if m.commandView {
		return 1
	}
	return 0
}
func (m *Model) commandOutcomeRow(width int) string {
	text := m.commandOutcome
	code := "32"
	if text == "" {
		text = "Command running…"
		code = "36"
	}
	if m.commandOutcomeKind == "failed" {
		code = "1;31"
	}
	if m.commandOutcomeKind == "cancelled" {
		code = "33"
	}
	text = ansi.Truncate(text, max(1, width), "")
	if os.Getenv("NO_COLOR") == "" {
		return color(code, text)
	}
	return text
}

func (m *Model) capture(args []string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	process, err := m.config.Execute(ctx, m.root, args)
	if err != nil || process == nil {
		cancel()
		if err == nil {
			err = fmt.Errorf("native adapter returned no process")
		}
		m.lastError, m.body = true, safe(err.Error())
		m.setCommandOutcome(err, false)
		return nil
	}
	m.generation++
	g := m.generation
	m.busy, m.busyKind, m.cancel = true, "command", cancel
	m.readEcho = ""
	m.commandEcho = commandLabel(process)
	m.commandView = true
	m.commandOutcome, m.commandOutcomeKind = "", ""
	m.body, m.scroll = m.commandEcho+"\nRunning…", 0
	s := &captureStream{ctx: ctx, notify: make(chan struct{}, 1)}
	prepareCapture(process, s)
	return func() tea.Msg {
		go func() {
			err := process.Run()
			s.mu.Lock()
			s.done, s.err = true, err
			s.mu.Unlock()
			s.signal()
		}()
		return s.wait(g)()
	}
}

// plainCaptureWriter keeps decoder state across pipe writes, including split
// escape sequences and UTF-8 runes. Only printable text, tabs and newlines reach
// the terminal; stdout and stderr share the same serialized stream.
type plainCaptureWriter struct {
	mu      sync.Mutex
	parser  *ansi.Parser
	output  io.Writer
	pending strings.Builder
}

func newPlainCaptureWriter(output io.Writer) *plainCaptureWriter {
	w := &plainCaptureWriter{parser: ansi.NewParser(), output: output}
	w.parser.SetHandler(ansi.Handler{
		Print: func(r rune) { w.pending.WriteString(safe(string(r))) },
		Execute: func(b byte) {
			if b == '\n' || b == '\t' {
				w.pending.WriteByte(b)
			}
		},
	})
	return w
}
func (w *plainCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.parser.Parse(p)
	text := w.pending.String()
	w.pending.Reset()
	_, err := io.WriteString(w.output, text)
	return len(p), err
}

func hasHelp(args []string) bool {
	if hasOption(args, "help") {
		return true
	}
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "-h" {
			return true
		}
		if strings.HasPrefix(args[i], "--") {
			key, _, assigned := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
			if valueFlags[key] && !assigned {
				i++
			}
		}
	}
	return false
}
