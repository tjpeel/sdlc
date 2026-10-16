//go:build !windows

package workrun

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticMailboxRetriesReplacedOpenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	next := filepath.Join(dir, "next.json")
	if err := os.WriteFile(next, []byte(`{"new":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if err := validateDiagnosticMailboxFile(old, path); !errors.Is(err, os.ErrNotExist) || !os.IsNotExist(err) {
		t.Fatalf("replacement must be retryable: %v", err)
	}
	data, err := readDiagnosticMailbox(path)
	if err != nil || string(data) != `{"new":true}` {
		t.Fatal(string(data), err)
	}
}

func TestDiagnosticMailboxStillRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "request.json")
			source := filepath.Join(dir, "source")
			if err := os.WriteFile(source, []byte(`{}`), 0644); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "hardlink":
				err = os.Link(source, path)
			case "symlink":
				err = os.Symlink(source, path)
			case "writable":
				err = os.Rename(source, path)
				if err == nil {
					err = os.Chmod(path, 0666)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = readDiagnosticMailbox(path)
			if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe mailbox must not be accepted or silently retried: %v", err)
			}
		})
	}
}
