//go:build !windows

package workrun

import (
	"fmt"
	"os"
	"syscall"
)

const liveDiagnosticsSupported = true

func openDiagnosticMailbox(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := validateDiagnosticMailboxFile(file, path); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func validateDiagnosticMailboxFile(file *os.File, path string) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("invalid diagnostic mailbox file")
	}
	meta, ok := info.Sys().(*syscall.Stat_t)
	if ok && meta.Nlink == 0 {
		// The helper atomically replaces request.json. An already opened old inode
		// can become unlinked before fstat. Retry only if the pathname moved away
		// from that inode; never accept a linked or otherwise unsafe request.
		current, pathErr := os.Lstat(path)
		if os.IsNotExist(pathErr) || pathErr == nil && !os.SameFile(info, current) {
			return os.ErrNotExist
		}
	}
	if !ok || meta.Nlink != 1 {
		return fmt.Errorf("linked diagnostic mailbox file")
	}
	return nil
}
