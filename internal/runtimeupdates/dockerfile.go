package runtimeupdates

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimepins"
)

// DockerfileUpdate holds the reviewed original and exactly four replacements.
// It never writes until Apply is called after runtime validation.
type DockerfileUpdate struct {
	directory             string
	directoryInfo         os.FileInfo
	originalInfo          os.FileInfo
	original, replacement []byte
}

var selectedDockerfileArgument = regexp.MustCompile(`^ARG[ \t]+(CODEX_VERSION|CLAUDE_VERSION|SKILLS_REVISION|AGENTS_REVISION)=(.*)$`)

func PrepareAgentToolDockerfile(source string, original []byte, pins runtimepins.Pins) (*DockerfileUpdate, error) {
	if err := pins.Validate(); err != nil {
		return nil, err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source checkout must be a real directory without symlinks")
	}
	directory := filepath.Join(source, "runtime")
	root, directoryInfo, err := openDockerfileDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, data, err := readDockerfileTarget(root)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, original) {
		return nil, fmt.Errorf("source Dockerfile changed while planning; retry after reviewing the source changes")
	}
	lines := strings.Split(string(original), "\n")
	counts := map[string]int{}
	for index, line := range lines {
		match := selectedDockerfileArgument.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		counts[match[1]]++
		// Retain declaration spacing; change only the selected value.
		lines[index] = line[:strings.Index(line, "=")+1] + pins.Arguments[match[1]]
	}
	for _, argument := range agentToolArguments {
		if counts[argument] != 1 {
			return nil, fmt.Errorf("source Dockerfile must declare %s exactly once", argument)
		}
	}
	// The normal recipe validator also rejects malformed or indented alternate
	// selected definitions which must not survive beside the replacement.
	if _, err := runtimepins.ReadDockerfile(original); err != nil {
		return nil, err
	}
	return &DockerfileUpdate{directory: directory, directoryInfo: directoryInfo, originalInfo: info, original: append([]byte(nil), original...), replacement: []byte(strings.Join(lines, "\n"))}, nil
}

func openDockerfileDirectory(directory string) (*os.Root, os.FileInfo, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("source runtime directory must be a real directory without symlinks")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, nil, fmt.Errorf("source runtime directory changed")
	}
	return root, actual, nil
}

func readDockerfileTarget(root *os.Root) (os.FileInfo, []byte, error) {
	info, err := root.Lstat("Dockerfile")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, nil, fmt.Errorf("source Dockerfile must be a bounded regular file without symlinks")
	}
	file, err := root.Open("Dockerfile")
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, nil, fmt.Errorf("source Dockerfile changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, nil, fmt.Errorf("cannot read bounded source Dockerfile")
	}
	return actual, data, nil
}

func (update *DockerfileUpdate) Apply() error {
	root, directoryInfo, err := openDockerfileDirectory(update.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if !os.SameFile(update.directoryInfo, directoryInfo) {
		return fmt.Errorf("source runtime directory changed; Dockerfile not written")
	}
	temporary := ".sdlc-Dockerfile-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(update.replacement); err == nil {
		err = file.Chmod(update.originalInfo.Mode())
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Recheck immediately before replacement; never overwrite edits made during
	// metadata lookup or the runtime build, even if the old inode was reused.
	info, data, err := readDockerfileTarget(root)
	if err != nil {
		return err
	}
	if !os.SameFile(update.originalInfo, info) || info.Mode() != update.originalInfo.Mode() || !bytes.Equal(data, update.original) {
		return fmt.Errorf("source Dockerfile changed during update; Dockerfile not written")
	}
	currentDirectory, directoryErr := os.Lstat(update.directory)
	if directoryErr != nil || !os.SameFile(update.directoryInfo, currentDirectory) {
		return fmt.Errorf("source runtime directory changed; Dockerfile not written")
	}
	if bytes.Equal(update.original, update.replacement) {
		return nil
	}
	return root.Rename(temporary, "Dockerfile")
}
