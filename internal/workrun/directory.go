package workrun

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var runPrefix = regexp.MustCompile(`^[0-9a-f]{3,24}$`)

// ValidateRunSelector checks a public run ID or prefix without weakening canonical IDs.
func ValidateRunSelector(id string) error {
	if !runPrefix.MatchString(id) {
		return fmt.Errorf("run ID must be three to twenty-four lowercase hexadecimal characters")
	}
	return nil
}

// SelectRunID resolves only unambiguous prefixes and returns a canonical ID.
func SelectRunID(ids []string, prefix string) (string, error) {
	if err := ValidateRunSelector(prefix); err != nil {
		return "", err
	}
	var matches []string
	for _, id := range ids {
		if runIdentifier.MatchString(id) && strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no run matches %q", prefix)
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", fmt.Errorf("run prefix %q is ambiguous; candidates: %s; use a longer prefix", prefix, strings.Join(matches, ", "))
	}
	return matches[0], nil
}

// ResolveRunDirectory searches retained checkpoints in the selected ticket's namespace.
func ResolveRunDirectory(root, reference, ticket, prefix string) (string, error) {
	if err := ValidateRunSelector(prefix); err != nil {
		return "", err
	}
	if filepath.Base(ticket) != ticket || !safeRelative(reference) || strings.Contains(reference, "/") {
		return "", fmt.Errorf("invalid selected run identity")
	}
	parent := filepath.Join(root, ".sdlc", "work", reference, "runs", strings.TrimSuffix(ticket, ".md"))
	if err := realDirectory(parent); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, entry := range entries {
		if runIdentifier.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	id, err := SelectRunID(ids, prefix)
	if err != nil {
		return "", err
	}
	return RunDirectory(root, reference, ticket, id, false)
}

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
