// Package project inspects a checkout and prepares portable local SDLC settings.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const ConfigPath = ".sdlc/project.json"
const maximumSize = 64 * 1024

type Config struct {
	Version    int        `json:"version"`
	Checks     [][]string `json:"checks"`
	InputFiles []string   `json:"input_files"`
}
type Remote struct{ Name, Identity string }
type Result struct {
	Root, Branch, Head       string
	Detached, Dirty          bool
	Remotes                  []Remote
	Tools, Tickets, Warnings []string
	Config                   Config
	ConfigCreated            bool
}

// Initialize inspects local files only; suggested checks are never executed.
func Initialize(ctx context.Context, directory string) (Result, error) {
	var r Result
	if err := ctx.Err(); err != nil {
		return r, err
	}
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		return r, errors.New("run sdlc init inside a non-bare Git checkout")
	}
	r.Root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return r, errors.New("cannot resolve the Git checkout directory")
	}
	r.Head, err = git(ctx, r.Root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		r.Head = ""
		r.Warnings = append(r.Warnings, "The checkout has no commits yet; make a first commit before launching work.")
	}
	r.Head = strings.TrimSpace(r.Head)
	branch, err := git(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	r.Detached = err != nil
	r.Branch = strings.TrimSpace(branch)
	r.Dirty, err = dirty(ctx, r.Root, r.Head)
	if err != nil {
		return r, err
	}
	names, err := git(ctx, r.Root, "remote")
	if err != nil {
		return r, err
	}
	for _, name := range strings.Fields(names) {
		raw, e := git(ctx, r.Root, "remote", "get-url", "--all", name)
		if e != nil {
			return r, e
		}
		for _, address := range strings.Split(strings.TrimSpace(raw), "\n") {
			r.Remotes = append(r.Remotes, Remote{Name: safeName(name), Identity: remoteIdentity(address)})
		}
	}
	if len(r.Remotes) > 1 {
		r.Warnings = append(r.Warnings, "Multiple remote identities found; no remote was selected.")
	}
	r.Tools, r.Config.Checks, err = discover(r.Root)
	if err != nil {
		return r, err
	}
	r.Config.Version = 1
	r.Config.InputFiles = []string{}
	exclude, err := git(ctx, r.Root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return r, err
	}
	exclude = strings.TrimSpace(exclude)
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(r.Root, exclude)
	}
	exclude = filepath.Clean(exclude)
	lockPath := filepath.Join(filepath.Dir(exclude), "sdlc-init.lock")
	preflight := func() error {
		for _, path := range []string{filepath.Join(r.Root, ".sdlc"), filepath.Join(r.Root, ".sdlc/work"), filepath.Dir(exclude)} {
			if e := directoryOrMissing(path); e != nil {
				return e
			}
		}
		for _, path := range []string{filepath.Join(r.Root, ConfigPath), exclude, lockPath} {
			if e := regularOrMissing(path); e != nil {
				return e
			}
		}
		tracked, e := git(ctx, r.Root, "ls-files", "-z", "--", ":(icase).sdlc/work")
		if e != nil {
			return e
		}
		if tracked != "" {
			return errors.New(".sdlc/work contains tracked or staged files; remove them from the Git index before initialization")
		}
		r.Tickets, e = tickets(r.Root)
		if e != nil {
			return e
		}
		if _, e = readOptional(exclude); e != nil {
			return e
		}
		if e = verifyIgnoreCandidate(ctx, r.Root); e != nil {
			return e
		}
		existing, e := readOptional(filepath.Join(r.Root, ConfigPath))
		if e != nil {
			return e
		}
		if existing != nil {
			if e = decodeConfig(existing, &r.Config); e != nil {
				return e
			}
			for _, input := range r.Config.InputFiles {
				path := filepath.Join(r.Root, filepath.FromSlash(input))
				if e = directoryOrMissing(filepath.Dir(path)); e != nil {
					return e
				}
				if e = regularOrMissing(path); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if err = preflight(); err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	// Git creates this metadata directory in normal checkouts. Never follow a
	// replacement symlink to create it or acquire the predictable lock.
	if err = os.MkdirAll(filepath.Dir(exclude), 0755); err != nil {
		return r, errors.New("cannot create Git exclude directory")
	}
	if err = regularOrMissing(lockPath); err != nil {
		return r, err
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return r, errors.New("cannot lock project initialization; another operation may be running")
	}
	defer lock.Close()
	opened, e := lock.Stat()
	current, ce := os.Lstat(lockPath)
	if e != nil || ce != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return r, errors.New("initialization lock changed while opening")
	}
	if err = preflight(); err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	data, err := readOptional(exclude)
	if err != nil {
		return r, err
	}
	expectedExclude := append([]byte{}, data...)
	rule := "/.sdlc/work/"
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSuffix(line, "\r") == rule {
			found = true
		}
	}
	if !found {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, []byte(rule+"\n")...)
		if err = writeRegular(exclude, data, false, expectedExclude); err != nil {
			return r, err
		}
	}
	ignored, err := git(ctx, r.Root, "check-ignore", "--no-index", ".sdlc/work/")
	if err != nil || strings.TrimSpace(ignored) != ".sdlc/work/" {
		return r, errors.New("Git does not ignore .sdlc/work; check repository ignore rules")
	}
	for _, path := range []string{filepath.Join(r.Root, ".sdlc"), filepath.Join(r.Root, ".sdlc/work")} {
		if err = directoryOrMissing(path); err != nil {
			return r, err
		}
		if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return r, errors.New("cannot create project state directory")
		}
	}
	config := filepath.Join(r.Root, ConfigPath)
	if _, err = os.Lstat(config); errors.Is(err, os.ErrNotExist) {
		data, _ = json.MarshalIndent(r.Config, "", "  ")
		data = append(data, '\n')
		if err = writeRegular(config, data, true, nil); err != nil {
			return r, err
		}
		r.ConfigCreated = true
	} else if err != nil {
		return r, errors.New("cannot inspect project settings")
	}
	if len(r.Remotes) == 0 {
		r.Warnings = append(r.Warnings, "No Git remotes found.")
	}
	if len(r.Tickets) == 0 {
		r.Warnings = append(r.Warnings, "No local ticket paths found.")
	}
	if len(r.Config.Checks) == 0 {
		r.Warnings = append(r.Warnings, "No project checks configured; review project settings before launching work.")
	}
	return r, nil
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	args = append([]string{"-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = directory
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if !strings.HasPrefix(key, "GIT_") || key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("cannot inspect local Git state; check the checkout and Git installation")
	}
	return string(out), nil
}
func directoryOrMissing(path string) error {
	// Inspect every existing ancestor, including paths outside the checkout used
	// by linked-worktree metadata, before any filesystem write.
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect project directory")
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("project and Git metadata directories must be real directories, not symlinks")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func regularOrMissing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect project file")
	}
	if !info.Mode().IsRegular() {
		return errors.New("project settings, tickets and Git exclude files must be regular files, not symlinks")
	}
	return nil
}
func readOptional(path string) ([]byte, error) {
	if err := regularOrMissing(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot inspect project file")
	}
	if before.Size() > maximumSize {
		return nil, errors.New("project configuration or metadata exceeds 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read project file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("project file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maximumSize+1))
	if err != nil || len(data) > maximumSize {
		return nil, errors.New("cannot read bounded project file")
	}
	return data, nil
}
func writeRegular(path string, data []byte, exclusive bool, expected []byte) error {
	if err := directoryOrMissing(filepath.Dir(path)); err != nil {
		return err
	}
	if err := regularOrMissing(path); err != nil {
		return err
	}
	before, statErr := os.Lstat(path)
	original, err := readOptional(path)
	if err != nil {
		return err
	}
	if !exclusive && string(original) != string(expected) {
		return errors.New("Git excludes changed during initialization; retry to preserve edits")
	}
	mode := os.FileMode(0600)
	if statErr == nil {
		mode = before.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sdlc-init-*")
	if err != nil {
		return errors.New("cannot stage project file")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot stage complete project file")
	}
	if err = directoryOrMissing(filepath.Dir(path)); err != nil {
		return err
	}
	if exclusive {
		// A hard link publishes the complete file without replacing any concurrent
		// settings file. The staging name is removed by the deferred cleanup.
		if err = os.Link(f.Name(), path); err != nil {
			return errors.New("project settings appeared during initialization; retry to preserve them")
		}
		return nil
	}
	current, currentErr := os.Lstat(path)
	if statErr == nil && (currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(before, current)) {
		return errors.New("Git excludes changed during initialization")
	}
	if errors.Is(statErr, os.ErrNotExist) && !errors.Is(currentErr, os.ErrNotExist) {
		return errors.New("Git excludes appeared during initialization; retry")
	}
	now, err := readOptional(path)
	if err != nil || string(now) != string(original) {
		return errors.New("Git excludes changed during initialization")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot publish complete Git exclude file")
	}
	return nil
}
func decodeConfig(data []byte, c *Config) error {
	var decoded Config
	target := c
	c = &decoded
	if !utf8.Valid(data) {
		return errors.New("project settings must be UTF-8 JSON")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(c); err != nil {
		return errors.New("project settings must contain valid version 1 JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || c.Version != 1 || c.Checks == nil || c.InputFiles == nil || len(c.Checks) > 64 || len(c.InputFiles) > 256 {
		return errors.New("project settings require version 1 and bounded checks and input files")
	}
	for _, command := range c.Checks {
		if len(command) == 0 || len(command) > 64 {
			return errors.New("each project check needs a bounded nonempty argument list")
		}
		for _, arg := range command {
			if arg == "" || len(arg) > 4096 || hasControl(arg) {
				return errors.New("project check arguments must be nonempty and contain no control characters")
			}
		}
	}
	for _, path := range c.InputFiles {
		if !portablePath(path) {
			return errors.New("input_files must use relative paths without traversal or control characters")
		}
	}
	*target = decoded
	return nil
}
func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
func portablePath(s string) bool {
	if s == "" || len(s) > 4096 || hasControl(s) || strings.ContainsAny(s, "\\:") || strings.HasPrefix(s, "/") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func safeName(s string) string {
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(s) {
		return "remote (name omitted)"
	}
	return s
}
func remoteIdentity(raw string) string {
	if hasControl(raw) {
		return "remote (identity omitted)"
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.Scheme == "file" {
			return "local path (omitted)"
		}
		return u.Hostname() + strings.TrimSuffix(u.EscapedPath(), "/")
	}
	if i := strings.Index(raw, ":"); i > 0 && !strings.ContainsAny(raw[:i], "/\\") && !(i == 1) {
		host := raw[:i]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		path := strings.SplitN(strings.SplitN(raw[i+1:], "?", 2)[0], "#", 2)[0]
		return host + "/" + strings.TrimPrefix(path, "/")
	}
	return "local path (omitted)"
}

// A temporary global exclude lets Git evaluate the candidate rule without
// modifying repository files. Repository negations take precedence over it.
func verifyIgnoreCandidate(ctx context.Context, root string) error {
	f, err := os.CreateTemp("", "sdlc-init-exclude-*")
	if err != nil {
		return errors.New("cannot prepare ignore-rule verification")
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString("/.sdlc/work/\n"); err != nil {
		f.Close()
		return errors.New("cannot prepare ignore-rule verification")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot prepare ignore-rule verification")
	}
	output, err := git(ctx, root, "-c", "core.excludesFile="+f.Name(), "check-ignore", "--no-index", ".sdlc/work/")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || strings.TrimSpace(output) != ".sdlc/work/" {
		return errors.New("repository ignore rules re-include .sdlc/work; remove that override before initialization")
	}
	return nil
}

// dirty compares index metadata with filesystem stats, without normalizing or
// reading tracked file contents. Timestamp-only changes are conservatively dirty.
func dirty(ctx context.Context, root, head string) (bool, error) {
	index, err := git(ctx, root, "ls-files", "--cached", "--debug", "-z")
	if err != nil {
		return false, err
	}
	for index != "" {
		end := strings.IndexByte(index, 0)
		if end < 0 {
			return false, errors.New("cannot parse Git index metadata")
		}
		path := index[:end]
		index = index[end+1:]
		lines := make([]string, 5)
		for i := range lines {
			end = strings.IndexByte(index, '\n')
			if end < 0 {
				return false, errors.New("cannot parse Git index metadata")
			}
			lines[i] = index[:end]
			index = index[end+1:]
		}
		if !portablePath(filepath.ToSlash(path)) {
			return true, nil
		}
		info, err := os.Lstat(filepath.Join(root, path))
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, errors.New("cannot inspect tracked file metadata")
		}
		csec, cnsec, ok := parseIndexTime(lines[0], "ctime")
		msec, mnsec, mok := parseIndexTime(lines[1], "mtime")
		sizeFields := strings.Fields(lines[4])
		if len(sizeFields) < 2 || sizeFields[0] != "size:" || !ok || !mok {
			return false, errors.New("cannot parse Git index metadata")
		}
		size, err := strconv.ParseInt(sizeFields[1], 10, 64)
		if err != nil {
			return false, errors.New("cannot parse Git index size")
		}
		sec, nsec, supported := changeTime(info)
		if info.Size() != size || info.ModTime().Unix() != msec || int64(info.ModTime().Nanosecond()) != mnsec || !supported || sec != csec || nsec != cnsec {
			return true, nil
		}
	}
	untracked, err := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	if untracked != "" {
		return true, nil
	}
	var args []string
	if head == "" {
		args = []string{"ls-files", "--cached", "-z"}
	} else {
		args = []string{"diff-index", "--cached", "--name-only", "--no-ext-diff", "--no-textconv", "-z", "HEAD", "--"}
	}
	output, err := git(ctx, root, args...)
	return output != "", err
}
func parseIndexTime(line, key string) (int64, int64, bool) {
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != key+":" {
		return 0, 0, false
	}
	parts := strings.Split(fields[1], ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	sec, a := strconv.ParseInt(parts[0], 10, 64)
	nsec, b := strconv.ParseInt(parts[1], 10, 64)
	return sec, nsec, a == nil && b == nil
}

// FileInfo.Sys exposes platform metadata. Linux and Darwin name their change
// timestamp differently; platforms without it are conservatively dirty.
func changeTime(info os.FileInfo) (int64, int64, bool) {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, name := range []string{"Ctim", "Ctimespec"} {
		timestamp := value.FieldByName(name)
		if timestamp.IsValid() && timestamp.Kind() == reflect.Struct {
			sec, nsec := timestamp.FieldByName("Sec"), timestamp.FieldByName("Nsec")
			if sec.IsValid() && nsec.IsValid() && sec.CanInt() && nsec.CanInt() {
				return sec.Int(), nsec.Int(), true
			}
		}
	}
	return 0, 0, false
}

// GitHubRepository infers only a single distinct sanitized origin identity.
// SSH addresses provide metadata here, never authentication material.
func GitHubRepository(remotes []Remote) string {
	repository := ""
	for _, remote := range remotes {
		if remote.Name != "origin" {
			continue
		}
		parts := strings.Split(remote.Identity, "/")
		if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") {
			return ""
		}
		name := parts[1] + "/" + strings.TrimSuffix(parts[2], ".git")
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`).MatchString(name) || strings.HasSuffix(parts[1], "-") || strings.Contains(parts[1], "--") {
			return ""
		}
		if repository != "" && !strings.EqualFold(repository, name) {
			return ""
		}
		repository = name
	}
	return repository
}
