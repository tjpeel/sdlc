package runtimeimage

import (
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var runtimeAssets = []string{
	"Dockerfile", ".dockerignore", "entrypoint.py", "dependencies.py", "sshd_config",
	"bin/sdlc-job", "bin/ssh-sign-file", "bin/sdlc_agents.py", "bin/ticket_input.py",
}

var publisherEmbeds = map[string]bool{
	"internal/githubauth/container.py":   true,
	"internal/providerauth/container.py": true,
	"internal/providerauth/headless.py":  true,
	"internal/instructions/default.md":   true,
}

const maximumBuildFile = 2 * 1024 * 1024
const maximumBuildContext = 16 * 1024 * 1024
const modulePrefix = "github.com/tjpeel/sdlc/"

// buildContext copies a bounded source closure to a private temporary directory.
// Docker never receives the checkout, host settings, logs or credential caches.
func buildContext(source string) (directory string, cleanup func(), err error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", nil, fmt.Errorf("cannot open runtime source")
	}
	defer root.Close()
	directory, err = os.MkdirTemp("", "sdlc-runtime-context-")
	if err != nil {
		return "", nil, fmt.Errorf("cannot create private runtime build context")
	}
	contextPath := directory
	cleanup = func() { os.RemoveAll(contextPath) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if os.MkdirAll(filepath.Join(directory, "local-catalogues"), 0700) != nil || os.WriteFile(filepath.Join(directory, "local-catalogues", ".keep"), nil, 0600) != nil {
		return "", cleanup, fmt.Errorf("cannot prepare local catalogue directory")
	}
	var total int64
	copied := map[string]bool{}
	copyFile := func(relative, target string) ([]byte, error) {
		if copied[relative] {
			return nil, nil
		}
		if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, "..") {
			return nil, fmt.Errorf("invalid runtime build source path")
		}
		// Lstat each component so even an in-tree link cannot add unexpected data.
		parts := strings.Split(relative, string(filepath.Separator))
		for index := range parts {
			path := filepath.Join(parts[:index+1]...)
			info, checkErr := root.Lstat(path)
			if checkErr != nil || info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) {
				return nil, fmt.Errorf("runtime build sources must use regular files and directories without symlinks")
			}
		}
		info, checkErr := root.Lstat(relative)
		if checkErr != nil || !info.Mode().IsRegular() || info.Size() > maximumBuildFile {
			return nil, fmt.Errorf("runtime build source is missing, special or too large")
		}
		file, openErr := root.Open(relative)
		if openErr != nil {
			return nil, fmt.Errorf("cannot open runtime build source")
		}
		defer file.Close()
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return nil, fmt.Errorf("runtime build source changed while opening")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maximumBuildFile+1))
		if readErr != nil || len(data) > maximumBuildFile {
			return nil, fmt.Errorf("runtime build source is unreadable or too large")
		}
		total += int64(len(data))
		if total > maximumBuildContext {
			return nil, fmt.Errorf("runtime build context exceeds size limit")
		}
		destination := filepath.Join(directory, target)
		if os.MkdirAll(filepath.Dir(destination), 0700) != nil || os.WriteFile(destination, data, 0600) != nil {
			return nil, fmt.Errorf("cannot prepare private runtime build source")
		}
		copied[relative] = true
		return data, nil
	}
	for _, asset := range runtimeAssets {
		if _, err = copyFile(filepath.Join("runtime", asset), asset); err != nil {
			return "", cleanup, err
		}
	}
	if _, err = copyFile("go.mod", "source/go.mod"); err != nil {
		return "", cleanup, err
	}
	pending := []string{"cmd/sdlc-publisher"}
	visited := map[string]bool{}
	for len(pending) > 0 {
		pkg := pending[0]
		pending = pending[1:]
		if visited[pkg] {
			continue
		}
		visited[pkg] = true
		info, checkErr := root.Lstat(pkg)
		if checkErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", cleanup, fmt.Errorf("publisher source package is missing or unsafe")
		}
		folder, openErr := root.Open(pkg)
		if openErr != nil {
			return "", cleanup, fmt.Errorf("cannot open publisher source package")
		}
		entries, readErr := folder.ReadDir(-1)
		folder.Close()
		if readErr != nil {
			return "", cleanup, fmt.Errorf("cannot list publisher source package")
		}
		count := 0
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			relative := filepath.Join(pkg, entry.Name())
			data, copyErr := copyFile(relative, filepath.Join("source", relative))
			if copyErr != nil {
				return "", cleanup, copyErr
			}
			count++
			parsed, parseErr := parser.ParseFile(token.NewFileSet(), relative, data, parser.ImportsOnly|parser.ParseComments)
			if parseErr != nil {
				return "", cleanup, fmt.Errorf("cannot parse publisher source imports")
			}
			for _, imported := range parsed.Imports {
				value, decodeErr := strconv.Unquote(imported.Path.Value)
				if decodeErr != nil {
					return "", cleanup, fmt.Errorf("invalid publisher source import")
				}
				if strings.HasPrefix(value, modulePrefix) {
					local := strings.TrimPrefix(value, modulePrefix)
					if !strings.HasPrefix(local, "internal/") || filepath.Clean(local) != local || strings.Contains(local, "..") {
						return "", cleanup, fmt.Errorf("unsupported publisher source import")
					}
					pending = append(pending, filepath.FromSlash(local))
				} else if strings.Contains(strings.Split(value, "/")[0], ".") {
					return "", cleanup, fmt.Errorf("publisher build requires an unsupported external module")
				}
			}
			// ParseFile ImportsOnly omits later comments; scan only exact literal embed
			// directives, allowing the repository's known public embedded helpers.
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "//go:embed ") {
					continue
				}
				directive := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//go:embed "))
				for _, name := range strings.Fields(directive) {
					embedded := filepath.ToSlash(filepath.Join(pkg, name))
					if !publisherEmbeds[embedded] {
						return "", cleanup, fmt.Errorf("publisher source uses an unsupported embedded asset")
					}
					if _, copyErr = copyFile(filepath.FromSlash(embedded), filepath.Join("source", filepath.FromSlash(embedded))); copyErr != nil {
						return "", cleanup, copyErr
					}
				}
			}
		}
		if count == 0 {
			return "", cleanup, fmt.Errorf("publisher source package has no Go files")
		}
	}
	return directory, cleanup, nil
}
