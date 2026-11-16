package workrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
)

// CaptureComparison creates an independent worker from a saved run's source
// history without moving either the user's checkout or the saved worker.
func CaptureComparison(ctx context.Context, launch project.LaunchResult, previous Plan, previousWorkspace, destination, branch, repository string, roles Roles) (Plan, error) {
	if !objectID.MatchString(previous.StartingSHA) || !objectID.MatchString(previous.SourceSHA) || !objectID.MatchString(previous.BaseSHA) {
		return Plan{}, errors.New("comparison source revision unavailable")
	}
	if _, err := isolatedGit(ctx, launch.Root, "check-ref-format", "--branch", previous.Base); err != nil {
		return Plan{}, errors.New("invalid comparison base branch")
	}
	parent := filepath.Dir(destination)
	if err := realDirectory(parent); err != nil {
		return Plan{}, err
	}
	stage, err := os.MkdirTemp(parent, ".comparison-")
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(stage)
	if err = os.Chmod(stage, 0700); err != nil {
		return Plan{}, err
	}
	bundle := filepath.Join(stage, "source.bundle")
	if _, err = SafeGit(ctx, previousWorkspace, "rev-list", "--objects", "--missing=error", "HEAD"); err != nil {
		return Plan{}, errors.New("saved run has incomplete source history")
	}
	if _, err = SafeGit(ctx, previousWorkspace, "bundle", "create", bundle, "HEAD"); err != nil {
		return Plan{}, fmt.Errorf("cannot capture saved run history: %w", err)
	}
	root := filepath.Join(stage, "source")
	if err = os.Mkdir(root, 0700); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "init", "--template="); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "fetch", "--no-tags", bundle, "HEAD:refs/sdlc/comparison-source"); err != nil {
		return Plan{}, err
	}
	for _, sha := range []string{previous.BaseSHA, previous.SourceSHA, previous.StartingSHA} {
		if _, err = isolatedGit(ctx, root, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return Plan{}, errors.New("recorded comparison revision is missing from saved history")
		}
	}
	for _, ancestor := range []string{previous.BaseSHA, previous.SourceSHA} {
		if _, err = isolatedGit(ctx, root, "merge-base", "--is-ancestor", ancestor, previous.StartingSHA); err != nil {
			return Plan{}, errors.New("recorded comparison source does not include its base")
		}
	}
	sourceBranch := "__sdlc_comparison_source__"
	if sourceBranch == previous.Base {
		sourceBranch += "_head"
	}
	if _, err = isolatedGit(ctx, root, "update-ref", "refs/heads/"+previous.Base, previous.BaseSHA); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "update-ref", "refs/heads/"+sourceBranch, previous.StartingSHA); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "symbolic-ref", "HEAD", "refs/heads/"+sourceBranch); err != nil {
		return Plan{}, err
	}
	if err = materializeSnapshot(ctx, root, previous.StartingSHA); err != nil {
		return Plan{}, err
	}
	originalInputs := map[string]bool{}
	for _, in := range previous.Inputs {
		originalInputs[in.Path] = true
	}
	for _, path := range launch.Inputs {
		inputRoot := launch.Root
		if originalInputs[path] {
			inputRoot = previousWorkspace
		}
		if err = copySource(inputRoot, root, path, maximumInputBytes); err != nil {
			return Plan{}, err
		}
	}
	checkRoot := filepath.Join(filepath.Dir(previousWorkspace), "check-inputs")
	for _, path := range launch.Config.InputFiles {
		if err = copySource(checkRoot, root, path, maximumInputBytes); err != nil {
			return Plan{}, err
		}
	}

	staged := launch
	staged.Root, staged.Head = root, previous.StartingSHA
	plan, err := Capture(ctx, staged, destination, branch, previous.Base, repository, roles)
	if err != nil {
		return Plan{}, err
	}
	if plan.StartingSHA != previous.StartingSHA || plan.BaseSHA != previous.BaseSHA || strings.TrimSpace(plan.SourceSHA) != previous.StartingSHA {
		os.RemoveAll(destination)
		if len(launch.Config.InputFiles) > 0 {
			os.RemoveAll(filepath.Join(parent, "check-inputs"))
		}
		return Plan{}, errors.New("comparison capture differs from recorded starting source")
	}
	plan.Root, plan.SourceSHA = launch.Root, previous.SourceSHA
	return plan, nil
}
