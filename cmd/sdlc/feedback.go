package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tjpeel/sdlc/internal/workrun"
)

const feedbackUsage = "Usage: sdlc feedback --run RUN_ID [--file FILE | --stdin] [--dry-run]\nDefault: paste hidden multiline feedback; Ctrl-D submits, Ctrl-C cancels."

func feedbackCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("feedback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "run ID or unique prefix")
	path := flags.String("file", "", "UTF-8 feedback file (at most 64 KiB)")
	stdin := flags.Bool("stdin", false, "read feedback from redirected stdin")
	dry := flags.Bool("dry-run", false, "preview without saving or dispatching")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, feedbackUsage)
		return err
	} else if err != nil {
		return err
	}
	fileSupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "file" {
			fileSupplied = true
		}
	})
	if *id == "" || flags.NArg() != 0 {
		return fmt.Errorf("feedback requires --run RUN_ID")
	}
	if fileSupplied && (*path == "" || *stdin) {
		return fmt.Errorf("select a nonempty --file or --stdin")
	}
	action, err := resolveRunAction(ctx, *id, false, true)
	if err != nil {
		return err
	}
	if *dry && !fileSupplied && !*stdin {
		fmt.Fprintln(output, "Local feedback preview: hidden input will be saved privately and assessed in the original session; no GitHub comment will be posted.")
		return printPRFeedbackResumePlan(output, *action.View.Journal, false)
	}
	var text string
	if fileSupplied {
		data, err := readHumanAnswer(*path)
		if err != nil {
			return err
		}
		text = string(data)
	} else if *stdin {
		data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
		if err != nil {
			return err
		}
		text = string(data)
	} else {
		text, err = readHiddenFeedback(ctx)
		if errors.Is(err, errAnswerCancelled) {
			_, err = fmt.Fprintln(output, "Feedback cancelled.")
			return err
		}
		if err != nil {
			return err
		}
	}
	if err := workrun.ValidateLocalPRFeedback(text); err != nil {
		return err
	}
	current, err := resolveRunAction(ctx, action.View.ID, false, true)
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return fmt.Errorf("run changed while collecting feedback; retry against the current checkpoint")
	}
	if *dry {
		sum := sha256.Sum256([]byte(text))
		fmt.Fprintf(output, "Local feedback: %d bytes; SHA256 %x. No files saved or work started.\n", len(text), sum)
		return printPRFeedbackResumePlan(output, *current.View.Journal, false)
	}
	pathSaved, err := workrun.StoreLocalPRFeedback(action.View.Directory, text)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Feedback saved: %s\n", pathSaved)
	argv := append(runActionArguments(current, ""), "--pr-feedback-file", pathSaved)
	return runCommand(ctx, argv, output)
}

// Use a controlling terminal with echo and canonical line limits disabled.
// The existing terminal owner restores exact settings on cancellation and errors.
func readHiddenFeedback(ctx context.Context) (text string, resultErr error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("hidden feedback requires a controlling terminal; use --file or --stdin")
	}
	prompt, err := newSigningTerminal(ctx, file)
	if err != nil {
		return "", fmt.Errorf("cannot disable terminal echo safely; use --file or --stdin")
	}
	return readHiddenFeedbackTerminal(ctx, prompt.(*signingTerminal))
}

func readHiddenFeedbackTerminal(ctx context.Context, terminal *signingTerminal) (text string, resultErr error) {
	var err error
	defer func() { resultErr = errors.Join(resultErr, terminal.Close()) }()
	terminal.mutex.Lock()
	err = terminal.stty("-echo", "-echonl", "-icanon", "min", "1", "time", "0")
	if err == nil {
		_, err = io.WriteString(terminal.file, "Paste feedback (hidden). Ctrl-D submits; Ctrl-C cancels.\n\x1b[?2004h")
	}
	terminal.mutex.Unlock()
	if err != nil {
		return "", err
	}
	defer io.WriteString(terminal.file, "\x1b[?2004l\n")
	return readFeedbackPaste(ctx, terminal.file)
}

func readFeedbackPaste(ctx context.Context, input io.Reader) (string, error) {
	reader := bufio.NewReader(input)
	var text strings.Builder
	pasted := false
	var invalid error
	for {
		b, err := reader.ReadByte()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", fmt.Errorf("feedback was not submitted; use Ctrl-D: %w", err)
		}
		switch b {
		case 3:
			return "", errAnswerCancelled
		case 4:
			if pasted {
				invalid = fmt.Errorf("feedback contains an unsupported control character")
				continue
			}
			if invalid != nil {
				return "", invalid
			}
			value := text.String()
			return value, workrun.ValidateLocalPRFeedback(value)
		case 27:
			// Bracketed paste markers are terminal framing, not feedback content.
			marker := make([]byte, 5)
			if _, err := io.ReadFull(reader, marker); err != nil {
				return "", fmt.Errorf("incomplete paste marker")
			}
			if string(marker) == "[200~" && !pasted {
				pasted = true
				continue
			}
			if string(marker) == "[201~" && pasted {
				pasted = false
				continue
			}
			invalid = fmt.Errorf("unsupported terminal escape; use --file for prepared feedback")
			continue
		case '\r':
			b = '\n'
		}
		if b < 32 && b != '\n' && b != '\t' || b == 127 {
			invalid = fmt.Errorf("feedback contains an unsupported control character")
			continue
		}
		if text.Len() >= 64*1024 {
			invalid = fmt.Errorf("feedback exceeds 64 KiB")
			continue
		}
		if invalid == nil {
			text.WriteByte(b)
		}
	}
}
