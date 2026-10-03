package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func discover(root string) ([]string, [][]string, error) {
	tools := []string{}
	checks := [][]string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, errors.New("cannot inspect project manifests")
	}
	known := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "go.mod", "go.work", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "Pipfile", "poetry.lock", "uv.lock", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
			known[name] = true
		default:
			if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") || strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".fsproj") {
				known[name] = true
			}
		}
		if known[name] {
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return nil, nil, errors.New("project manifests must be regular files, not symlinks")
			}
			info, e := entry.Info()
			if e != nil || !info.Mode().IsRegular() {
				return nil, nil, errors.New("cannot inspect project manifest")
			}
			tools = append(tools, name)
		}
	}
	sort.Strings(tools)
	if known["go.mod"] {
		checks = append(checks, []string{"go", "test", "./..."}, []string{"go", "vet", "./..."})
	}
	if known["package.json"] {
		data, e := readOptional(filepath.Join(root, "package.json"))
		if e != nil {
			return nil, nil, e
		}
		var manifest struct {
			Scripts map[string]json.RawMessage `json:"scripts"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return nil, nil, errors.New("package.json must contain valid JSON to discover checks")
		}
		manager := "npm"
		count := 0
		for _, pair := range [][2]string{{"package-lock.json", "npm"}, {"npm-shrinkwrap.json", "npm"}, {"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}} {
			if known[pair[0]] {
				manager = pair[1]
				count++
			}
		}
		if count > 1 {
			manager = ""
		}
		if manager != "" {
			for _, script := range []string{"test", "lint", "build"} {
				var value string
				if raw, ok := manifest.Scripts[script]; ok && json.Unmarshal(raw, &value) == nil && value != "" {
					checks = append(checks, []string{manager, "run", script})
				}
			}
		}
	}
	return tools, checks, nil
}

var ticketName = regexp.MustCompile(`^[0-9]+-[^/]+\.md$`)

func validWorkReference(reference string) bool {
	return utf8.ValidString(reference) && reference != "" && reference != "." && reference != ".." &&
		!strings.ContainsAny(reference, "/\\") && !hasControl(reference)
}

func tickets(root string) ([]string, error) {
	result := []string{}
	work := filepath.Join(root, ".sdlc/work")
	entries, err := os.ReadDir(work)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, errors.New("cannot inspect local work directory")
	}
	for _, entry := range entries {
		ref := filepath.Join(work, entry.Name())
		if err := directoryOrMissing(ref); err != nil {
			return nil, err
		}
		ticketDir := filepath.Join(ref, "tickets")
		if err := directoryOrMissing(ticketDir); err != nil {
			return nil, err
		}
		files, err := os.ReadDir(ticketDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("cannot inspect ticket directory")
		}
		for _, file := range files {
			if err := regularOrMissing(filepath.Join(ticketDir, file.Name())); err != nil {
				return nil, err
			}
			if ticketName.MatchString(file.Name()) {
				relative := filepath.ToSlash(filepath.Join(".sdlc/work", entry.Name(), "tickets", file.Name()))
				if !validWorkReference(entry.Name()) || !utf8.ValidString(file.Name()) || !portablePath(file.Name()) {
					return nil, errors.New("ticket paths require a valid work reference and safe filenames")
				}
				result = append(result, relative)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}
