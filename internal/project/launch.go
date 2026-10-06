package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LaunchResult records the explicit files approved for one ticket run.
type LaunchResult struct {
	Root, Reference, Ticket, Head, Branch string
	Config                                Config
	Inputs                                []string
}

// Launch validates project settings and resolves linked requirement documents
// without writing project state.
func Launch(ctx context.Context, directory, reference, ticket string, inputs []string) (LaunchResult, error) {
	return launch(ctx, directory, reference, ticket, inputs, true)
}

// LaunchFrozen validates a recorded selection without discovering new links.
func LaunchFrozen(ctx context.Context, directory, reference, ticket string, inputs []string) (LaunchResult, error) {
	return launch(ctx, directory, reference, ticket, inputs, false)
}

func launch(ctx context.Context, directory, reference, ticket string, inputs []string, resolve bool) (LaunchResult, error) {
	var r LaunchResult
	work, err := InspectWork(ctx, directory, reference)
	if err != nil {
		return r, err
	}
	r.Root, r.Reference = work.Root, reference
	for _, candidate := range work.Tickets {
		if ticket == filepath.Base(candidate) || ticket == candidate {
			r.Ticket = candidate
			break
		}
	}
	if r.Ticket == "" {
		return r, errors.New("select an exact numbered ticket filename from the selected work reference")
	}
	r.Head, err = git(ctx, r.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return r, errors.New("ticket execution requires an existing HEAD commit")
	}
	r.Head = strings.TrimSpace(r.Head)
	r.Branch, _ = git(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	r.Branch = strings.TrimSpace(r.Branch)
	if err = directoryOrMissing(filepath.Join(r.Root, ".sdlc")); err != nil {
		return r, err
	}
	data, err := readOptional(filepath.Join(r.Root, ConfigPath))
	if err != nil {
		return r, err
	}
	if data == nil {
		return r, errors.New("project settings are missing; run sdlc init before launching work")
	}
	if err = decodeConfig(data, &r.Config); err != nil {
		return r, err
	}
	selected := append([]string{r.Ticket}, inputs...)
	selected = append(selected, r.Config.InputFiles...)
	seen := map[string]bool{}
	for _, path := range selected {
		// References can contain colons; paths are never interpreted as Git refs.
		if !launchPath(path) {
			return r, errors.New("selected inputs require exact relative paths without traversal")
		}
		if seen[path] {
			continue
		}
		full := filepath.Join(r.Root, filepath.FromSlash(path))
		if err = directoryOrMissing(filepath.Dir(full)); err != nil {
			return r, err
		}
		info, e := os.Lstat(full)
		if e != nil || !info.Mode().IsRegular() {
			return r, errors.New("selected and configured inputs must exist as regular files")
		}
		seen[path] = true
		if path == r.Ticket || containsLaunchInput(inputs, path) {
			r.Inputs = append(r.Inputs, path)
		}
	}
	if resolve {
		r.Inputs, err = ResolveRequirements(ctx, r.Root, reference, r.Inputs)
		if err != nil {
			return r, err
		}
	}
	return r, ctx.Err()
}

func launchPath(path string) bool {
	if path == "" || len(path) > 4096 || hasControl(path) || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func containsLaunchInput(inputs []string, path string) bool {
	for _, input := range inputs {
		if input == path {
			return true
		}
	}
	return false
}
