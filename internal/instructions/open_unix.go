//go:build darwin || linux

package instructions

import (
	"os"
	"syscall"
)

func openRegular(path string) (*os.File, error) {
	// A source replaced by a FIFO or symlink must not block or follow the link.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
