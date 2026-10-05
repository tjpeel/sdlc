//go:build windows

package runstatus

import "os"

// Unix mode bits cannot establish private ownership on Windows. Fail closed
// until equivalent file ownership and hard-link checks are implemented.
func historyOwned(os.FileInfo, bool) bool { return false }
