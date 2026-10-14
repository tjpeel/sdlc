//go:build windows

package providerauth

import "os"

func diagnosticHelperUnlinked(os.FileInfo) bool { return false }
