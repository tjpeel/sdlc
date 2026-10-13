package workrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
)

// Candidate discovery reads Git objects, never the provider's working files,
// check outputs, selected private inputs or local .sdlc settings.
func resolveCandidateChecks(ctx context.Context, j *Journal, revision Revision) error {
	if j.Plan.CheckSelection != "runtime" {
		return nil
	}
	j.EffectiveChecks = nil
	root, err := os.MkdirTemp("", "sdlc-check-discovery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	tree, err := SafeGit(ctx, j.Workspace, "ls-tree", "-r", "-z", revision.Head)
	if err != nil {
		return err
	}
	hasDotnetTarget := false
	for _, entry := range strings.Split(tree, "\x00") {
		_, path, ok := strings.Cut(entry, "\t")
		if !ok || strings.Contains(path, "/") {
			continue
		}
		switch filepath.Ext(path) {
		case ".sln", ".slnx", ".csproj", ".fsproj":
			hasDotnetTarget = true
		}
	}
	total := 0
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		metadata, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || !sourceRelative(path) {
			return fmt.Errorf("candidate discovery requires regular committed source")
		}
		if !discoveryMetadataForProject(path, hasDotnetTarget) || strings.HasPrefix(path, ".sdlc/") || excluded(path) {
			continue
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			return fmt.Errorf("candidate discovery metadata must be regular committed files")
		}
		if discoveryPresenceOnly(path) {
			target := filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(target, nil, 0600); err != nil {
				return err
			}
			continue
		}
		sizeText, err := SafeGit(ctx, j.Workspace, "cat-file", "-s", fields[2])
		if err != nil {
			return err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil || size < 0 || size > 64*1024 {
			return fmt.Errorf("candidate discovery metadata exceeds 64 KiB")
		}
		// No checkout filter, hook, or project script runs.
		data, err := SafeGit(ctx, j.Workspace, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		total += len(data)
		if total > 4*1024*1024 {
			return fmt.Errorf("candidate discovery metadata exceeds 4 MiB")
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(data), 0600); err != nil {
			return err
		}
	}
	discovered, err := project.DiscoverChecks(root)
	if err != nil {
		return err
	}
	if discovered.DockerTests && !j.Plan.DockerTests {
		return fmt.Errorf("candidate checks require Docker capability absent from this frozen run; start a new run with Docker checks enabled")
	}
	j.EffectiveChecks = discovered.Checks
	return nil
}

func discoveryMetadataForProject(path string, dotnet bool) bool {
	if !discoveryMetadata(path) {
		return false
	}
	if !dotnet {
		name := strings.ToLower(path)
		if strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".fsproj") || strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") || filepath.Base(name) == "directory.build.props" || strings.HasPrefix(name, ".github/workflows/") {
			return false
		}
		switch name {
		case "readme.md", "contributing.md", "global.json", ".config/dotnet-tools.json", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
			return false
		}
	}
	return true
}

func discoveryMetadata(path string) bool {
	name := strings.ToLower(path)
	if strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".fsproj") || strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") || filepath.Base(name) == "directory.build.props" {
		return true
	}
	if strings.HasPrefix(name, ".github/workflows/") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
		return true
	}
	if name == ".config/dotnet-tools.json" {
		return true
	}
	switch name {
	case "go.mod", "go.work", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "pipfile", "poetry.lock", "uv.lock", "global.json", "contributing.md", "readme.md", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}

// These inputs select tools by name only. Their content is not parsed by
// discovery, and lockfiles may be substantially larger than parsed metadata.
func discoveryPresenceOnly(path string) bool {
	switch strings.ToLower(path) {
	case "go.mod", "go.work", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "pipfile", "poetry.lock", "uv.lock", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}
