//go:build linux || darwin

package providerauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tjpeel/sdlc/internal/filelock"
)

// Each random invocation owns one inode until its container cleanup completes.
// This records liveness without reserving a provider or relying on reusable PIDs.
func (manager Manager) registerOperation(name, volume, provider, image string) (*os.File, error) {
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-auth-"), "-"+provider)
	path := filepath.Join(manager.Runtime.Directory, name+".operation")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot register provider operation")
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock provider operation")
	}
	record := labels(installation, provider)
	record["image"] = image
	record["name"] = name
	if err = json.NewEncoder(file).Encode(record); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot save provider operation")
	}
	return file, nil
}

func (manager Manager) liveOperation(name, installation, provider, image string) bool {
	prefix := ""
	for _, candidate := range []string{"sdlc-auth-", "sdlc-headless-", "sdlc-interactive-"} {
		if strings.HasPrefix(name, candidate) {
			prefix = candidate
		}
	}
	if prefix == "" || !installationID.MatchString(strings.TrimPrefix(name, prefix)) {
		return false
	}
	path := filepath.Join(manager.Runtime.Directory, name+".operation")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return false
	}
	descriptor, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	var record map[string]string
	if json.NewDecoder(file).Decode(&record) != nil {
		return false
	}
	if record["name"] != name || record["image"] != image {
		return false
	}
	for key, value := range labels(installation, provider) {
		if record[key] != value {
			return false
		}
	}
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, filelock.ErrBusy)
}

func validOperationDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
