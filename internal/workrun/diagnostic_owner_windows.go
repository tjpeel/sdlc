//go:build windows

package workrun

import "os"

func diagnosticOwned(info os.FileInfo) bool { return false }
