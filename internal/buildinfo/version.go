package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var Version = "0.1.0-dev"

func String() string {
	revision := "unknown"
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	if modified {
		revision += "-dirty"
	}
	return fmt.Sprintf("sdlc %s (%s; %s/%s)", Version, revision, runtime.GOOS, runtime.GOARCH)
}
