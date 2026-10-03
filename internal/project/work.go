package project

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// WorkResult identifies local tickets without reading their contents.
type WorkResult struct {
	Root, Reference string
	Tickets         []string
}

// InspectWork finds the numbered Markdown tickets for an explicitly selected
// work reference. It does not initialize the project or execute any checks.
func InspectWork(ctx context.Context, directory, reference string) (WorkResult, error) {
	var result WorkResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !utf8.ValidString(reference) || !portablePath(reference) || strings.Contains(reference, "/") {
		return result, errors.New("work reference must be a nonempty portable directory name without traversal or control characters")
	}
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("run sdlc work inside a non-bare Git checkout")
	}
	result.Root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return result, errors.New("cannot resolve the Git checkout directory")
	}
	result.Reference = reference
	result.Tickets = []string{}
	work := filepath.Join(result.Root, ".sdlc", "work", reference)
	ticketDirectory := filepath.Join(work, "tickets")
	for _, path := range []string{work, ticketDirectory} {
		if err := directoryOrMissing(path); err != nil {
			return result, err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return result, errors.New("selected work reference and its tickets directory must exist")
		} else if err != nil {
			return result, errors.New("cannot inspect selected work directory")
		}
	}
	tracked, err := git(ctx, result.Root, "ls-files", "-z", "--", ":(icase).sdlc/work")
	if err != nil {
		return result, err
	}
	if tracked != "" {
		return result, errors.New(".sdlc/work contains tracked or staged files; remove them from the Git index before inspecting work")
	}
	ignored, err := git(ctx, result.Root, "check-ignore", "--no-index", ".sdlc/work/")
	if err != nil || strings.TrimSpace(ignored) != ".sdlc/work/" {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("Git must ignore .sdlc/work; run sdlc init and review repository ignore rules")
	}
	before, err := os.Lstat(ticketDirectory)
	if err != nil {
		return result, errors.New("cannot inspect ticket directory")
	}
	files, err := os.Open(ticketDirectory)
	if err != nil {
		return result, errors.New("cannot inspect ticket directory")
	}
	defer files.Close()
	opened, err := files.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return result, errors.New("ticket directory changed while opening")
	}
	if err := directoryOrMissing(ticketDirectory); err != nil {
		return result, err
	}
	type ticket struct {
		id   uint64
		path string
	}
	tickets := []ticket{}
	ids := map[uint64]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entries, readErr := files.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return result, errors.New("cannot inspect ticket directory")
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.EqualFold(filepath.Ext(name), ".md") || name[0] < '0' || name[0] > '9' {
				continue
			}
			if !utf8.ValidString(name) || !portablePath(name) || !ticketName.MatchString(name) {
				return result, errors.New("numbered ticket Markdown filenames must use a numeric ID, a nonempty title and .md")
			}
			prefix, _, _ := strings.Cut(name, "-")
			id, err := strconv.ParseUint(prefix, 10, 64)
			if err != nil || id == 0 {
				return result, errors.New("ticket numeric IDs must be positive integers within the supported range")
			}
			if ids[id] {
				return result, errors.New("selected work contains duplicate numeric ticket IDs")
			}
			if err := regularOrMissing(filepath.Join(ticketDirectory, name)); err != nil {
				return result, err
			}
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return result, errors.New("ticket files must be regular files, not symlinks")
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return result, errors.New("cannot inspect ticket file")
			}
			ids[id] = true
			tickets = append(tickets, ticket{id, filepath.ToSlash(filepath.Join(".sdlc", "work", reference, "tickets", name))})
		}
		if readErr == io.EOF {
			break
		}
	}
	if len(tickets) == 0 {
		return result, errors.New("selected work has no numbered Markdown ticket files")
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].id < tickets[j].id })
	paths := make([]string, len(tickets))
	for i, ticket := range tickets {
		paths[i] = ticket.path
	}
	// Check each selected file without interpreting quoted Git output. Nested
	// ignore exceptions must not expose tickets even when the work root is ignored.
	for _, path := range paths {
		if _, err := git(ctx, result.Root, "check-ignore", "--quiet", "--no-index", "--", path); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, errors.New("Git must ignore every selected ticket; run sdlc init and review repository ignore rules")
		}
	}
	result.Tickets = paths
	return result, nil
}
