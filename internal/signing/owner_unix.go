//go:build !windows

package signing

import (
	"fmt"
	"os"
	"syscall"
)

func privateOwner(info os.FileInfo) error {
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok || data.Uid != uint32(os.Getuid()) || data.Nlink != 1 {
		return fmt.Errorf("credential input must be owned by this user without hard links")
	}
	return nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("unsafe signing state directory")
	}
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok || data.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("unsafe signing state directory ownership")
	}
	return nil
}
