//go:build linux || darwin

package filelock

import (
	"fmt"
	"os"
	"syscall"
)

// Acquire leaves the lock file in place so every caller locks the same inode.
// The kernel releases the lock if the process exits, including after a crash.
func Acquire(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another SDLC operation holds the lock: %w", err)
	}
	return file, nil
}
