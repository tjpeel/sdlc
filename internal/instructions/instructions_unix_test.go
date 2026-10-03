//go:build darwin || linux

package instructions

import (
	"bytes"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNamedPipeSourceRejectedWithoutBlockingOrDamagingState(t *testing.T) {
	manager := Manager{Directory: t.TempDir()}
	if err := manager.Set(source(t, []byte("Existing additions\n"))); err != nil {
		t.Fatal(err)
	}
	want := show(t, manager)
	pipe := filepath.Join(t.TempDir(), "source-pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(pipe); err == nil {
		t.Fatal("named pipe source accepted")
	}
	if !bytes.Equal(show(t, manager), want) {
		t.Fatal("named pipe source damaged stored instructions")
	}
}
