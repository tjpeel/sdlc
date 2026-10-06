package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var Version = "0.1.0-beta.6"

// Revision can be set by the package builder when source has no Git metadata.
var Revision = "unknown"

type Identity struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Dirty    bool   `json:"dirty"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

func Current() Identity {
	identity := Identity{Version: Version, Revision: Revision, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				identity.Revision = setting.Value
			case "vcs.modified":
				identity.Dirty = setting.Value == "true"
			}
		}
	}
	return identity
}

func String() string {
	identity := Current()
	revision := identity.Revision
	if identity.Dirty {
		revision += "-dirty"
	}
	return fmt.Sprintf("sdlc %s (%s; %s/%s)", identity.Version, revision, identity.OS, identity.Arch)
}
