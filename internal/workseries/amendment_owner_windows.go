//go:build windows

package workseries

import "os"

func amendmentOwned(info os.FileInfo) bool { return false }
