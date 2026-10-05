package terminallaunch

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runPythonBridge(t *testing.T, module string) (string, error, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "iterm2.py"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "auth-ui-launched")
	payload, err := json.Marshal(map[string]string{"command": "'/fake/sdlc' launch execute --id opaque", "session_id": "w0t0p0:demo-session"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", itermHelper)
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "ITERM2_COOKIE=disposable-fake-cookie", "ITERM2_KEY=disposable-fake-key", "SDLC_TEST_MARKER="+marker)
	output, err := cmd.CombinedOutput()
	return string(output), err, marker
}

func TestPythonBridgeCreatesOnlyUnselectedTab(t *testing.T) {
	output, err, _ := runPythonBridge(t, `
import asyncio
from types import SimpleNamespace
class Session:
    session_id = "demo-session"
    async def async_get_profile(self):
        return SimpleNamespace(name="User terminal style")
class Window:
    tabs = [SimpleNamespace(sessions=[Session()])]
    async def async_create_tab(self, **kwargs):
        assert kwargs == {"profile":"User terminal style", "command":"'/fake/sdlc' launch execute --id opaque", "select":False}, kwargs
        return object()
async def async_get_app(connection):
    return SimpleNamespace(windows=[Window()])
def run_until_complete(main, retry):
    assert retry is False
    asyncio.run(main(None))
`)
	if err != nil || strings.TrimSpace(output) != "background-tab-created" {
		t.Fatalf("output=%s error=%v", output, err)
	}
}

func TestPythonBridgeBlocksAuthorizationSubprocess(t *testing.T) {
	output, err, marker := runPythonBridge(t, `
import subprocess
import os
import sys
def run_until_complete(main, retry):
    subprocess.run([sys.executable, "-c", "from pathlib import Path; import os; Path(os.environ['SDLC_TEST_MARKER']).touch()"], check=True)
`)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 {
		t.Fatalf("output=%s error=%v", output, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authorization subprocess ran: %v", err)
	}
}

func TestPythonBridgeRefusesPyObjCAuthRoute(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AppKit.py"), []byte("raise RuntimeError('must not import AppKit')"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iterm2.py"), []byte("import AppKit"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", itermHelper)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "ITERM2_COOKIE=disposable-fake-cookie", "ITERM2_KEY=disposable-fake-key")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || len(output) != 0 {
		t.Fatalf("output=%s error=%v", output, err)
	}
}
