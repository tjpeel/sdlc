// Package homebrew recognises SDLC installations managed by the local tap.
package homebrew

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tjpeel/sdlc/internal/install"
)

const Formula = "local/sdlc/sdlc"
const ManifestFilename = "sdlc-homebrew.json"

type Package struct {
	Executable string
	Source     string
	Version    string
	Revision   string
	Formula    string
}

type manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Formula       string `json:"formula"`
	Version       string `json:"version"`
	Revision      string `json:"revision"`
	SHA256        string `json:"sha256"`
}

// Detect validates an installation before allowing Homebrew to own updates.
// Ordinary source-built executables have no package claim and return nil.
func Detect(path string) (*Package, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	executable, err := filepath.EvalSymlinks(abs)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bin := filepath.Dir(executable)
	keg := filepath.Dir(bin)
	if filepath.Base(executable) != "sdlc" || filepath.Base(bin) != "bin" || filepath.Base(filepath.Dir(keg)) != "sdlc" {
		return nil, nil
	}
	manifestPath := filepath.Join(keg, "libexec", ManifestFilename)
	if _, err := os.Lstat(manifestPath); os.IsNotExist(err) {
		var receipt struct {
			Source struct {
				Tap string `json:"tap"`
			} `json:"source"`
		}
		if readJSON(filepath.Join(keg, "INSTALL_RECEIPT.json"), 1024*1024, &receipt, false) == nil && receipt.Source.Tap == "local/sdlc" {
			return nil, fmt.Errorf("Homebrew package manifest is missing")
		}
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("Homebrew package manifest: %w", err)
	}
	var metadata manifest
	if err := readJSON(manifestPath, 16*1024, &metadata, true); err != nil {
		return nil, fmt.Errorf("Homebrew package manifest: %w", err)
	}
	if metadata.SchemaVersion != 1 || metadata.Formula != Formula || strings.TrimSpace(metadata.Version) == "" || strings.ContainsAny(metadata.Version, " \t\r\n") || !validHex(metadata.Revision, 40) || !validHex(metadata.SHA256, 64) {
		return nil, fmt.Errorf("invalid Homebrew package manifest")
	}
	var receipt struct {
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	if err := readJSON(filepath.Join(keg, "INSTALL_RECEIPT.json"), 1024*1024, &receipt, false); err != nil {
		return nil, fmt.Errorf("Homebrew installation receipt: %w", err)
	}
	if receipt.Source.Tap != "local/sdlc" {
		return nil, fmt.Errorf("Homebrew installation receipt does not belong to local/sdlc")
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024*1024 {
		return nil, fmt.Errorf("Homebrew executable must be a regular file under 512 MiB")
	}
	file, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("Homebrew executable changed while reading")
	}
	build, err := buildinfo.Read(file)
	if err != nil || build.Path != "github.com/tjpeel/sdlc/cmd/sdlc" {
		return nil, fmt.Errorf("Homebrew executable is not the SDLC Go command")
	}
	settings := make(map[string]string)
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return nil, fmt.Errorf("Homebrew executable is not native to this host")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 512*1024*1024+1)); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
		return nil, fmt.Errorf("Homebrew executable does not match its package checksum")
	}
	sourcePath := filepath.Join(keg, "libexec", "source")
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Homebrew package source must be a real directory")
	}
	source, err := install.ValidateSource(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("Homebrew package source: %w", err)
	}
	if source != sourcePath {
		return nil, fmt.Errorf("Homebrew package source must remain within its keg")
	}
	return &Package{Executable: executable, Source: source, Version: metadata.Version, Revision: metadata.Revision, Formula: metadata.Formula}, nil
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func readJSON(path string, limit int64, target any, strict bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("must be a regular file under %d bytes", limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("file changed while reading")
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON data")
	}
	return nil
}
