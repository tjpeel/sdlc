package workrun

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed cache/nuget.py
var nugetCacheScript string

// Host packages enter through a bounded, validated package-only staging tree.
// The helper sees no wider home, configuration, source URLs or auth state.
func (s *sharedChecks) prepareNuget(ctx context.Context, name, workspace string, output io.Writer) error {
	lock, err := s.lease(ctx, "nuget")
	if err != nil {
		return err
	}
	defer lock.Close()
	started := time.Now()
	staging, err := os.MkdirTemp("", "sdlc-nuget-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	staging, err = filepath.EvalSymlinks(staging)
	if err != nil {
		return err
	}
	epoch, err := s.host(ctx, "exec", s.name, "sh", "-c", `mkdir -p /sdlc/nuget; if ! test -f /sdlc/nuget/.epoch; then printf '%s' "$1" > /sdlc/nuget/.epoch; fi; cat /sdlc/nuget/.epoch`, "sdlc-nuget", name)
	if err != nil {
		return fmt.Errorf("identify NuGet seed: %w", err)
	}
	index := filepath.Join(s.directory, "nuget-host-index.json")
	if err = exportNugetHost(ctx, s.checker.nugetSeedDirectory, staging, index, workspace, string(epoch), output); err != nil {
		return err
	}
	args := s.nugetHelper(name + "-nuget-prepare")
	args = append(args, "--mount", "type=bind,src="+staging+",dst=/incoming,readonly", "--entrypoint", "python3", s.imageID, "-c", nugetCacheScript, "prepare", "/sdlc/nuget/packages", "/incoming", "/sdlc/homes/"+name, "/sdlc/nuget-sessions/"+name)
	message, err := s.host(ctx, args...)
	if err != nil {
		return fmt.Errorf("prepare private NuGet cache: %w", err)
	}
	fmt.Fprintf(output, "NuGet private cache preparation: %s\n", time.Since(started).Round(time.Millisecond))
	if _, err = output.Write(message); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(staging, "index.json"))
	if err != nil {
		return err
	}
	return os.WriteFile(index, data, 0600)
}

func exportNugetHost(ctx context.Context, seedDirectory, staging, index, workspace, epoch string, output io.Writer) error {
	python, err := exec.LookPath("python3")
	if errors.Is(err, exec.ErrNotFound) {
		if _, err = fmt.Fprintln(output, "NuGet host seed import unavailable: python3 is not installed; runtime package cache reuse continues."); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"epoch": epoch, "packages": map[string]any{}})
		return os.WriteFile(filepath.Join(staging, "index.json"), data, 0600)
	}
	if err != nil {
		return fmt.Errorf("locate NuGet host seed importer: %w", err)
	}
	if seedDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		seedDirectory = filepath.Join(home, ".nuget", "packages")
	}
	exporter := exec.CommandContext(ctx, python, "-c", nugetCacheScript, "export", seedDirectory, staging, index, workspace, epoch)
	exporter.Stdout = output
	if err := exporter.Run(); err != nil {
		return fmt.Errorf("validate NuGet host seed: %w", err)
	}
	return nil
}

func (s *sharedChecks) nugetHelper(name string) []string {
	return checkContainerEnvironment([]string{"run", "--rm", "--name", name, "--label", testOwnerLabel + "=" + name[:35], "--pull", "never", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--mount", "type=volume,src=" + s.workVolume + ",dst=/sdlc"})
}

func (s *sharedChecks) promoteNuget(ctx context.Context, name string) error {
	lock, err := s.lease(ctx, "nuget")
	if err != nil {
		return err
	}
	defer lock.Close()
	args := s.nugetHelper(name + "-nuget-promote")
	args = append(args, "--entrypoint", "python3", s.imageID, "-c", nugetCacheScript, "promote", "/sdlc/nuget-sessions/"+name+"/packages", "/sdlc/nuget/packages", "/sdlc/nuget-sessions/"+name+"/.seed-index.json")
	message, err := s.host(ctx, args...)
	if s.output != nil {
		_, writeErr := s.output.Write(message)
		if err == nil {
			err = writeErr
		}
	}
	return err
}

// Frozen older runtimes remain usable: only proxies advertising the flag receive
// cache sources. Remember capability by immutable image identity, never by tag.
func (s *sharedChecks) nugetCapability(ctx context.Context, name string) (bool, error) {
	key := sha256.Sum256([]byte(s.imageID))
	path := filepath.Join(s.directory, "proxy-nuget-"+hex.EncodeToString(key[:])+".capability")
	lock, err := s.lease(ctx, "proxy-nuget-"+hex.EncodeToString(key[:]))
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if data, err := os.ReadFile(path); err == nil {
		return string(data) == "supported", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	args := checkContainerEnvironment([]string{"run", "--rm", "--name", name + "-proxy-capability", "--label", testOwnerLabel + "=" + name, "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", s.imageID, "-c", "exec /usr/local/bin/sdlc-test-proxy --help 2>&1"})
	help, err := s.host(ctx, args...)
	if err != nil {
		return false, fmt.Errorf("probe frozen runtime test proxy: %w", err)
	}
	capability := "legacy"
	supported := strings.Contains(string(help), "-nuget-cache")
	if supported {
		capability = "supported"
	}
	return supported, os.WriteFile(path, []byte(capability), 0600)
}
