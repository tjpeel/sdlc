package runtimeimage

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var runtimeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func ValidateName(name string) error {
	if name == "" {
		return nil
	}
	if !runtimeName.MatchString(name) || name == "local" || strings.HasPrefix(name, "build-") {
		return fmt.Errorf("runtime name must be 1-48 lowercase letters, digits or hyphens, begin with a letter, and cannot be local or build-*")
	}
	return nil
}
func (manager Manager) ImageName() string {
	if manager.Name == "" {
		return Image
	}
	return "sdlc:" + manager.Name
}
func (manager Manager) statePath() (string, error) {
	if err := ValidateName(manager.Name); err != nil {
		return "", err
	}
	name := "runtime.json"
	if manager.Name != "" {
		name = "runtime." + manager.Name + ".json"
	}
	return filepath.Join(manager.Directory, name), nil
}
