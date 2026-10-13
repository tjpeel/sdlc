//go:build !windows

package controllerasset

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStageMakesReadOnlyStoredHelperExecutableUnderRestrictiveUmask(t *testing.T) {
	source := filepath.Join(t.TempDir(), "stored-helper")
	if err := os.WriteFile(source, []byte("disposable verified artifact bytes"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0444); err != nil {
		t.Fatal(err)
	}
	oldMask := syscall.Umask(0077)
	defer syscall.Umask(oldMask)
	staged, cleanup, err := stage("linux/amd64", func(string) (string, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Lstat(staged)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("staged helper inherited restrictive umask", err)
	}
	originalHash, _ := Hash(source)
	stagedHash, _ := Hash(staged)
	if originalHash != stagedHash {
		t.Fatal("staged helper bytes changed")
	}
}
