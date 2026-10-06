package runtimeimage

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maximumSkillsArchive = 16 * 1024 * 1024
const maximumSkillsEntries = 20000

type boundedArchive struct {
	file  *os.File
	bytes int64
}

func (w *boundedArchive) Write(data []byte) (int, error) {
	if w.bytes+int64(len(data)) > maximumSkillsArchive {
		return 0, errors.New("local skills archive exceeds 16 MiB")
	}
	n, err := w.file.Write(data)
	w.bytes += int64(n)
	return n, err
}
func catalogueGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
}
func cleanCatalogue(ctx context.Context, root, revision string) error {
	status, err := catalogueGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil || len(status) != 0 {
		return errors.New("local skills catalogue must have a clean committed working tree")
	}
	head, err := catalogueGit(ctx, root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != revision {
		return errors.New("local skills catalogue revision changed during snapshot")
	}
	return nil
}

// snapshotSkills includes only Git's committed archive, never the checkout or
// ignored/untracked account files. Check boundaries before any Docker operation.
func snapshotSkills(ctx context.Context, source, contextDirectory string) (string, string, error) {
	root, err := filepath.Abs(source)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	top, err := catalogueGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errors.New("local skills source must be a Git checkout root")
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil || actual != root {
		return "", "", errors.New("local skills source must match its Git checkout root")
	}
	head, err := catalogueGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", "", errors.New("local skills source must have a committed HEAD")
	}
	revision := strings.TrimSpace(string(head))
	if !sourceCommitID.MatchString(revision) {
		return "", "", errors.New("local skills source has an invalid commit identity")
	}
	if err := cleanCatalogue(ctx, root, revision); err != nil {
		return "", "", err
	}
	destination := filepath.Join(contextDirectory, "local-catalogues", "skills.tar")
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", "", err
	}
	archive := &boundedArchive{file: file}
	command := exec.CommandContext(ctx, "git", "-C", root, "archive", "--format=tar", revision)
	command.Stdout = archive
	runErr := command.Run()
	closeErr := file.Close()
	if runErr != nil || closeErr != nil {
		return "", "", errors.New("cannot create bounded committed skills archive")
	}
	if err := validateSkillsArchive(destination); err != nil {
		return "", "", err
	}
	if err := cleanCatalogue(ctx, root, revision); err != nil {
		return "", "", err
	}
	return root, revision, nil
}

type catalogueEntry struct {
	header tar.Header
	data   []byte
}

func cataloguePath(name string) error {
	if !utf8.ValidString(name) || name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
		return errors.New("local skills archive contains an unsafe path")
	}
	for _, part := range strings.Split(name, "/") {
		switch strings.ToLower(part) {
		case ".git", ".secrets", ".ssh", ".aws", ".codex", ".claude", "auth.json", "credentials":
			return errors.New("local skills archive contains authentication state")
		}
	}
	return nil
}

// Resolve using archive names, never the host filesystem. A link used as a
// directory component is rejected even when a later '..' would clean it away.
func catalogueLinkTarget(name, target string, entries map[string]*catalogueEntry) (string, error) {
	if !utf8.ValidString(target) || target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return "", errors.New("catalogue link target must stay within the committed archive")
	}
	var parts []string
	if dir := path.Dir(name); dir != "." {
		parts = strings.Split(dir, "/")
	}
	for _, part := range strings.Split(target, "/") {
		if part != "" && part != "." && part != ".." {
			if err := cataloguePath(part); err != nil {
				return "", err
			}
		}
		if entry := entries[strings.Join(parts, "/")]; entry != nil && entry.header.Typeflag != tar.TypeDir {
			return "", errors.New("catalogue link traverses a non-directory parent")
		}
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", errors.New("catalogue link escapes archive root")
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	resolved := strings.Join(parts, "/")
	if err := cataloguePath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}
func resolveCatalogueFile(name string, entries map[string]*catalogueEntry) (*catalogueEntry, error) {
	visited := map[string]bool{}
	for hop := 0; hop < 32; hop++ {
		if visited[name] {
			return nil, errors.New("catalogue links contain a cycle")
		}
		visited[name] = true
		entry := entries[name]
		if entry == nil {
			return nil, errors.New("catalogue link target is not a committed regular file")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if value := entries[parent]; value != nil && value.header.Typeflag == tar.TypeSymlink {
				return nil, errors.New("catalogue entry traverses a parent symlink")
			}
		}
		if entry.header.Typeflag == tar.TypeReg {
			return entry, nil
		}
		if entry.header.Typeflag != tar.TypeSymlink {
			return nil, errors.New("catalogue link target is not a committed regular file")
		}
		var err error
		name, err = catalogueLinkTarget(name, entry.header.Linkname, entries)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("catalogue link chain exceeds 32 entries")
}
func validateSkillsArchive(archive string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumSkillsArchive {
		file.Close()
		return errors.New("local skills archive exceeds its bounds")
	}
	reader := tar.NewReader(file)
	entries := map[string]*catalogueEntry{}
	var ordered []*catalogueEntry
	linked := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			file.Close()
			return errors.New("local skills archive is invalid")
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if len(ordered) >= maximumSkillsEntries {
			file.Close()
			return errors.New("local skills archive has too many entries")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if err := cataloguePath(name); err != nil {
			file.Close()
			return err
		}
		if entries[name] != nil {
			file.Close()
			return errors.New("local skills archive has duplicate paths")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeSymlink {
			file.Close()
			return errors.New("local skills archive must contain regular files, directories or bounded internal file references")
		}
		if header.Size < 0 || header.Size > maximumBuildFile {
			file.Close()
			return errors.New("local skills archive file exceeds 2 MiB")
		}
		copy := *header
		copy.Name = name
		entry := &catalogueEntry{header: copy}
		if header.Typeflag == tar.TypeReg {
			entry.data, err = io.ReadAll(io.LimitReader(reader, maximumBuildFile+1))
			if err != nil || int64(len(entry.data)) != header.Size {
				file.Close()
				return errors.New("local skills archive file is incomplete")
			}
		}
		if header.Typeflag == tar.TypeSymlink {
			linked = true
		}
		entries[name] = entry
		ordered = append(ordered, entry)
	}
	if err := file.Close(); err != nil {
		return err
	}
	for _, entry := range ordered {
		for parent := path.Dir(entry.header.Name); parent != "."; parent = path.Dir(parent) {
			if value := entries[parent]; value != nil && value.header.Typeflag != tar.TypeDir {
				return errors.New("catalogue entry has a non-directory parent")
			}
		}
		if entry.header.Typeflag == tar.TypeSymlink {
			if _, err := resolveCatalogueFile(entry.header.Name, entries); err != nil {
				return err
			}
		}
	}
	if _, err := resolveCatalogueFile("scripts/install-skills", entries); err != nil {
		return errors.New("local skills source lacks committed scripts/install-skills")
	}
	if !linked {
		return nil
	}
	materialized, err := os.CreateTemp(filepath.Dir(archive), "skills-materialized-")
	if err != nil {
		return err
	}
	defer os.Remove(materialized.Name())
	writer := tar.NewWriter(&boundedArchive{file: materialized})
	for _, entry := range ordered {
		original := entry
		if entry.header.Typeflag == tar.TypeSymlink {
			entry, err = resolveCatalogueFile(entry.header.Name, entries)
			if err != nil {
				materialized.Close()
				return err
			}
		}
		header := tar.Header{Name: original.header.Name, Mode: entry.header.Mode, Typeflag: entry.header.Typeflag, Size: int64(len(entry.data)), ModTime: entry.header.ModTime}
		if err := writer.WriteHeader(&header); err != nil {
			materialized.Close()
			return errors.New("materialized local skills archive exceeds its bounds")
		}
		if _, err := writer.Write(entry.data); err != nil {
			materialized.Close()
			return errors.New("materialized local skills archive exceeds its bounds")
		}
	}
	if err := writer.Close(); err != nil {
		materialized.Close()
		return errors.New("materialized local skills archive exceeds its bounds")
	}
	if err := materialized.Sync(); err != nil {
		materialized.Close()
		return err
	}
	if err := materialized.Close(); err != nil {
		return err
	}
	return os.Rename(materialized.Name(), archive)
}

func localSkillsRecipe(recipe []byte) ([]byte, error) {
	text := string(recipe)
	if !strings.Contains(text, "ARG LOCAL_SKILLS=0\n") || !strings.Contains(text, "/tmp/sdlc-local-catalogues/skills.tar") {
		return nil, fmt.Errorf("runtime source does not support local skills archives")
	}
	return []byte(strings.Replace(text, "ARG LOCAL_SKILLS=0\n", "ARG LOCAL_SKILLS=1\n", 1)), nil
}
