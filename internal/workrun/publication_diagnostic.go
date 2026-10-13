package workrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaximumPublicationDiagnostic = 16 * 1024

type PublicationDiagnostic struct {
	Stage    string `json:"stage"`
	Code     string `json:"code"`
	ExitCode int    `json:"exit_code"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
}

func validPublicationDiagnostic(d PublicationDiagnostic) bool {
	return (d.Stage == "command" || d.Stage == "request") && (d.Code == "command_failed" || d.Code == "request_refused" || d.Code == "diagnostic_overflow") && d.ExitCode >= -1 && d.ExitCode <= 255 && d.Bytes > 0 && d.Bytes <= MaximumPublicationDiagnostic && d.Path == "failure.stderr" && len(d.SHA256) == 64
}

func diagnosticBytes(directory string, d PublicationDiagnostic) ([]byte, error) {
	if !validPublicationDiagnostic(d) {
		return nil, fmt.Errorf("invalid publication diagnostic")
	}
	path := filepath.Join(directory, "publisher", d.Path)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !diagnosticOwned(info) || info.Mode().Perm() != 0600 || info.Size() != int64(d.Bytes) {
		return nil, fmt.Errorf("invalid publication diagnostic artifact")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("publication diagnostic changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaximumPublicationDiagnostic+1))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if len(data) != d.Bytes || hex.EncodeToString(hash[:]) != d.SHA256 {
		return nil, fmt.Errorf("publication diagnostic hash mismatch")
	}
	return data, nil
}

// LoadPublicationDiagnostic returns validated metadata without reading raw diagnostics into a report.
func LoadPublicationDiagnostic(directory string) (PublicationDiagnostic, error) {
	var d PublicationDiagnostic
	if realDirectory(directory) != nil || privatePublisherParent(directory) != nil || realDirectory(filepath.Join(directory, "publisher")) != nil {
		return d, fmt.Errorf("invalid private publication diagnostic directory")
	}
	path := filepath.Join(directory, "publisher", "failure.json")
	info, err := os.Lstat(path)
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() || !diagnosticOwned(info) || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return d, fmt.Errorf("invalid publication diagnostic manifest")
	}
	f, err := os.Open(path)
	if err != nil {
		return d, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return d, fmt.Errorf("publication diagnostic manifest changed")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF {
		return d, fmt.Errorf("invalid publication diagnostic manifest")
	}
	data, err := diagnosticBytes(directory, d)
	clear(data)
	if err != nil {
		return d, err
	}
	d.Path = filepath.Join(directory, "publisher", d.Path)
	return d, nil
}

func retainPublicationDiagnostic(directory string, d PublicationDiagnostic, data []byte) error {
	if !validPublicationDiagnostic(d) || len(data) != d.Bytes {
		return fmt.Errorf("invalid publication diagnostic")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != d.SHA256 {
		return fmt.Errorf("publication diagnostic hash mismatch")
	}
	private := filepath.Join(directory, "publisher")
	if err := atomicDiagnosticWrite(filepath.Join(private, d.Path), data); err != nil {
		return err
	}
	manifest, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return atomicDiagnosticWrite(filepath.Join(private, "failure.json"), manifest)
}
func atomicDiagnosticWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".diagnostic-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
