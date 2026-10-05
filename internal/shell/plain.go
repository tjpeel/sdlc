package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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
			fmt.Fprintln(c.Output, "Launch review cancelled; no run started.")
			continue
		}
		var command func() any
		if args[0] == "start" && len(args) == 1 {
			if len(m.review) == 0 {
				fmt.Fprintln(c.Output, "No reviewed launch. Use /run first.")
				continue
			}
			cmd := m.start()
			command = func() any { return cmd() }
		} else {
			entry, known := m.command(args)
			local := args[0] == "help" || args[0] == "clear" || args[0] == "scope" || args[0] == "reference" || args[0] == "run" || (args[0] == "project" && len(args) > 1 && args[1] == "select")
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
				if value.kind == "launch" && next != nil {
					_, _ = m.Update(next())
				}
			}
		}
		m.monitor = false
		fmt.Fprintln(c.Output, strings.ReplaceAll(m.body, "Ctrl+S starts; Esc cancels.", "/start confirms; /cancel cancels."))
	}
}
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	var one [1]byte
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return strings.TrimSuffix(b.String(), "\r"), nil
			}
			if b.Len() >= 16384 {
				return "", fmt.Errorf("command exceeds 16 KiB")
			}
			b.WriteByte(one[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}
