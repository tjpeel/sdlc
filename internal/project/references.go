package project

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Reference struct {
	Name    string   `json:"name"`
	Tickets []string `json:"tickets"`
	Error   string   `json:"error,omitempty"`
}

type ReferencesResult struct {
	Version    int         `json:"version"`
	Root       string      `json:"root"`
	References []Reference `json:"references"`
}

// References lists only ignored, untracked local work metadata. It never reads
// ticket bodies or initializes the checkout.
func References(ctx context.Context, directory string) (ReferencesResult, error) {
	result := ReferencesResult{Version: 1, References: []Reference{}}
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return result, errors.New("select a non-bare Git checkout")
	}
	result.Root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return result, err
	}
	work := filepath.Join(result.Root, ".sdlc", "work")
	if err := directoryOrMissing(work); err != nil {
		return result, err
	}
	tracked, err := git(ctx, result.Root, "ls-files", "-z", "--", ":(icase).sdlc/work")
	if err != nil {
		return result, err
	}
	if tracked != "" {
		return result, errors.New(".sdlc/work contains tracked or staged files; remove them from the Git index")
	}
	directoryFile, err := os.Open(work)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer directoryFile.Close()
	entries, err := directoryFile.ReadDir(513)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	ignored, err := git(ctx, result.Root, "check-ignore", "--no-index", ".sdlc/work/")
	if err != nil || strings.TrimSpace(ignored) != ".sdlc/work/" {
		return result, errors.New("Git must ignore .sdlc/work; run sdlc init and review ignore rules")
	}
	if len(entries) > 512 {
		return result, errors.New("too many work references; select a reference explicitly")
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !validWorkReference(entry.Name()) || !entry.IsDir() {
			continue
		}
		ref := Reference{Name: entry.Name(), Tickets: []string{}}
		work, err := InspectWork(ctx, result.Root, entry.Name())
		if err != nil {
			ref.Error = err.Error()
		} else {
			ref.Tickets = work.Tickets
		}
		result.References = append(result.References, ref)
	}
	return result, nil
}
