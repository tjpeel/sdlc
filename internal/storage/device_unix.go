//go:build linux || darwin

package storage

import (
	"fmt"
	"os"
	"syscall"
)

func device(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("filesystem device unavailable")
	}
	return uint64(stat.Dev), nil
}
