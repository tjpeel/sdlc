package instructions

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func source(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custom.md")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func show(t *testing.T, manager Manager) []byte {
	t.Helper()
	content, err := manager.Show()
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestDefaultAndResetPreserveRuleAndOtherState(t *testing.T) {
	manager := Manager{Directory: filepath.Join(t.TempDir(), "state")}
	want := []byte("If you have any question for a human, stop implementation immediately and report it. Do not assume an answer or continue implementation until a human has answered.\n")
	if got := show(t, manager); !bytes.Equal(got, want) {
		t.Fatalf("default instructions = %q", got)
	}
	if _, err := os.Stat(manager.Directory); !os.IsNotExist(err) {
		t.Fatal("show created installation state", err)
	}
	if err := manager.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(source(t, []byte("Use the repository test command.\n"))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime.json", "auth-installation.json"} {
		if err := os.WriteFile(filepath.Join(manager.Directory, name), []byte("disposable state\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := show(t, manager); !bytes.Equal(got, want) {
		t.Fatalf("reset instructions = %q", got)
	}
	if _, err := os.Stat(manager.path()); !os.IsNotExist(err) {
		t.Fatal("reset retained custom additions", err)
	}
	for _, name := range []string{"runtime.json", "auth-installation.json"} {
		content, err := os.ReadFile(filepath.Join(manager.Directory, name))
		if err != nil || string(content) != "disposable state\n" {
			t.Fatalf("reset changed %s", name)
		}
	}
}

func TestSetSnapshotsContentAndShowReturnsIndependentCopy(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	custom := []byte("Run tests before committing.\r\n\tKeep changes focused. ✓\n")
	path := source(t, custom)
	if err := manager.Set(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Changed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte(nil), defaultContent...), '\n'), custom...)
	if got := show(t, manager); !bytes.Equal(got, want) {
		t.Fatalf("snapshot instructions = %q", got)
	}
	stored, err := os.ReadFile(manager.path())
	if err != nil || !bytes.Equal(stored, custom) {
		t.Fatal("custom content was not stored exactly", err)
	}
	first := show(t, manager)
	first[0] = '!'
	if !bytes.Equal(show(t, manager), want) {
		t.Fatal("show exposed mutable default content")
	}
}

func TestEmptyAdditionsPreserveDefaultAndReplacementWorks(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	for _, content := range [][]byte{[]byte("Custom addition\n"), {}} {
		if err := manager.Set(source(t, content)); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(show(t, manager), defaultContent) {
		t.Fatal("empty custom additions changed mandatory instructions")
	}
}

func TestInvalidInputDoesNotDamageStoredContent(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	if err := manager.Set(source(t, []byte("Existing custom addition\n"))); err != nil {
		t.Fatal(err)
	}
	want := show(t, manager)
	cases := map[string][]byte{
		"oversize":     bytes.Repeat([]byte("x"), maximumSize+1),
		"invalid UTF8": {0xff},
		"NUL":          []byte("private content\x00"),
		"escape":       []byte("private content\x1b[31m"),
		"DEL":          []byte("private content\x7f"),
		"C1 control":   []byte("private content\u0085"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			err := manager.Set(source(t, content))
			if err == nil {
				t.Fatal("invalid custom input accepted")
			}
			if strings.Contains(err.Error(), "private content") {
				t.Fatal("error exposed input content")
			}
			if !bytes.Equal(show(t, manager), want) {
				t.Fatal("failed set damaged previous custom additions")
			}
		})
	}
	if err := manager.Set(t.TempDir()); err == nil {
		t.Fatal("directory accepted as instruction source")
	}
	if !bytes.Equal(show(t, manager), want) {
		t.Fatal("nonregular source damaged previous custom additions")
	}
}

func TestMaximumSizeAccepted(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	custom := bytes.Repeat([]byte("x"), maximumSize)
	if err := manager.Set(source(t, custom)); err != nil {
		t.Fatal(err)
	}
	if got := show(t, manager); len(got) != len(defaultContent)+1+maximumSize {
		t.Fatal("maximum-sized additions were truncated")
	}
}

func TestSymlinkSourcesAndDestinationsRejected(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	path := source(t, []byte("External content\n"))
	link := filepath.Join(t.TempDir(), "linked.md")
	if err := os.Symlink(path, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires permission", err)
		}
		t.Fatal(err)
	}
	if err := manager.Set(path); err != nil {
		t.Fatal(err)
	}
	want := show(t, manager)
	if err := manager.Set(link); err == nil {
		t.Fatal("symlink instruction source accepted")
	}
	if !bytes.Equal(show(t, manager), want) {
		t.Fatal("symlink source damaged stored content")
	}
	if err := os.Remove(manager.path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, manager.path()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(source(t, []byte("Replacement\n"))); err == nil {
		t.Fatal("symlink destination accepted")
	}
	if _, err := manager.Show(); err == nil {
		t.Fatal("show followed a symlink destination")
	}
	if err := manager.Reset(); err == nil {
		t.Fatal("reset accepted a symlink destination")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "External content\n" {
		t.Fatal("symlink target was changed", err)
	}
}

func TestUnsafeStoredContentRejected(t *testing.T) {
	for _, content := range [][]byte{bytes.Repeat([]byte("x"), maximumSize+1), {0xff}, {'\x00'}} {
		manager := Manager{Directory: t.TempDir()}
		if err := os.WriteFile(manager.path(), content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Show(); err == nil {
			t.Fatal("unsafe stored instructions accepted")
		}
	}
}

func TestNonregularDestinationRejected(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	if err := os.Mkdir(manager.path(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(source(t, []byte("Custom addition\n"))); err == nil {
		t.Fatal("directory accepted as instruction destination")
	}
	if _, err := manager.Show(); err == nil {
		t.Fatal("show accepted a directory destination")
	}
	if err := manager.Reset(); err == nil {
		t.Fatal("reset accepted a directory destination")
	}
	if info, err := os.Stat(manager.path()); err != nil || !info.IsDir() {
		t.Fatal("operation changed directory destination", err)
	}
}

func TestSymlinkStateDirectoryAndLockRejected(t *testing.T) {
	actual := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(actual, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires permission", err)
		}
		t.Fatal(err)
	}
	manager := Manager{Directory: link}
	path := source(t, []byte("Custom addition\n"))
	if err := manager.Set(path); err == nil {
		t.Fatal("set accepted a symlink state directory")
	}
	if _, err := manager.Show(); err == nil {
		t.Fatal("show accepted a symlink state directory")
	}
	if err := manager.Reset(); err == nil {
		t.Fatal("reset accepted a symlink state directory")
	}
	manager.Directory = actual
	if err := os.Symlink(path, filepath.Join(actual, "instructions.lock")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(path); err == nil {
		t.Fatal("set accepted a symlink lock file")
	}
	if err := manager.Reset(); err == nil {
		t.Fatal("reset accepted a symlink lock file")
	}
	if _, err := os.Stat(manager.path()); !os.IsNotExist(err) {
		t.Fatal("failed set created custom instructions", err)
	}
}

func TestPrivateModeAndNoOtherStateCreated(t *testing.T) {
	manager := Manager{Directory: filepath.Join(t.TempDir(), "state")}
	if err := manager.Set(source(t, []byte("Custom addition\n"))); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(manager.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "instructions.lock" || entries[1].Name() != "instructions.md" {
		t.Fatal("set created unrelated state", entries)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{manager.path(), filepath.Join(manager.Directory, "instructions.lock")} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("instruction file is not private: %v", err)
			}
		}
		info, err := os.Stat(manager.Directory)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("new state directory is not private", err)
		}
	}
}

func TestConcurrentMutationRejected(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	if err := manager.Set(source(t, []byte("Existing additions\n"))); err != nil {
		t.Fatal(err)
	}
	want := show(t, manager)
	lock, err := filelock.Acquire(filepath.Join(manager.Directory, "instructions.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := manager.Set(source(t, []byte("Replacement\n"))); err == nil {
		t.Fatal("set bypassed installation lock")
	}
	if err := manager.Reset(); err == nil {
		t.Fatal("reset bypassed installation lock")
	}
	if !bytes.Equal(show(t, manager), want) {
		t.Fatal("failed mutation damaged previous custom additions")
	}
}

func TestNewUsesInstallationStateDirectory(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("SDLC_STATE_DIR", directory)
	manager, err := New()
	if err != nil || manager.Directory != directory {
		t.Fatal("New did not use configured installation state", err)
	}
	t.Setenv("SDLC_STATE_DIR", "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	manager, err = New()
	if err != nil || manager.Directory != filepath.Join(base, "sdlc") {
		t.Fatal("New did not use default installation state", err)
	}
}
