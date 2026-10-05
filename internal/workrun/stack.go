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

const maximumBundleBytes = int64(2 * 1024 * 1024 * 1024)

type rebaseExecutor interface {
	Rebase(context.Context, string, string, string, string) (RebaseResult, error)
}

// BeginReconciliation must be saved before calling ReplayReconciliation. The
// snapshot path is private controller state, never provider input.
func BeginReconciliation(journal *Journal, snapshot BranchSnapshot) error {
	if journal == nil || !objectID.MatchString(journal.Plan.SourceSHA) || !objectID.MatchString(snapshot.SHA) || snapshot.Bundle == "" || snapshot.Branch == "" {
		return errors.New("invalid reconciliation boundary")
	}
	if journal.Reconciliation != nil {
		return errors.New("reconciliation is already pending")
	}
	journal.Reconciliation = &Reconciliation{OldSource: journal.Plan.SourceSHA, Snapshot: snapshot, State: "pending"}
	return nil
}

// ReplayReconciliation is safe after a controller crash: DockerRepository
// retains a revision-keyed marker and returns the completed rebase instead of
// replaying it. Callers save the returned journal state under their run lock.
func ReplayReconciliation(ctx context.Context, repository rebaseExecutor, journal *Journal) (RebaseResult, error) {
	if journal == nil || journal.Reconciliation == nil {
		return RebaseResult{}, errors.New("no reconciliation is pending")
	}
	record := journal.Reconciliation
	if record.State == "rebased" || record.State == "conflict" {
		return record.Result, nil
	}
	if record.State != "pending" || !objectID.MatchString(record.OldSource) || !objectID.MatchString(record.Snapshot.SHA) {
		return RebaseResult{}, errors.New("invalid reconciliation checkpoint")
	}
	result, err := repository.Rebase(ctx, journal.Workspace, record.Snapshot.Bundle, record.OldSource, record.Snapshot.SHA)
	if err != nil {
		return RebaseResult{}, err
	}
	record.Result = result
	if result.Conflict {
		record.State = "conflict"
	} else {
		record.State = "rebased"
	}
	return result, nil
}

// CaptureSnapshot creates a provider workspace from a controller-produced
// bundle. Only explicitly selected local inputs are copied from the original
// launch root; Git objects and configuration always come from the snapshot.
func CaptureSnapshot(ctx context.Context, launch project.LaunchResult, snapshot BranchSnapshot, destination, branch, repository string, roles Roles) (Plan, error) {
	if !objectID.MatchString(snapshot.SHA) || snapshot.Branch == "" || snapshot.Bundle == "" {
		return Plan{}, errors.New("invalid source snapshot")
	}
	if _, err := isolatedGit(ctx, launch.Root, "check-ref-format", "--branch", snapshot.Branch); err != nil {
		return Plan{}, errors.New("invalid snapshot branch")
	}
	info, err := os.Lstat(snapshot.Bundle)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBundleBytes {
		return Plan{}, errors.New("invalid source snapshot bundle")
	}
	parent := filepath.Dir(destination)
	if err := realDirectory(parent); err != nil {
		return Plan{}, err
	}
	stage, err := os.MkdirTemp(parent, ".snapshot-")
	if err != nil {
		return Plan{}, err
	}
	if err = os.Chmod(stage, 0700); err != nil {
		os.RemoveAll(stage)
		return Plan{}, err
	}
	defer os.RemoveAll(stage)
	root := filepath.Join(stage, "source")
	if err = os.Mkdir(root, 0700); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "init", "--template="); err != nil {
		return Plan{}, fmt.Errorf("cannot create source snapshot: %w", err)
	}
	if _, err = isolatedGit(ctx, root, "-c", "protocol.file.allow=always", "fetch", "--no-tags", snapshot.Bundle, "refs/sdlc/snapshot:refs/sdlc/snapshot"); err != nil {
		return Plan{}, err
	}
	head, err := isolatedGit(ctx, root, "rev-parse", "--verify", snapshot.SHA+"^{commit}")
	if err != nil || strings.TrimSpace(head) != snapshot.SHA {
		return Plan{}, errors.New("snapshot does not contain expected revision")
	}
	if _, err = isolatedGit(ctx, root, "update-ref", "refs/heads/"+snapshot.Branch, snapshot.SHA); err != nil {
		return Plan{}, err
	}
	if _, err = isolatedGit(ctx, root, "symbolic-ref", "HEAD", "refs/heads/"+snapshot.Branch); err != nil {
		return Plan{}, err
	}
	if err = materializeSnapshot(ctx, root, snapshot.SHA); err != nil {
		return Plan{}, err
	}
	// These are the only mutable files Capture needs from the original root.
	for _, path := range append(append([]string{}, launch.Inputs...), launch.Config.InputFiles...) {
		if err = copySource(launch.Root, root, path, maximumInputBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Plan{}, err
		}
	}
	staged := launch
	staged.Root, staged.Head = root, snapshot.SHA
	plan, err := Capture(ctx, staged, destination, branch, snapshot.Branch, repository, roles)
	if err != nil {
		return Plan{}, err
	}
	// The journal remains anchored to the user's project, never the temporary
	// clone. Capture has already produced the worker checkout and hashes.
	plan.Root = launch.Root
	return plan, nil
}

// materializeSnapshot writes raw blob bytes rather than checking out files, so
// attributes, filters and hooks from repository content cannot run.
func materializeSnapshot(ctx context.Context, root, sha string) error {
	tree, err := isolatedGit(ctx, root, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || excluded(path) || filepath.IsAbs(path) || strings.Contains(path, "..") {
			return errors.New("snapshot tree contains unsupported or private path")
		}
		data, err := isolatedGit(ctx, root, "show", sha+":"+path)
		if err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if fields[0] == "100755" {
			mode = 0700
		}
		if err := os.WriteFile(destination, []byte(data), mode); err != nil {
			return err
		}
	}
	return nil
}
