//go:build linux || darwin

package filelock

import (
	"errors"
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
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return nil, fmt.Errorf("cannot acquire operation lock: %w", err)
	}
	return file, nil
}

func tryLock(file *os.File, mode Mode) (bool, error) {
	flags := syscall.LOCK_SH | syscall.LOCK_NB
	if mode == Exclusive {
		flags = syscall.LOCK_EX | syscall.LOCK_NB
	}
	err := syscall.Flock(int(file.Fd()), flags)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	return false, err
}
