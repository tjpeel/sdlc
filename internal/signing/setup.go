package signing

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/tjpeel/sdlc/internal/filelock"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const maximumBootstrap = 16384

func referenceSegment(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || value == "." || value == ".." || strings.ContainsAny(value, "/\\?#%\"'`") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// NewProfile builds public metadata only; it does not read the bootstrap or vault.
func NewProfile(name, vault, item, publicKey, fingerprint, bootstrapPath string) (Profile, error) {
	if name == "" {
		name = "default"
	}
	if _, err := ProfilePath("", name); err != nil {
		return Profile{}, err
	}
	if !referenceSegment(vault) || !referenceSegment(item) {
		return Profile{}, fmt.Errorf("signing vault and item must be nonempty unambiguous reference segments")
	}
	publicKey = strings.TrimSpace(publicKey)
	if strings.ContainsAny(publicKey, "\x00\r\n") {
		return Profile{}, fmt.Errorf("signing public key must be one Ed25519 public-key line")
	}
	fields := strings.Fields(publicKey)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return Profile{}, fmt.Errorf("signing public key must use Ed25519")
	}
	publicKey = fields[0] + " " + fields[1]
	wire, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return Profile{}, fmt.Errorf("invalid signing public key")
	}
	hash := sha256.Sum256(wire)
	expected := "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		fingerprint = expected
	} else if fingerprint != expected {
		return Profile{}, fmt.Errorf("signing public key and supplied fingerprint differ")
	}
	profile := Profile{Provider: DefaultProvider, Version: 1, ID: name + "-signing", Reference: "op://" + vault + "/" + item + "/private key?ssh-format=openssh", PublicKey: publicKey, Fingerprint: fingerprint, BootstrapFile: bootstrapPath}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// BootstrapPath returns a profile-specific external path without writing it.
func BootstrapPath(directory, name string) (string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", fmt.Errorf("bootstrap storage directory must be an absolute canonical path")
	}
	if name == "" {
		name = "default"
	}
	if _, err := ProfilePath("", name); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "signing-"+name+"-bootstrap")
	if err := bootstrapParents(path, false); err != nil {
		return "", err
	}
	return path, nil
}

// bootstrapParents checks existing ancestors before creating any missing child.
// Ancestors such as /tmp may be shared; the immediate storage parent must be
// private and owned by this user before a credential file can be created there.
func bootstrapParents(path string, create bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || outsideRepository(path) != nil {
		return fmt.Errorf("bootstrap storage must use an absolute external path")
	}
	parent := filepath.Dir(path)
	var ancestors []string
	for current := parent; ; current = filepath.Dir(current) {
		ancestors = append(ancestors, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for index := len(ancestors) - 1; index >= 0; index-- {
		directory := ancestors[index]
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) && create {
			if os.Mkdir(directory, 0700) != nil {
				return fmt.Errorf("cannot create private bootstrap storage")
			}
			info, err = os.Lstat(directory)
		}
		if os.IsNotExist(err) && !create {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bootstrap storage ancestors must be directories without symlinks")
		}
	}
	if create && privateDirectory(parent) != nil {
		return fmt.Errorf("bootstrap storage parent must be owned by this user with mode 0700")
	}
	return nil
}

func normalizedBootstrap(token []byte) ([]byte, error) {
	if len(token) == 0 || len(token) > maximumBootstrap {
		return nil, fmt.Errorf("bootstrap token must be one bounded nonempty line")
	}
	copied := append([]byte(nil), token...)
	normalized := bytes.TrimSpace(copied)
	if len(normalized) == 0 || bytes.ContainsAny(normalized, "\x00\r\n") {
		clear(copied)
		return nil, fmt.Errorf("bootstrap token must be one bounded nonempty line")
	}
	// Return the allocation so callers can clear whitespace and the token together.
	return copied, nil
}

// StoreBootstrap creates a private file exclusively; it never overwrites a token.
func StoreBootstrap(path string, token []byte) error {
	copied, err := normalizedBootstrap(token)
	if err != nil {
		return err
	}
	defer clear(copied)
	if err := bootstrapParents(path, true); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	expected, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("cannot inspect private bootstrap storage")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return fmt.Errorf("cannot open private bootstrap storage")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) || opened.Mode().Perm() != 0700 || privateDirectory(parent) != nil {
		return fmt.Errorf("bootstrap storage changed while opening")
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("bootstrap file already exists or cannot be created safely")
	}
	created, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("cannot inspect created bootstrap file")
	}
	success := false
	defer func() {
		if !success {
			removeCreatedFile(root, filepath.Base(path), created)
		}
	}()
	normalized := bytes.TrimSpace(copied)
	_, writeErr := file.Write(normalized)
	if writeErr == nil {
		_, writeErr = file.Write([]byte{'\n'})
	}
	info, statErr := file.Stat()
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || privateOwner(info) != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot save private bootstrap file")
	}
	success = true
	return nil
}

// CheckBootstrap validates local framing and private file safety without network.
func CheckBootstrap(profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	data, err := privateRead(profile.BootstrapFile, maximumBootstrap)
	if err != nil {
		return err
	}
	defer clear(data)
	copied, err := normalizedBootstrap(data)
	if copied != nil {
		clear(copied)
	}
	return err
}

// StoreNew writes a profile exclusively for onboarding. It never overwrites a
// profile produced by another setup process and never reads bootstrap contents.
func StoreNew(directory string, profile Profile, name string) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("signing profile storage must use an absolute canonical directory")
	}
	path, err := ProfilePath(directory, name)
	if err != nil {
		return err
	}
	if err := bootstrapParents(path, true); err != nil {
		return fmt.Errorf("signing profile storage must use owned private directories outside repositories without symlinks")
	}
	expected, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("cannot inspect private signing profile storage")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("cannot open private signing profile storage")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) || opened.Mode().Perm() != 0700 || privateDirectory(directory) != nil {
		return fmt.Errorf("signing profile storage changed while opening")
	}
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode signing profile")
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("signing profile already exists or cannot be created safely")
	}
	created, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("cannot inspect created signing profile")
	}
	success := false
	defer func() {
		if !success {
			removeCreatedFile(root, filepath.Base(path), created)
		}
	}()
	_, writeErr := file.Write(append(encoded, '\n'))
	info, statErr := file.Stat()
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || privateOwner(info) != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot save private signing profile")
	}
	success = true
	return nil
}

// StoreReplacement replaces only the private profile read by the setup wizard.
// The caller must hold its AcquireSetup lease until this operation finishes.
// Bootstrap files are neither opened nor changed.
func StoreReplacement(directory string, profile Profile, expected Profile, name string) error {
	if profile.Validate() != nil {
		return fmt.Errorf("invalid replacement signing profile")
	}
	if expected.Validate() != nil {
		return fmt.Errorf("invalid expected signing profile")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("signing profile storage must use an absolute canonical directory")
	}
	path, err := ProfilePath(directory, name)
	if err != nil {
		return err
	}
	if filepath.Clean(profile.BootstrapFile) == path || filepath.Clean(expected.BootstrapFile) == path {
		return fmt.Errorf("signing profile storage must differ from bootstrap files")
	}
	if bootstrapParents(path, false) != nil || privateDirectory(directory) != nil {
		return fmt.Errorf("signing profile storage must use owned private directories outside repositories without symlinks")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("cannot inspect private signing profile storage")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("cannot open private signing profile storage")
	}
	defer root.Close()
	if replacementDirectory(root, directory, path, directoryInfo) != nil {
		return fmt.Errorf("signing profile storage changed while opening")
	}
	base := filepath.Base(path)
	targetInfo, err := matchingReplacementProfile(root, base, expected)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode signing profile")
	}
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return fmt.Errorf("cannot prepare replacement signing profile")
	}
	temporary := ".signing-profile-" + hex.EncodeToString(identifier[:])
	if filepath.Clean(profile.BootstrapFile) == filepath.Join(directory, temporary) || filepath.Clean(expected.BootstrapFile) == filepath.Join(directory, temporary) {
		return fmt.Errorf("signing profile storage must differ from bootstrap files")
	}
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create private replacement signing profile")
	}
	created, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("cannot inspect created signing profile")
	}
	success := false
	defer func() {
		if !success {
			removeCreatedFile(root, temporary, created)
		}
	}()
	data := append(encoded, '\n')
	written, writeErr := file.Write(data)
	info, statErr := file.Stat()
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(data) || statErr != nil || !safeReplacementFile(info) || !os.SameFile(created, info) || syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot save private replacement signing profile")
	}
	if err := publishReplacement(root, directory, path, directoryInfo, targetInfo, temporary, created, expected, profile); err != nil {
		return err
	}
	success = true
	return nil
}

func safeReplacementFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && privateOwner(info) == nil
}

func replacementDirectory(root *os.Root, directory, path string, expected os.FileInfo) error {
	current, err := os.Lstat(directory)
	opened, openedErr := root.Stat(".")
	if err != nil || openedErr != nil || !os.SameFile(expected, current) || !os.SameFile(expected, opened) || bootstrapParents(path, false) != nil || privateDirectory(directory) != nil {
		return fmt.Errorf("signing profile storage changed during replacement")
	}
	return nil
}

func matchingReplacementProfile(root *os.Root, name string, expected Profile) (os.FileInfo, error) {
	unsafe := fmt.Errorf("existing signing profile is missing, unsafe or invalid")
	before, err := root.Lstat(name)
	if err != nil || !safeReplacementFile(before) || before.Size() <= 0 || before.Size() > 8192 {
		return nil, unsafe
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, unsafe
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !safeReplacementFile(opened) {
		return nil, unsafe
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		return nil, unsafe
	}
	after, statErr := file.Stat()
	current, currentErr := root.Lstat(name)
	if statErr != nil || currentErr != nil || !os.SameFile(before, after) || !os.SameFile(before, current) || !safeReplacementFile(after) || !safeReplacementFile(current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, unsafe
	}
	var profile Profile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&profile) != nil {
		return nil, unsafe
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || profile.Validate() != nil {
		return nil, unsafe
	}
	if profile != expected {
		return nil, fmt.Errorf("existing signing profile changed; repeat signing setup")
	}
	return current, nil
}

func publishReplacement(root *os.Root, directory, path string, directoryInfo, targetInfo os.FileInfo, temporary string, created os.FileInfo, expected, replacement Profile) error {
	if replacementDirectory(root, directory, path, directoryInfo) != nil {
		return fmt.Errorf("signing profile storage changed during replacement")
	}
	pending, err := matchingReplacementProfile(root, temporary, replacement)
	if err != nil || !os.SameFile(created, pending) {
		return fmt.Errorf("replacement signing profile changed before publication")
	}
	current, err := matchingReplacementProfile(root, filepath.Base(path), expected)
	if err != nil {
		return err
	}
	if !os.SameFile(targetInfo, current) {
		return fmt.Errorf("existing signing profile changed; repeat signing setup")
	}
	if err := root.Rename(temporary, filepath.Base(path)); err != nil {
		return fmt.Errorf("cannot replace private signing profile")
	}
	return nil
}

// AcquireSetup owns the whole profile onboarding/configuration transaction.
// Keep the returned nonblocking exclusive lease until storage and cleanup finish.
// Its empty lock file remains in place so every caller uses the same inode.
func AcquireSetup(directory, name string) (*os.File, error) {
	if name == "" {
		name = "default"
	}
	if _, err := ProfilePath("", name); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, fmt.Errorf("signing setup storage must use an absolute canonical directory")
	}
	path := filepath.Join(directory, "signing-setup-"+name+".lock")
	if err := bootstrapParents(path, true); err != nil {
		return nil, fmt.Errorf("signing setup storage must use owned private directories outside repositories without symlinks")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect private signing setup storage")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot open private signing setup storage")
	}
	defer root.Close()
	openedDirectory, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, openedDirectory) || privateDirectory(directory) != nil {
		return nil, fmt.Errorf("signing setup storage changed while opening")
	}
	base := filepath.Base(path)
	created, err := root.OpenFile(base, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		if created.Close() != nil {
			return nil, fmt.Errorf("cannot prepare signing setup lock")
		}
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("cannot prepare signing setup lock")
	}
	expected, err := root.Lstat(base)
	if err != nil || !expected.Mode().IsRegular() || expected.Mode().Perm() != 0600 || privateOwner(expected) != nil {
		return nil, fmt.Errorf("signing setup lock must be a private regular file owned by this user without links")
	}
	lease, err := filelock.Acquire(path)
	if err != nil {
		return nil, fmt.Errorf("signing setup/configuration lease is unavailable; another operation may hold this profile")
	}
	opened, statErr := lease.Stat()
	current, currentErr := root.Lstat(base)
	currentDirectory, directoryErr := os.Lstat(directory)
	if statErr != nil || currentErr != nil || directoryErr != nil || !os.SameFile(expected, opened) || !os.SameFile(opened, current) || !os.SameFile(directoryInfo, currentDirectory) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || privateOwner(opened) != nil {
		lease.Close()
		return nil, fmt.Errorf("signing setup lock changed while opening")
	}
	return lease, nil
}

func removeCreatedFile(root *os.Root, name string, created os.FileInfo) {
	current, err := root.Lstat(name)
	if err == nil && os.SameFile(created, current) {
		root.Remove(name)
	}
}
