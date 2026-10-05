// Package notify provides optional local alerts containing no private run data.
package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type Options struct {
	Mode  string
	Sound bool
}

func (options Options) Validate() error { return validate(options, runtime.GOOS) }

func validate(options Options, platform string) error {
	switch options.Mode {
	case "", "off", "bell":
	case "desktop":
		if platform != "darwin" {
			return fmt.Errorf("desktop notifications are supported only on macOS")
		}
	default:
		return fmt.Errorf("notification mode must be off, desktop or bell")
	}
	if options.Sound && options.Mode != "desktop" {
		return fmt.Errorf("notification sound requires desktop mode")
	}
	return nil
}

type Sender interface {
	Send(context.Context, string) error
}

func New(options Options, output io.Writer) (Sender, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	switch options.Mode {
	case "desktop":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot prepare local desktop notifications")
		}
		return desktopSender{sound: options.Sound, home: home, execute: execute}, nil
	case "bell":
		if output == nil {
			return nil, fmt.Errorf("bell notifications require terminal output")
		}
		return bellSender{output}, nil
	default:
		return offSender{}, nil
	}
}

type offSender struct{}

func (offSender) Send(context.Context, string) error { return nil }

type bellSender struct{ output io.Writer }

func (sender bellSender) Send(ctx context.Context, _ string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("local notification was cancelled")
	}
	written, err := sender.output.Write([]byte{7})
	if err != nil || written != 1 {
		return fmt.Errorf("could not ring local notification bell")
	}
	return nil
}

const desktopScript = `on run argv
set messageText to item 1 of argv
if item 2 of argv is "sound" then
 display notification messageText with title "SDLC" sound name "Glass"
else
 display notification messageText with title "SDLC"
end if
end run`

type commandExecutor func(context.Context, string, []string, []string) error

func execute(ctx context.Context, path string, args, environment []string) error {
	command := exec.CommandContext(ctx, path, args...)
	command.Env = environment
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return command.Run()
}

type desktopSender struct {
	sound   bool
	home    string
	execute commandExecutor
}

func (sender desktopSender) Send(ctx context.Context, message string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sound := "silent"
	if sender.sound {
		sound = "sound"
	}
	if err := sender.execute(ctx, "/usr/bin/osascript", []string{"-e", desktopScript, message, sound}, []string{"PATH=/usr/bin:/bin", "HOME=" + sender.home}); err != nil {
		return fmt.Errorf("could not deliver local desktop notification")
	}
	return nil
}
