//go:build !linux && !darwin

package storage

import (
	"fmt"
	"os"
)

func device(os.FileInfo) (uint64, error) {
	return 0, fmt.Errorf("storage scanning requires filesystem device boundary checks, unavailable on this host")
}
