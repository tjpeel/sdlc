package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Only runs under the disposable PTY created by the parent test.
func TestFeedbackTerminalChild(t *testing.T) {
	mode := os.Getenv("SDLC_FEEDBACK_PTY_FIXTURE")
	if mode == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	prompt, openErr := newSigningTerminal(ctx, os.Stdin)
	if openErr != nil {
		t.Fatal(openErr)
	}
	text, err := readHiddenFeedbackTerminal(ctx, prompt.(*signingTerminal))
	if mode == "cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel failed", err)
		}
	} else if err != nil || text != "private finding one\nprivate finding two\n" {
		t.Fatal("paste failed", err)
	}
	fmt.Println("PTY settings restored")
	ack := os.NewFile(3, "disposable-pty-ack")
	defer ack.Close()
	if _, err := ack.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackTerminalHidesPasteAndRestoresAfterSIGINT(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("supported terminal hosts only")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PTY probe requires python3")
	}
	probe := strings.NewReplacer(
		"TestSigningTerminalChild", "TestFeedbackTerminalChild",
		"SDLC_SIGNING_PTY_FIXTURE", "SDLC_FEEDBACK_PTY_FIXTURE",
		"Disposable token (hidden): ", "Ctrl-C cancels.",
		"fake-hidden-bootstrap-token\\n", "\\x1b[200~private finding one\\nprivate finding two\\n\\x1b[201~\\x04",
		"fake-hidden-bootstrap-token", "private finding one",
	).Replace(signingPTYProbe)
	for _, mode := range []string{"input", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(python, "-c", probe, os.Args[0])
			cmd.Env = append(os.Environ(), "SDLC_FEEDBACK_PTY_FIXTURE="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "PASS:") {
				t.Fatal("hidden-paste PTY check failed", err, string(output))
			}
		})
	}
}
