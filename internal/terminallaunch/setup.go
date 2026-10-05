package terminallaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TerminalReadiness struct {
	Ready     bool      `json:"ready"`
	Runner    string    `json:"runner,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Message   string    `json:"message"`
}

// TerminalStatus is read-only and never contacts iTerm or requests permission.
func TerminalStatus(directory string) (TerminalReadiness, error) {
	status := TerminalReadiness{Message: "Run sdlc terminal setup in iTerm2; enable its Python API in Settings first."}
	if err := cleanDirectory(directory); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status, nil
		}
		return status, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return status, err
	}
	if !owned(info) || info.Mode().Perm() != 0700 {
		return status, fmt.Errorf("terminal state must be private and owned")
	}
	data, err := readPrivateBytes(filepath.Join(directory, "ready.json"), 16*1024)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return TerminalReadiness{}, fmt.Errorf("invalid terminal readiness record")
	}
	helper, err := readPrivateBytes(filepath.Join(directory, "bridge.py"), 128*1024)
	if err != nil {
		return TerminalReadiness{}, err
	}
	if string(helper) != itermHelper {
		return TerminalReadiness{Message: "Terminal bridge changed; run setup again."}, nil
	}
	if err := validateRunner(status.Runner); err != nil {
		return TerminalReadiness{Message: "Official iTerm2 runner unavailable; run setup again."}, nil
	}
	status.Message = "Bridge configured; launch rechecks existing Automation permission without asking."
	return status, nil
}

// Setup is explicit: macOS may show its Automation grant. The handshake creates
// no terminal tab or job, and never changes iTerm's Python API security settings.
func Setup(ctx context.Context, stateDirectory string) (TerminalReadiness, error) {
	if err := terminalSession(); err != nil {
		return TerminalReadiness{}, err
	}
	directory := filepath.Join(stateDirectory, "terminal")
	if err := privateDirectory(directory); err != nil {
		return TerminalReadiness{}, err
	}
	runner, err := findRunner()
	if err != nil {
		return TerminalReadiness{}, err
	}
	// A failed reconfiguration must not leave the previous ready flag active.
	if err := atomicPrivateBytes(filepath.Join(directory, "ready.json"), []byte(`{"ready":false,"message":"Setup handshake incomplete; run setup again."}`)); err != nil {
		return TerminalReadiness{}, err
	}
	helper := filepath.Join(directory, "bridge.py")
	if _, err := privateFile(helper); err != nil && !errors.Is(err, os.ErrNotExist) {
		return TerminalReadiness{}, err
	}
	if err := atomicPrivateBytes(helper, []byte(itermHelper)); err != nil {
		return TerminalReadiness{}, err
	}
	if err := runBridge(ctx, directory, runner, "setup", ""); err != nil {
		return TerminalReadiness{}, fmt.Errorf("terminal setup failed; enable iTerm Python API and review Automation permissions: %w", err)
	}
	status := TerminalReadiness{Ready: true, Runner: runner, CheckedAt: time.Now().UTC(), Message: "Official background-tab bridge configured."}
	data, err := json.Marshal(status)
	if err != nil {
		return status, err
	}
	if err := atomicPrivateBytes(filepath.Join(directory, "ready.json"), data); err != nil {
		return status, err
	}
	return status, nil
}

func findRunner() (string, error) {
	home, _ := os.UserHomeDir()
	var verificationError error
	for _, path := range []string{"/Applications/iTerm.app/Contents/Resources/it2run", filepath.Join(home, "Applications/iTerm.app/Contents/Resources/it2run")} {
		if err := validateRunner(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			verificationError = err
		}
	}
	if verificationError != nil {
		return "", fmt.Errorf("%w: official iTerm2 runner could not be verified: %v", ErrUnsupported, verificationError)
	}
	return "", fmt.Errorf("%w: official iTerm.app it2run utility not found", ErrUnsupported)
}

func validateRunner(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasSuffix(path, "/iTerm.app/Contents/Resources/it2run") {
		return fmt.Errorf("invalid official runner path")
	}
	if err := cleanDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("official runner must be a protected executable")
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The official release script pins this publisher, not just the bundle ID:
	// https://github.com/gnachman/iTerm2/blob/master/tools/release_beta.sh
	const requirement = `=anchor apple generic and identifier "com.googlecode.iterm2" and certificate leaf[subject.OU] = "H7V7XYVQ7D"`
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--test-requirement", requirement, bundle).Run(); err != nil {
		return fmt.Errorf("iTerm bundle signature does not establish the expected trusted identity")
	}
	return nil
}

func runBridge(ctx context.Context, directory, runner, mode, command string) error {
	id, err := NewID()
	if err != nil {
		return err
	}
	payload := filepath.Join(directory, id+".json")
	ack := filepath.Join(directory, id+".ack")
	data, err := json.Marshal(map[string]string{"mode": mode, "command": command, "session_id": os.Getenv("ITERM_SESSION_ID")})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(payload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		os.Remove(payload)
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	defer os.Remove(payload)
	defer os.Remove(ack)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Only an opaque private payload path crosses the official runner boundary.
	if err := exec.CommandContext(ctx, runner, filepath.Join(directory, "bridge.py"), payload).Run(); err != nil {
		return fmt.Errorf("terminal runner acknowledgement unavailable; inspect launch status before retrying")
	}
	for {
		data, err := readPrivateBytes(ack, 4096)
		if err == nil {
			if strings.TrimSpace(string(data)) == mode+"-ready" {
				return nil
			}
			return fmt.Errorf("terminal bridge declined the request")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("terminal bridge acknowledgement unavailable; inspect launch status before retrying")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func readPrivateBytes(path string, limit int64) ([]byte, error) {
	info, err := privateFile(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("private file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("private file exceeds size limit")
	}
	return data, nil
}

func atomicPrivateBytes(path string, data []byte) error {
	if _, err := privateFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".terminal-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
