package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The helper has a disposable controlling PTY, never the user's terminal.
func TestSigningTerminalChild(t *testing.T) {
	mode := os.Getenv("SDLC_SIGNING_PTY_FIXTURE")
	if mode == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	prompt, err := newSigningTerminal(ctx, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	data, err := prompt.Read("Disposable token (hidden): ", true)
	defer clear(data)
	closeErr := prompt.Close()
	if mode == "cancel" {
		if !errors.Is(err, context.Canceled) || closeErr != nil {
			t.Fatal("cancel did not restore terminal", err, closeErr)
		}
	} else if err != nil || closeErr != nil || string(data) != "fake-hidden-bootstrap-token" {
		t.Fatal("hidden input or terminal restore failed", err, closeErr)
	}
	// Keep the session leader alive until the parent checks settings: macOS
	// invalidates the slave's ioctls after its controlling process exits.
	fmt.Println("PTY settings restored")
	ack := os.NewFile(3, "disposable-pty-ack")
	defer ack.Close()
	if _, err := ack.Read(make([]byte, 1)); err != nil {
		t.Fatal("PTY acknowledgement failed", err)
	}
}

const signingPTYProbe = `
import errno, fcntl, os, select, signal, sys, termios, time
master, slave = os.openpty()
initial = termios.tcgetattr(slave)
os.set_blocking(slave, False)
ack_read, ack_write = os.pipe()
pid = os.fork()
if pid == 0:
    os.setsid()
    fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
    for target in (0, 1, 2): os.dup2(slave, target)
    os.close(master)
    if slave > 2: os.close(slave)
    os.dup2(ack_read, 3)
    if ack_read != 3: os.close(ack_read)
    os.close(ack_write)
    os.execv(sys.argv[1], [sys.argv[1], '-test.run=^TestSigningTerminalChild$', '-test.v'])
os.close(ack_read)
output = bytearray()
sent = False
restored = False
status = None
deadline = time.monotonic() + 15
try:
    while time.monotonic() < deadline:
        if select.select([master], [], [], .05)[0]:
            try: output.extend(os.read(master, 65536))
            except OSError as error:
                if error.errno != errno.EIO: raise
        if not sent and b'Disposable token (hidden): ' in output:
            if termios.tcgetattr(slave)[3] & termios.ECHO:
                raise RuntimeError('token prompt was printed while echo remained enabled')
            if os.environ['SDLC_SIGNING_PTY_FIXTURE'] == 'cancel': os.kill(pid, signal.SIGINT)
            else: os.write(master, b'fake-hidden-bootstrap-token\n')
            sent = True
        if not restored and b'PTY settings restored' in output:
            if termios.tcgetattr(slave) != initial: raise RuntimeError('terminal settings were not restored exactly')
            os.write(ack_write, b'1')
            restored = True
        exited, value = os.waitpid(pid, os.WNOHANG)
        if exited:
            status = value
            break
    if status is None: raise RuntimeError('PTY fixture did not stop after input/cancellation')
    if not sent or not restored or os.waitstatus_to_exitcode(status) != 0:
        raise RuntimeError('PTY fixture failed: ' + output.decode(errors='replace'))
    if b'fake-hidden-bootstrap-token' in output: raise RuntimeError('token appeared in terminal output')
    print('PASS: hidden input, restoration and fixed stty selection')
finally:
    if status is None:
        os.kill(pid, signal.SIGKILL)
        os.waitpid(pid, 0)
    os.close(master)
    os.close(slave)
    os.close(ack_write)
`

func TestSigningTerminalHidesInputAndRestoresAfterSIGINT(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("signing terminal is supported on macOS and Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is needed for the disposable PTY acceptance check")
	}
	// A repository can influence PATH. This executable must never be selected
	// for terminal control, even when it appears first on PATH.
	directory := t.TempDir()
	marker := filepath.Join(directory, "shadowed-stty-ran")
	fake := "#!/bin/sh\n/bin/touch '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(directory, "stty"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"input", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(python, "-c", signingPTYProbe, os.Args[0])
			command.Env = append(os.Environ(), "SDLC_SIGNING_PTY_FIXTURE="+mode, "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "PASS:") {
				t.Fatal("PTY acceptance failed", err, string(output))
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("PATH-provided stty was executed")
			}
		})
	}
}
