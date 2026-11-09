package runtimeimage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadDefaultName returns the runtime selected for new work. The empty name
// denotes the original local runtime; saved runs retain their own selection.
func ReadDefaultName(dir string) (string, error) {
	path := filepath.Join(dir, "runtime-default.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return "", fmt.Errorf("unsafe default runtime selection")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var selection struct {
		Version int    `json:"version"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(data, &selection); err != nil || selection.Version != 1 {
		return "", fmt.Errorf("invalid default runtime selection")
	}
	if err := ValidateName(selection.Name); err != nil {
		return "", err
	}
	return selection.Name, nil
}

// WriteDefaultName atomically saves a private selection for new work.
func WriteDefaultName(dir, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("runtime state directory must be private")
	}
	if _, err := ReadDefaultName(dir); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Version int    `json:"version"`
		Name    string `json:"name"`
	}{1, name})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".runtime-default-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "runtime-default.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
