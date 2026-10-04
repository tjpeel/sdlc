package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// A controlling terminal is required even when stdin is redirected. There is no
// fallback to echoed stdin, command arguments or environment variables for secrets.
// stty is supplied by the supported macOS/Linux host; no shell is involved.
type signingTerminal struct {
	ctx      context.Context
	file     *os.File
	original string
	mutex    sync.Mutex
	closed   bool
	closeErr error
	done     chan struct{}
}

func signingStty() (string, error) {
	// PATH can be changed by a repository's environment. Use the host OS tool,
	// never a workspace-provided executable to protect hidden credential input.
	switch runtime.GOOS {
	case "darwin":
		return "/bin/stty", nil
	case "linux":
		return "/usr/bin/stty", nil
	default:
		return "", fmt.Errorf("signing setup requires a macOS or Linux host")
	}
}

func openSigningTerminal(ctx context.Context) (signingPrompter, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("signing setup requires a private macOS/Linux terminal; use configure with an external profile for automated provisioning")
	}
	return newSigningTerminal(ctx, file)
}

// Takes ownership of file. Tests supply an inherited disposable PTY descriptor
// so terminal control can be tested without accessing the user's terminal.
func newSigningTerminal(ctx context.Context, file *os.File) (signingPrompter, error) {
	stty, err := signingStty()
	if err != nil {
		file.Close()
		return nil, err
	}
	command := exec.CommandContext(ctx, stty, "-g")
	command.Stdin = file
	command.Stderr = io.Discard
	state, err := command.Output()
	if err != nil || len(state) == 0 || len(state) > 4096 {
		file.Close()
		return nil, fmt.Errorf("cannot control terminal echo safely; signing setup stopped")
	}
	terminal := &signingTerminal{ctx: ctx, file: file, original: strings.TrimSpace(string(state)), done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			terminal.Close()
		case <-terminal.done:
		}
	}()
	return terminal, nil
}

func (terminal *signingTerminal) stty(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stty, err := signingStty()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, stty, args...)
	command.Stdin = terminal.file
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if command.Run() != nil {
		return fmt.Errorf("cannot control terminal echo safely")
	}
	return nil
}

func (terminal *signingTerminal) restore() error {
	terminal.mutex.Lock()
	defer terminal.mutex.Unlock()
	if terminal.closed {
		if terminal.closeErr != nil {
			return terminal.closeErr
		}
		return terminal.ctx.Err()
	}
	return terminal.stty(terminal.original)
}

func (terminal *signingTerminal) Read(label string, secret bool) (value []byte, resultErr error) {
	if err := terminal.ctx.Err(); err != nil {
		return nil, err
	}
	terminal.mutex.Lock()
	if terminal.closed {
		terminal.mutex.Unlock()
		return nil, fmt.Errorf("setup terminal is closed")
	}
	if secret {
		if err := terminal.stty("-echo", "-echonl"); err != nil {
			terminal.mutex.Unlock()
			return nil, err
		}
	}
	_, writeErr := io.WriteString(terminal.file, label)
	terminal.mutex.Unlock()
	// Restore the exact prior settings after success, error or cancellation.
	if secret {
		defer func() {
			if err := terminal.restore(); err != nil {
				clear(value)
				value, resultErr = nil, err
			}
		}()
	}
	if writeErr != nil {
		return nil, fmt.Errorf("cannot write setup prompt")
	}
	data := make([]byte, 16384)
	success := false
	defer func() {
		if !success {
			clear(data)
		}
	}()
	one := make([]byte, 1)
	defer clear(one)
	for index := 0; index < len(data); index++ {
		count, err := terminal.file.Read(one)
		if err := terminal.ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil || count != 1 {
			return nil, fmt.Errorf("setup input ended; no signing configuration saved")
		}
		if one[0] == '\n' {
			if secret {
				io.WriteString(terminal.file, "\n")
			}
			success = true
			return data[:index], nil
		}
		data[index] = one[0]
	}
	return nil, fmt.Errorf("setup input exceeds the private input limit")
}

func (terminal *signingTerminal) Close() error {
	terminal.mutex.Lock()
	defer terminal.mutex.Unlock()
	if terminal.closed {
		return terminal.closeErr
	}
	err := terminal.stty(terminal.original)
	if err != nil {
		// Make one further attempt before closing the terminal descriptor, then
		// retain any failure so cancellation and deferred Close can report it.
		err = terminal.stty(terminal.original)
	}
	terminal.closed = true
	close(terminal.done)
	closeErr := terminal.file.Close()
	if err != nil {
		terminal.closeErr = fmt.Errorf("terminal settings could not be restored; run /bin/stty sane (macOS) or /usr/bin/stty sane (Linux) in this terminal")
	} else {
		terminal.closeErr = closeErr
	}
	return terminal.closeErr
}
