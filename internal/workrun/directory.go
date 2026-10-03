package workrun

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var runIdentifier = regexp.MustCompile(`^[0-9a-f]{24}$`)

func RunDirectory(root, reference, ticket, id string, create bool) (string, error) {
	if !runIdentifier.MatchString(id) || filepath.Base(ticket) != ticket || !safeRelative(reference) || strings.Contains(reference, "/") {
		return "", fmt.Errorf("invalid selected run identity")
	}
	path := root
	for _, part := range []string{".sdlc", "work", reference, "runs", strings.TrimSuffix(ticket, ".md"), id} {
		if err := realDirectory(path); err != nil {
			return "", err
		}
		path = filepath.Join(path, part)
		if info, err := os.Lstat(path); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("private run paths must be real directories")
			}
		} else if os.IsNotExist(err) && create {
			if err := os.Mkdir(path, 0700); err != nil {
				return "", err
			}
		} else {
			return "", fmt.Errorf("selected run directory is unavailable")
		}
		if create && (part == "runs" || part == strings.TrimSuffix(ticket, ".md") || part == id) {
			if err := os.Chmod(path, 0700); err != nil {
				return "", err
			}
		}
	}
	return path, nil
}
