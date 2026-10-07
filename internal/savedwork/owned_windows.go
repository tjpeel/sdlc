//go:build windows

package savedwork

import "os"

func owned(os.FileInfo, bool) bool { return false }
