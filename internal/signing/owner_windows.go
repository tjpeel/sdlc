//go:build windows

package signing

import (
	"fmt"
	"os"
)

func privateOwner(info os.FileInfo) error {
	return fmt.Errorf("unattended signing bootstrap ownership checks are not yet supported on Windows")
}

func privateDirectory(path string) error {
	return fmt.Errorf("unattended signing state ownership checks are not yet supported on Windows")
}
