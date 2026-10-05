package terminallaunch

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed iterm.py
var itermHelper string

// ITerm2 uses the documented official it2run utility. iTerm supplies the
// scripting credentials; SDLC never requests, reads or stores them.
type ITerm2 struct{ Directory string }

func (backend ITerm2) Launch(ctx context.Context, command string) error {
	dir, err := backend.directory()
	if err != nil {
		return err
	}
	status, err := TerminalStatus(dir)
	if err != nil {
		return err
	}
	if !status.Ready {
		return fmt.Errorf("%w: run sdlc terminal setup before launching background tabs", ErrUnsupported)
	}
	if err := terminalSession(); err != nil {
		return err
	}
	if err := automationPermission(ctx); err != nil {
		return fmt.Errorf("%w: Automation permission unavailable; run terminal setup explicitly", ErrUnsupported)
	}
	return runBridge(ctx, dir, status.Runner, "launch", command)
}

func (backend ITerm2) directory() (string, error) {
	base := backend.Directory
	if base == "" {
		base = os.Getenv("SDLC_STATE_DIR")
	}
	if base == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(dir, "sdlc")
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "terminal"), nil
}

func terminalSession() error {
	if runtime.GOOS != "darwin" || os.Getenv("TERM_PROGRAM") != "iTerm.app" || os.Getenv("ITERM_SESSION_ID") == "" {
		return fmt.Errorf("%w: use an existing local iTerm2 session", ErrUnsupported)
	}
	return nil
}

func automationPermission(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Use the same sender executable as official it2run, so the existing grant
	// is checked for the actual Automation sender rather than a Python process.
	return exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", permissionProbe).Run()
}

// AEDeterminePermissionToAutomateTarget's final parameter is askUserIfNeeded.
// https://developer.apple.com/documentation/coreservices/3025784-aedeterminepermissiontoautomatet
const permissionProbe = `
ObjC.import('Foundation');
ObjC.import('CoreServices');
ObjC.bindFunction('AEDeterminePermissionToAutomateTarget', ['int', ['void *', 'unsigned int', 'unsigned int', 'bool']]);
var target = $.NSAppleEventDescriptor.descriptorWithBundleIdentifier('com.googlecode.iterm2');
var result = $.AEDeterminePermissionToAutomateTarget(target.aeDesc, 0x2a2a2a2a, 0x2a2a2a2a, false);
if (result !== 0) { throw new Error('Existing Automation permission unavailable'); }
`
