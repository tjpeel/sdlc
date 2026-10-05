//go:build linux || darwin

package runstatus

import (
	"os"
	"syscall"
)

func historyOwned(info os.FileInfo, singleLink bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && (!singleLink || stat.Nlink == 1)
}
