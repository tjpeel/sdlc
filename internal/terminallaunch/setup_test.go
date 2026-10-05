package terminallaunch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFiniteSetupBridgeUsesFreshOfficialRunnerEnvironment(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "terminal")
	if err := privateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivateBytes(filepath.Join(directory, "bridge.py"), []byte(itermHelper)); err != nil {
		t.Fatal(err)
	}
	module := `
import asyncio
from types import SimpleNamespace
class Session:
    session_id = "demo-session"
    async def async_get_profile(self):
        raise AssertionError("setup must not create or configure a terminal")
class Window:
    tabs = [SimpleNamespace(sessions=[Session()])]
    async def async_create_tab(self, **kwargs):
        raise AssertionError("setup must not create a tab")
async def async_get_app(connection):
    return SimpleNamespace(windows=[Window()])
def run_until_complete(main, retry):
    assert retry is False
    asyncio.run(main(None))
`
	if err := os.WriteFile(filepath.Join(base, "iterm2.py"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(base, "iTerm.app/Contents/Resources/it2run")
	if err := os.MkdirAll(filepath.Dir(runner), 0700); err != nil {
		t.Fatal(err)
	}
	// Disposable fake script credentials never leave this isolated test process.
	script := "#!/bin/sh\nITERM2_COOKIE=disposable-fake-cookie ITERM2_KEY=disposable-fake-key exec " + quote(python) + " \"$@\"\n"
	if err := os.WriteFile(runner, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONPATH", base)
	t.Setenv("ITERM_SESSION_ID", "w0t0p0:demo-session")
	t.Setenv("ITERM2_COOKIE", "")
	t.Setenv("ITERM2_KEY", "")
	if err := runBridge(context.Background(), directory, runner, "setup", ""); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".ack") {
			t.Fatalf("temporary bridge data retained: %s", entry.Name())
		}
	}
	status := TerminalReadiness{Ready: true, Runner: runner, CheckedAt: time.Now().UTC()}
	data, _ := json.Marshal(status)
	if err := atomicPrivateBytes(filepath.Join(directory, "ready.json"), data); err != nil {
		t.Fatal(err)
	}
	got, err := TerminalStatus(directory)
	if err != nil || got.Ready {
		t.Fatalf("status=%+v error=%v", got, err)
	}
	if err := validateRunner(runner); err == nil {
		t.Fatal("unsigned fake runner accepted")
	}
	if err := atomicPrivateBytes(filepath.Join(directory, "bridge.py"), []byte("changed")); err != nil {
		t.Fatal(err)
	}
	got, err = TerminalStatus(directory)
	if err != nil || got.Ready {
		t.Fatalf("changed bridge accepted: %+v %v", got, err)
	}
}

func TestTerminalStatusIsReadOnlyWithoutSetup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	got, err := TerminalStatus(directory)
	if err != nil || got.Ready {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("status created state")
	}
}

func TestLaunchStatusDoesNotCreateUnknownID(t *testing.T) {
	s, request := fixture(t)
	if _, err := s.Status(request.ID); !os.IsNotExist(err) {
		t.Fatalf("unexpected status: %v", err)
	}
	if _, err := os.Stat(s.Directory); !os.IsNotExist(err) {
		t.Fatal("status created state")
	}
}
