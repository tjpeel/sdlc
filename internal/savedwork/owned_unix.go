//go:build linux || darwin

package savedwork

import (
	"os"
	"syscall"
)

func owned(info os.FileInfo, singleLink bool) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid()) && (!singleLink || s.Nlink == 1)
}
