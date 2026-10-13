//go:build !windows

package controllerasset

import (
	"fmt"
	"os"
	"syscall"
)

func privateImmutable(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0222 != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("controller asset must be owned and immutable")
	}
	return nil
}
