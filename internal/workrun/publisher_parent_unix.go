//go:build !windows

package workrun

import (
	"fmt"
	"os"
	"syscall"
)

// The bind-mounted child must be writable to UID 1000 on Docker Desktop. The
// unmounted parent supplies the local-user boundary and is checked every time.
func privatePublisherParent(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("unsafe publisher parent")
	}
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok || data.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("unsafe publisher parent ownership")
	}
	return nil
}
