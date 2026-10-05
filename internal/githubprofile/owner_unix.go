//go:build linux || darwin

package githubprofile

import (
	"fmt"
	"os"
	"syscall"
)

func owned(info os.FileInfo, singleLink bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || (singleLink && stat.Nlink != 1) {
		return fmt.Errorf("unsafe account metadata ownership or links")
	}
	return nil
}
