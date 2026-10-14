//go:build !windows

package providerauth

import (
	"os"
	"syscall"
)

func diagnosticHelperUnlinked(info os.FileInfo) bool {
	metadata, ok := info.Sys().(*syscall.Stat_t)
	return ok && metadata.Nlink == 1
}
