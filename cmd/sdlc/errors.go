package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"golang.org/x/term"
)

// reportedError retains failure status after a child has printed its final result.
type reportedError struct{ error }

func (e *reportedError) Unwrap() error { return e.error }

func reportCommandError(output io.Writer, err error) {
	var reported *reportedError
	if errors.As(err, &reported) {
		return
	}
	text := strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(fmt.Sprintf("sdlc: %v", err)))
	if file, ok := output.(*os.File); ok && term.IsTerminal(int(file.Fd())) && os.Getenv("TERM") != "dumb" {
		if _, disabled := os.LookupEnv("NO_COLOR"); !disabled {
			text = "\x1b[1;31m" + text + "\x1b[0m"
		}
	}
	fmt.Fprintln(output, text)
}
