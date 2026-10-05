//go:build windows

package githubprofile

import (
	"fmt"
	"os"
)

func owned(os.FileInfo, bool) error {
	return fmt.Errorf("private account metadata permissions are unsupported on Windows")
}
