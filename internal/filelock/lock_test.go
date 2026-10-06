package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrentOperationRejectedAndClosedLockReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Acquire(path); err == nil {
		second.Close()
		t.Fatal("overlapping operation acquired the same lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(path)
	if err != nil {
		t.Fatal("closed operation left a stale lock", err)
	}
	next.Close()
}

func TestBusyAndFilesystemErrorsRemainDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Acquire(path); !errors.Is(err, ErrBusy) {
		t.Fatal("contention missing sentinel", err)
	}
	if _, err := Acquire(filepath.Join(t.TempDir(), "absent", "lock")); !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrBusy) {
		t.Fatal("open failure became contention", err)
	}
}
