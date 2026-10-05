package project

import (
	"context"
	"path/filepath"
	"strings"
)

type SourceIdentity struct {
	Root, Branch, Revision string
	Dirty                  bool
}

// InspectIdentity reuses the metadata-only dirty check. Displaying a prompt
// must not execute repository content filters or filesystem-monitor hooks.
func InspectIdentity(ctx context.Context, directory string) (SourceIdentity, error) {
	var result SourceIdentity
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return result, err
	}
	result.Root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return result, err
	}
	head, err := git(ctx, result.Root, "rev-parse", "--verify", "HEAD")
	if err != nil && ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Revision = strings.TrimSpace(head)
	branch, _ := git(ctx, result.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	result.Branch = strings.TrimSpace(branch)
	result.Dirty, err = dirty(ctx, result.Root, result.Revision)
	return result, err
}
