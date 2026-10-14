package providerauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateDiagnosticMounts(request HeadlessRequest) error {
	paths := []string{request.DiagnosticHelper, request.DiagnosticRequests, request.DiagnosticResponses}
	if paths[0] == "" && paths[1] == "" && paths[2] == "" {
		return nil
	}
	if request.ReadOnly {
		return fmt.Errorf("review sessions cannot mount diagnostic channels")
	}
	for i, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return fmt.Errorf("diagnostic mount paths must be absolute and clean")
		}
		resolved, err := filepath.EvalSymlinks(path)
		info, statErr := os.Lstat(path)
		if err != nil || statErr != nil || resolved != path || info.Mode()&os.ModeSymlink != 0 || (i == 0 && (!info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || !diagnosticHelperUnlinked(info))) || (i > 0 && !info.IsDir()) {
			return fmt.Errorf("diagnostic mounts require an unlinked readonly helper and real directories")
		}
		for _, protected := range []string{request.Workspace, request.SessionDirectory} {
			relative, err := filepath.Rel(protected, path)
			if err != nil || relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..") {
				return fmt.Errorf("diagnostic channel must be outside workspace and native session storage")
			}
		}
		for k, other := range paths {
			if k == i || other == "" {
				continue
			}
			relative, _ := filepath.Rel(other, path)
			if relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..") {
				return fmt.Errorf("diagnostic mounts must be separate")
			}
		}
	}
	return nil
}
