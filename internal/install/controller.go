package install

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/controllerasset"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func prepareControllers(ctx context.Context, root, cli, destination, version, revision string, dirty bool, stdout, stderr io.Writer) (string, error) {
	hash, err := controllerasset.Hash(cli)
	if err != nil {
		return "", err
	}
	final := controllerasset.Directory(destination, hash)

	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".sdlc-controller-build-*")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Chmod(temporary, 0700)
			_ = os.RemoveAll(temporary)
		}
	}()
	fields := strings.Fields(version)
	reportedVersion := version
	if len(fields) >= 2 && fields[0] == "sdlc" {
		reportedVersion = fields[1]
	}
	manifest := controllerasset.Manifest{Version: reportedVersion, Revision: revision, Dirty: dirty, CLIHash: hash, Helpers: map[string]string{}}
	for _, arch := range []string{"amd64", "arm64"} {
		path := filepath.Join(temporary, "sdlc-publisher-linux-"+arch)
		command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=true", "-o", path, "./cmd/sdlc-publisher")
		command.Dir = root
		command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		command.Stdout = stdout
		command.Stderr = stderr
		if err = command.Run(); err != nil {
			return "", fmt.Errorf("build Linux publisher controller: %w", err)
		}
		if err = os.Chmod(path, 0555); err != nil {
			return "", err
		}
		manifest.Helpers[arch], err = controllerasset.Hash(path)
		if err != nil {
			return "", err
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(temporary, "manifest.json"), data, 0444); err != nil {
		return "", err
	}
	if err = os.Chmod(temporary, 0555); err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(final); statErr == nil {
		identity := buildinfo.Identity{Version: manifest.Version, Revision: manifest.Revision, Dirty: manifest.Dirty}
		for _, arch := range []string{"amd64", "arm64"} {
			if err := controllerasset.VerifyInstallation(cli, identity, "linux/"+arch); err != nil {
				return "", fmt.Errorf("existing trusted controller bundle differs: %w", err)
			}
		}
		existing, err := os.ReadFile(filepath.Join(final, "manifest.json"))
		if err != nil || string(existing) != string(data) {
			return "", fmt.Errorf("existing trusted controller manifest differs")
		}
		return final, nil
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	if err = os.Rename(temporary, final); err != nil {
		return "", err
	}
	success = true
	return final, nil
}
