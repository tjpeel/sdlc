package workrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrCombinedConflict = errors.New("dependency snapshots conflict; child has not started")

// CombineSnapshots operates only on controller-exported objects in a fresh,
// credential-free repository. Temporary merge commits never enter the published
// history: the final aggregate is one linear commit above integration.
func CombineSnapshots(ctx context.Context, directory string, integration BranchSnapshot, parents []BranchSnapshot) (Revision, error) {
	if len(parents) == 0 || !objectID.MatchString(integration.SHA) {
		return Revision{}, fmt.Errorf("combined base requires exact snapshots")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return Revision{}, err
	}
	if _, err := isolatedGit(ctx, directory, "init", "-b", "aggregate"); err != nil {
		return Revision{}, err
	}
	fetch := func(snapshot BranchSnapshot, ref string) error {
		if !objectID.MatchString(snapshot.SHA) {
			return fmt.Errorf("invalid snapshot revision")
		}
		info, err := os.Lstat(snapshot.Bundle)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBundleBytes {
			return fmt.Errorf("invalid combined snapshot bundle")
		}
		if _, err := SafeGit(ctx, directory, "fetch", "--no-tags", snapshot.Bundle, "refs/sdlc/snapshot:"+ref); err != nil {
			return err
		}
		actual, err := SafeGit(ctx, directory, "rev-parse", ref)
		if err != nil || strings.TrimSpace(actual) != snapshot.SHA {
			return fmt.Errorf("combined snapshot changed")
		}
		return nil
	}
	if err := fetch(integration, "refs/sdlc/integration"); err != nil {
		return Revision{}, err
	}
	commit := func(tree string, parents ...string) (string, error) {
		args := []string{"-c", "user.name=SDLC aggregate", "-c", "user.email=aggregate@example.invalid", "commit-tree", tree}
		for _, parent := range parents {
			args = append(args, "-p", parent)
		}
		args = append(args, "-m", "Combined dependency snapshot")
		value, err := SafeGit(ctx, directory, args...)
		return strings.TrimSpace(value), err
	}
	combined := integration.SHA
	var tree string
	for i, snapshot := range parents {
		if err := fetch(snapshot, fmt.Sprintf("refs/sdlc/parent-%d", i)); err != nil {
			return Revision{}, err
		}
		output, err := SafeGit(ctx, directory, "merge-tree", "--write-tree", combined, snapshot.SHA)
		if err != nil {
			var status interface{ ExitCode() int }
			if errors.As(err, &status) && status.ExitCode() == 1 {
				return Revision{}, ErrCombinedConflict
			}
			return Revision{}, err
		}
		tree = strings.Split(output, "\n")[0]
		if !objectID.MatchString(tree) {
			return Revision{}, fmt.Errorf("invalid combined tree")
		}
		combined, err = commit(tree, combined, snapshot.SHA)
		if err != nil {
			return Revision{}, err
		}
	}
	head, err := commit(tree, integration.SHA)
	if err != nil {
		return Revision{}, err
	}
	if _, err := SafeGit(ctx, directory, "update-ref", "refs/heads/aggregate", head); err != nil {
		return Revision{}, err
	}
	if err := materializeSnapshot(ctx, directory, head); err != nil {
		return Revision{}, err
	}
	if _, err := SafeGit(ctx, directory, "bundle", "create", filepath.Join(filepath.Dir(directory), "source.bundle"), "HEAD"); err != nil {
		return Revision{}, err
	}
	return Revision{Head: head, Tree: tree, Clean: true}, nil
}

// CaptureCombinedCheckInputs preserves selected verification guidance separately
// from the aggregate tree, using the same stable reads and bounds as ticket runs.
func CaptureCombinedCheckInputs(root, destination string, paths []string) ([]Input, error) {
	if err := os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	inputs := []Input{}
	var total int64
	for _, path := range paths {
		if !safeCheckRecoveryPath(path) {
			return nil, fmt.Errorf("unsafe combined check input")
		}
		data, err := readStableInput(root, path, maximumInputBytes-total)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		target := filepath.Join(destination, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return nil, err
		}
		inputs = append(inputs, Input{Path: path, SHA256: inputHash(data)})
	}
	return inputs, nil
}
