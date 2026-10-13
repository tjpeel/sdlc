//go:build windows

package providerauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func (manager Manager) registerOperation(name, volume, provider, image string) (*os.File, error) {
	path := filepath.Join(manager.Runtime.Directory, name+".operation")
	created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot register provider operation")
	}
	before, err := created.Stat()
	created.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect provider operation")
	}
	file, err := filelock.Acquire(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock provider operation")
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		file.Close()
		return nil, fmt.Errorf("provider operation changed")
	}
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-auth-"), "-"+provider)
	record := labels(installation, provider)
	record["image"], record["name"] = image, name
	if _, err := file.Seek(1, 0); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot position provider operation")
	}
	if json.NewEncoder(file).Encode(record) != nil {
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
	if err != nil || !before.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		return false
	}
	if _, err := file.Seek(1, 0); err != nil {
		return false
	}
	var record map[string]string
	if json.NewDecoder(file).Decode(&record) != nil || record["name"] != name || record["image"] != image {
		return false
	}
	for key, value := range labels(installation, provider) {
		if record[key] != value {
			return false
		}
	}
	probe, err := filelock.Acquire(path)
	if probe != nil {
		probe.Close()
	}
	after, checkErr := os.Lstat(path)
	return checkErr == nil && os.SameFile(before, after) && errors.Is(err, filelock.ErrBusy)
}

func validOperationDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
