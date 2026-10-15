package workrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StoreLocalPRFeedback retains a private, content-addressed source alongside the
// journal. The journal owns dispatch/recovery; this file is the user's source copy.
func StoreLocalPRFeedback(runDir, text string) (string, error) {
	if err := ValidateLocalPRFeedback(text); err != nil {
		return "", err
	}
	directory := filepath.Join(runDir, "feedback")
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", fmt.Errorf("feedback history must be a private real directory")
	}
	path := filepath.Join(directory, strings.TrimPrefix(localFeedbackID(text), "local:")+".txt")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(text)) {
			return "", fmt.Errorf("retained local feedback is unsafe or changed")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if string(data) != text {
			return "", fmt.Errorf("retained local feedback differs")
		}
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if _, err = io.WriteString(file, text); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
