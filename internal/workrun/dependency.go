package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/filelock"
)

type dependencyInvalidError struct{}

func (dependencyInvalidError) Error() string {
	return "dependency base changed; resume the owning feature before continuing"
}

// StoreDeferredAnswer records a human response without running stale native
// work. The owning feature consumes it after restoring a valid dependency base.
func StoreDeferredAnswer(directory string, journal *Journal, answer string) error {
	if journal == nil || journal.State != "waiting_for_human" || len(journal.Outcome.Questions) == 0 || answer == "" || len(answer) > 1024*1024 {
		return fmt.Errorf("no pending human question for deferred answer")
	}
	lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := Load(directory)
	if err != nil || current.UpdatedAt != journal.UpdatedAt {
		return fmt.Errorf("human question changed before answer could be saved")
	}
	if journal.DeferredAnswer != "" && journal.DeferredAnswer != answer {
		return fmt.Errorf("a different human answer is already awaiting dependency reconciliation")
	}
	journal.DeferredAnswer = answer
	return Save(directory, journal)
}
func (dependencyInvalidError) DependencyInvalidated() bool { return true }

var ErrDependencyInvalid error = dependencyInvalidError{}

// The private marker may be written while a native job owns the journal lock.
// It fences the next controller operation and every standalone resume.
func InvalidateDependency(directory string) error {
	if err := realDirectory(directory); err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".dependency-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString("dependency base changed\n")
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(directory, "dependency-invalid"))
}
func DependencyIsInvalid(directory string, journal *Journal) bool {
	if journal != nil && journal.DependencyInvalid {
		return true
	}
	_, err := os.Lstat(filepath.Join(directory, "dependency-invalid"))
	if !os.IsNotExist(err) {
		return true
	}
	if journal == nil || journal.Plan.DependencyContext == nil {
		return false
	}
	return !dependencyOwnerValid(journal.Plan)
}

func dependencyOwnerValid(plan Plan) bool {
	dependency := plan.DependencyContext
	if dependency == nil {
		return true
	}
	expected := filepath.Join(plan.Root, ".sdlc", "work", plan.Reference, "series")
	if dependency.Owner != expected || filepath.Base(dependency.Ticket) != dependency.Ticket {
		return false
	}
	path := filepath.Join(expected, "journal.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var owner struct {
		Results map[string]struct {
			RunID, State, Branch, Base, BaseSHA, HeadSHA, MergeSHA string
			DependencyInvalid                                      bool
		}
	}
	if json.Unmarshal(data, &owner) != nil {
		return false
	}
	for _, parent := range dependency.Parents {
		current, ok := owner.Results[parent.Ticket]
		if !ok || current.RunID != parent.RunID || current.State != parent.State || current.Branch != parent.Branch || current.Base != parent.Base || current.BaseSHA != parent.BaseSHA || current.HeadSHA != parent.HeadSHA || current.MergeSHA != parent.MergeSHA {
			return false
		}
	}
	return true
}

type dependencyRemote interface {
	Branch(context.Context, Plan, string) (BranchSnapshot, error)
	Observe(context.Context, Plan, Publication) (RemotePR, error)
}

// ValidateDependencyContext runs through the trusted isolated publisher before
// native work, including standalone answer and feedback actions. No repository
// code or check commands run in that credential-bearing boundary.
func ValidateDependencyContext(ctx context.Context, directory string, plan Plan, remote dependencyRemote) error {
	dependency := plan.DependencyContext
	if dependency == nil {
		return nil
	}
	if DependencyIsInvalid(directory, &Journal{Plan: plan}) {
		return ErrDependencyInvalid
	}
	if !objectID.MatchString(dependency.IntegrationSHA) || !objectID.MatchString(dependency.SignedSHA) || dependency.SignedSHA != plan.SourceSHA {
		return ErrDependencyInvalid
	}
	integration, err := remote.Branch(ctx, plan, dependency.IntegrationBranch)
	if err != nil {
		return err
	}
	if integration.SHA != dependency.IntegrationSHA {
		return ErrDependencyInvalid
	}
	base, err := remote.Branch(ctx, plan, plan.Base)
	if err != nil {
		return err
	}
	if base.SHA != dependency.SignedSHA {
		return ErrDependencyInvalid
	}
	for _, parent := range dependency.Parents {
		parentPlan := plan
		parentPlan.Branch, parentPlan.Base, parentPlan.BaseSHA = parent.Branch, parent.Base, parent.BaseSHA
		observed, err := remote.Observe(ctx, parentPlan, Publication{Number: parent.Number, HeadSHA: parent.HeadSHA})
		if err != nil {
			return err
		}
		if observed.HeadSHA != parent.HeadSHA || observed.Base != parent.Base || observed.BaseSHA != parent.BaseSHA {
			return ErrDependencyInvalid
		}
		if parent.State == "ready" {
			if observed.State != "OPEN" {
				return ErrDependencyInvalid
			}
			branch, err := remote.Branch(ctx, plan, parent.Branch)
			if err != nil {
				return err
			}
			if branch.SHA != parent.HeadSHA {
				return ErrDependencyInvalid
			}
			if checks, ok := remote.(interface {
				Checks(context.Context, Plan, Publication) (CIResult, error)
			}); ok {
				status, err := checks.Checks(ctx, parentPlan, Publication{Number: parent.Number, HeadSHA: parent.HeadSHA, BaseSHA: parent.BaseSHA})
				if err != nil {
					return err
				}
				if status.Status != "passed" {
					return ErrDependencyInvalid
				}
			}
		} else if parent.State != "merged" || observed.State != "MERGED" || observed.MergeSHA != parent.MergeSHA {
			return ErrDependencyInvalid
		}
	}
	return nil
}
func ClearDependencyInvalidation(directory string) error {
	err := os.Remove(filepath.Join(directory, "dependency-invalid"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot clear dependency invalidation: %w", err)
	}
	return nil
}
