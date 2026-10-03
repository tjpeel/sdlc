// Package instructions manages private, installation-wide agent instructions.
package instructions

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const maximumSize = 16 * 1024

//go:embed default.md
var defaultContent []byte

type Manager struct {
	Directory string
}

func New() (Manager, error) {
	directory := os.Getenv("SDLC_STATE_DIR")
	if directory == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Manager{}, err
		}
		directory = filepath.Join(base, "sdlc")
	}
	return Manager{Directory: directory}, nil
}

// Show returns the mandatory pause rule followed by any custom additions.
func (manager Manager) Show() ([]byte, error) {
	if err := manager.directory(false); errors.Is(err, os.ErrNotExist) {
		return append([]byte(nil), defaultContent...), nil
	} else if err != nil {
		return nil, err
	}
	custom, err := readRegular(manager.path(), "stored instructions")
	if errors.Is(err, os.ErrNotExist) {
		return append([]byte(nil), defaultContent...), nil
	}
	if err != nil {
		return nil, err
	}
	if err := validate(custom); err != nil {
		return nil, err
	}
	result := append([]byte(nil), defaultContent...)
	if len(custom) != 0 {
		result = append(result, '\n')
		result = append(result, custom...)
	}
	return result, nil
}

// Set snapshots a local UTF-8 file. Later source edits do not change the setting.
func (manager Manager) Set(sourceFile string) error {
	custom, err := readRegular(sourceFile, "instruction source")
	if err != nil {
		return err
	}
	if err := validate(custom); err != nil {
		return err
	}
	if err := manager.directory(true); err != nil {
		return err
	}
	lock, err := manager.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := regularOrMissing(manager.path(), "stored instructions"); err != nil {
		return err
	}
	file, err := os.CreateTemp(manager.Directory, ".instructions-*")
	if err != nil {
		return fmt.Errorf("cannot prepare instructions: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("cannot protect instructions: %w", err)
	}
	if _, err := file.Write(custom); err != nil {
		return fmt.Errorf("cannot write instructions: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cannot sync instructions: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close instructions: %w", err)
	}
	// Recheck before replacement; never accept a symlink as a settings file.
	if err := regularOrMissing(manager.path(), "stored instructions"); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), manager.path()); err != nil {
		return fmt.Errorf("cannot replace instructions: %w", err)
	}
	return nil
}

// Reset removes custom additions while retaining the mandatory pause rule.
func (manager Manager) Reset() error {
	if err := manager.directory(false); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := manager.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := regularOrMissing(manager.path(), "stored instructions"); err != nil {
		return err
	}
	if err := os.Remove(manager.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot reset instructions: %w", err)
	}
	return nil
}

func (manager Manager) path() string {
	return filepath.Join(manager.Directory, "instructions.md")
}

func (manager Manager) directory(create bool) error {
	if manager.Directory == "" {
		return errors.New("instruction state directory is not configured")
	}
	info, err := os.Lstat(manager.Directory)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(manager.Directory, 0700); err != nil {
			return fmt.Errorf("cannot create instruction state directory: %w", err)
		}
		info, err = os.Lstat(manager.Directory)
	}
	if err != nil {
		return fmt.Errorf("cannot inspect instruction state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("instruction state directory must be a directory, not a symlink")
	}
	return nil
}

func (manager Manager) lock() (*os.File, error) {
	path := filepath.Join(manager.Directory, "instructions.lock")
	if err := regularOrMissing(path, "instruction lock"); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock instruction settings: %w", err)
	}
	info, err := lock.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || !info.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		lock.Close()
		return nil, errors.New("instruction lock changed while opening it")
	}
	return lock, nil
}

func regularOrMissing(path, label string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symlink", label)
	}
	return nil
}

func readRegular(path, label string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect %s: %w", label, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", label)
	}
	if before.Size() > maximumSize {
		return nil, errors.New("custom instructions exceed the 16 KiB limit")
	}
	file, err := openRegular(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", label, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%s changed while opening it", label)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumSize+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", label, err)
	}
	return data, nil
}

func validate(data []byte) error {
	if len(data) > maximumSize {
		return errors.New("custom instructions exceed the 16 KiB limit")
	}
	if !utf8.Valid(data) {
		return errors.New("custom instructions must be valid UTF-8")
	}
	for _, character := range string(data) {
		if unicode.IsControl(character) && character != '\t' && character != '\n' && character != '\r' {
			return errors.New("custom instructions contain unsupported control characters")
		}
	}
	return nil
}
