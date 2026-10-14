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
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		file.Close()
		return nil, fmt.Errorf("invalid diagnostic mailbox file")
	}
	meta, ok := info.Sys().(*syscall.Stat_t)
	if !ok || meta.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("linked diagnostic mailbox file")
	}
	return file, nil
}
