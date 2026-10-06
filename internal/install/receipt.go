package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const ReceiptFilename = "installation.json"

// Receipt records the private local source used to install the host command.
type Receipt struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	Executable    string `json:"executable"`
	Version       string `json:"version"`
	Revision      string `json:"revision,omitempty"`
	Dirty         bool   `json:"dirty,omitempty"`
	SHA256        string `json:"sha256"`
}

// ValidateSource accepts only an existing checkout of the SDLC module.
func ValidateSource(source string) (string, error) {
	root, err := directory(source)
	if err != nil {
		return "", fmt.Errorf("source directory: %w", err)
	}
	path := filepath.Join(root, "go.mod")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("SDLC source go.mod: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", fmt.Errorf("SDLC source go.mod must be a regular file under 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("SDLC source go.mod changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64*1024 || !utf8.Valid(data) {
		return "", fmt.Errorf("SDLC source go.mod must contain UTF-8 under 64 KiB")
	}
	valid := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			valid = fields[1] == "github.com/tjpeel/sdlc" || fields[1] == `"github.com/tjpeel/sdlc"`
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("source must declare module github.com/tjpeel/sdlc")
	}
	info, err = os.Stat(filepath.Join(root, "cmd", "sdlc"))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("source is missing cmd/sdlc")
	}
	return root, nil
}

func canonicalStateDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve existing parents without creating anything, including for dry runs.
	if info, err := os.Lstat(abs); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("installation state directory must be a real directory")
		}
		return filepath.EvalSymlinks(abs)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := directory(filepath.Dir(abs))
	if os.IsNotExist(err) {
		parent, err = canonicalStateDirectory(filepath.Dir(abs))
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// ReadReceipt reads a bounded regular private receipt, rejecting stale or altered paths.
func ReadReceipt(stateDirectory string) (Receipt, error) {
	var receipt Receipt
	state, err := canonicalStateDirectory(stateDirectory)
	if err != nil {
		return receipt, err
	}
	path := filepath.Join(state, ReceiptFilename)
	info, err := os.Lstat(path)
	if err != nil {
		return receipt, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 || info.Mode().Perm()&0077 != 0 {
		return receipt, fmt.Errorf("installation receipt must be a private regular file under 16 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return receipt, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return receipt, fmt.Errorf("installation receipt changed while reading")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, fmt.Errorf("installation receipt: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return receipt, fmt.Errorf("installation receipt contains trailing data")
	}
	if receipt.SchemaVersion != 1 || receipt.Version == "" || len(receipt.Version) > 4096 {
		return receipt, fmt.Errorf("unsupported or invalid installation receipt")
	}
	if decoded, err := hex.DecodeString(receipt.SHA256); err != nil || len(decoded) != sha256.Size {
		return receipt, fmt.Errorf("installation receipt executable hash is invalid")
	}
	root, err := ValidateSource(receipt.Source)
	if err != nil || root != receipt.Source {
		return receipt, fmt.Errorf("installation receipt source is missing or noncanonical")
	}
	if !filepath.IsAbs(receipt.Executable) || filepath.Clean(receipt.Executable) != receipt.Executable || filepath.Base(receipt.Executable) != executableName() {
		return receipt, fmt.Errorf("installation receipt executable is invalid")
	}
	bin, err := directory(filepath.Dir(receipt.Executable))
	if err != nil || bin != filepath.Dir(receipt.Executable) {
		return receipt, fmt.Errorf("installation receipt executable directory is missing or noncanonical")
	}
	if info, err := os.Lstat(receipt.Executable); err != nil || !info.Mode().IsRegular() {
		return receipt, fmt.Errorf("installation receipt executable is missing or nonregular")
	}
	return receipt, nil
}

func saveReceipt(state string, receipt Receipt) error {
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	if err := os.Chmod(state, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(state, ".installation-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(state, ReceiptFilename))
}

// MatchesExecutable verifies the receipt against the current installed bytes.
func (receipt Receipt) MatchesExecutable(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	if !samePath(abs, receipt.Executable) {
		return false, nil
	}
	hash, err := executableHash(abs)
	if err != nil {
		return false, err
	}
	return hash == receipt.SHA256, nil
}

func executableHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 512*1024*1024 {
		return "", fmt.Errorf("executable must be a regular file under 512 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("executable changed while reading")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, 512*1024*1024+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
