//go:build !windows

package workrun

import (
	"os"
	"syscall"
)

func diagnosticOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
