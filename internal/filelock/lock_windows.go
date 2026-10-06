package filelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

// Closing the handle releases the lock, including when Windows ends the process.
func Acquire(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		file.Close()
		if errors.Is(callErr, syscall.Errno(33)) {
			return nil, fmt.Errorf("%w: %w", ErrBusy, callErr)
		}
		return nil, fmt.Errorf("cannot acquire operation lock: %w", callErr)
	}
	return file, nil
}

func tryLock(file *os.File, mode Mode) (bool, error) {
	flags := uintptr(1) // LOCKFILE_FAIL_IMMEDIATELY
	if mode == Exclusive {
		flags |= 2
	}
	var overlapped syscall.Overlapped
	result, _, err := lockFileEx.Call(file.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return false, nil
	}
	if errors.Is(err, syscall.Errno(33)) {
		return true, nil
	} // ERROR_LOCK_VIOLATION
	return false, err
}
