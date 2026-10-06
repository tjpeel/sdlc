package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// RunPlain offers line-oriented command review when the rich terminal is unavailable.
// It does not poll between lines or consume input ahead of a native TTY handoff.
func RunPlain(ctx context.Context, c Config) error {
	if c.Input == nil {
		c.Input = os.Stdin
	}
	if c.Output == nil {
		c.Output = os.Stdout
	}
	m := NewModel(ctx, c)
	fmt.Fprintln(c.Output, "SDLC", safe(c.Version), "(plain shell; /help for commands)")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.action != nil && m.answerMode {
			fmt.Fprintln(c.Output, m.actionContext())
			fmt.Fprintln(c.Output, "Enter the answer; . submits and resumes, /cancel discards. Use .. for a literal dot and //cancel for a literal /cancel.")
			submitted, e := readPlainAnswer(c.Input, m)
			if e != nil {
				return e
			}
			if !submitted {
				m.discardAction()
				fmt.Fprintln(c.Output, "Answer cancelled.")
				continue
			}
			cmd := m.dispatchAction()
			if cmd != nil {
				_, next := m.Update(cmd())
				if next != nil {
					_, _ = m.Update(next())
				}
			}
			m.monitor = false
			fmt.Fprintln(c.Output, m.body)
			continue
		}
		fmt.Fprintf(c.Output, "➜ %s > ", safe(m.root))
		line, err := readLine(c.Input)
		if errors.Is(err, io.EOF) && line == "" {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		args, parseErr := Parse(line)
		if parseErr != nil {
			fmt.Fprintln(c.Output, parseErr)
			continue
		}
		if args[0] == "exit" && len(args) == 1 {
			return nil
		}
		if args[0] == "cancel" && len(args) == 1 {
			m.review = nil
			if m.action != nil {
				m.discardAction()
			}
			fmt.Fprintln(c.Output, "Launch review cancelled; no run started.")
			continue
		}
		var command func() any
		if args[0] == "start" && len(args) == 1 {
			if len(m.review) == 0 && m.action == nil {
				fmt.Fprintln(c.Output, "No reviewed launch. Use /run first.")
				continue
			}
			cmd := m.start()
			if m.action != nil {
				cmd = m.dispatchAction()
			}
			command = func() any { return cmd() }
		} else {
			entry, known := m.command(args)
			local := args[0] == "help" || args[0] == "clear" || args[0] == "scope" || args[0] == "reference" || args[0] == "run" || args[0] == "answer" || args[0] == "attention" || args[0] == "resume" || (args[0] == "project" && len(args) > 1 && args[1] == "select")
			if !local && !known {
				fmt.Fprintln(c.Output, "Unknown command. Use /help.")
				continue
			}
			if !local && entry.Native {
				if c.Execute == nil {
					fmt.Fprintln(c.Output, "Native command adapter unavailable")
					continue
				}
				process, e := c.Execute(ctx, m.root, args)
				if e == nil && process == nil {
					e = fmt.Errorf("native adapter returned no process")
				}
				if e == nil {
					process.Stdin = c.Input
					process.Stdout = c.Output
					process.Stderr = c.Output
					e = process.Run()
				}
				if e != nil {
					fmt.Fprintln(c.Output, "Native command failed:", safe(e.Error()))
				}
				continue
			}
			m.draft = []rune(line)
			m.dismissed = true
			m.suggestions = nil
			cmd := m.submit()
			if cmd != nil {
				command = func() any { return cmd() }
			}
		}
		if command != nil {
			msg := command()
			if value, ok := msg.(result); ok {
				_, next := m.Update(value)
				if (value.kind == "launch" || value.kind == "run-action") && next != nil {
					_, _ = m.Update(next())
				}
			}
		}
		m.monitor = false
		if m.action != nil && !m.answerMode {
			fmt.Fprintln(c.Output, m.actionContext())
			fmt.Fprintln(c.Output, "/start resumes; /cancel cancels.")
		}
		fmt.Fprintln(c.Output, strings.ReplaceAll(m.body, "Ctrl+S starts; Esc cancels.", "/start confirms; /cancel cancels."))
	}
}
func readLine(r io.Reader) (string, error) { return readBoundedLine(r, 16384) }
func readBoundedLine(r io.Reader, limit int) (string, error) {
	var b strings.Builder
	oversized := false
	var one [1]byte
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				if oversized {
					return "", fmt.Errorf("input exceeds %d bytes", limit)
				}
				return strings.TrimSuffix(b.String(), "\r"), nil
			}
			if b.Len() >= limit {
				oversized = true
			}
			if !oversized {
				b.WriteByte(one[0])
			}
		}
		if err != nil {
			if oversized && errors.Is(err, io.EOF) {
				return "", fmt.Errorf("input exceeds %d bytes", limit)
			}
			return b.String(), err
		}
	}
}

func readPlainAnswer(input io.Reader, m *Model) (bool, error) {
	var lines []string
	if len(m.draft) > 0 {
		lines = strings.Split(string(m.draft), "\n")
	}
	for {
		line, err := readBoundedLine(input, maxAnswerBytes+1)
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			if strings.HasPrefix(err.Error(), "input exceeds ") {
				fmt.Fprintln(m.config.Output, "Answer line exceeds 64 KiB; line discarded.")
				continue
			}
			return false, err
		}
		if !utf8.ValidString(line) {
			fmt.Fprintln(m.config.Output, "Answer must be valid UTF-8; line discarded.")
			continue
		}
		switch line {
		case "/cancel":
			return false, nil
		case ".":
			m.draft = []rune(strings.Join(lines, "\n"))
			m.cursor = len(m.draft)
			if err := validateAnswer(string(m.draft)); err != nil {
				fmt.Fprintln(m.config.Output, err)
				continue
			}
			return true, nil
		case "..":
			line = "."
		case "//cancel":
			line = "/cancel"
		}
		candidate := strings.Join(append(append([]string(nil), lines...), line), "\n")
		if len(candidate) > maxAnswerBytes {
			fmt.Fprintln(m.config.Output, "Answer exceeds 64 KiB; line discarded.")
			continue
		}
		lines = append(lines, line)
	}
}
