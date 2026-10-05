package terminallaunch

import "os"

// Native background terminal launch is unavailable on Windows in this pass.
// Do not create a request store without native ownership/ACL validation.
func owned(os.FileInfo) bool               { return false }
func singleLink(os.FileInfo) bool          { return false }
func directoryIdentity(os.FileInfo) string { return "" }
