package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maximumReviewCompanionBytes = 128 * 1024 * 1024

func frozenCompanionSource(ctx context.Context, workspace string, source CompanionRepository) (string, error) {
	root := filepath.Join(workspace, filepath.FromSlash(source.Path))
	if err := realDirectory(root); err != nil {
		return "", fmt.Errorf("frozen companion source path is unavailable or unsafe")
	}
	revision, err := validateCompanionCheckout(ctx, root, source.Request.Repository)
	if err != nil {
		return "", err
	}
	if revision != source.Revision {
		return "", fmt.Errorf("staged companion source revision changed")
	}
	return root, nil
}

func boundReviewCompanion(root string) error {
	var total int64
	return filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if info.Size() > maximumReviewCompanionBytes-total {
				return fmt.Errorf("companion source exceeds review copy limit")
			}
			total += info.Size()
		} else if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("companion source contains an unsupported file")
		}
		return nil
	})
}

func prepareReviewCompanions(ctx context.Context, journal Journal, workspace string) error {
	if err := validateCompanionRepositories(journal.CompanionRepositories, journal.Plan); err != nil {
		return err
	}
	metadata, err := json.Marshal(journal.CompanionRepositories)
	if err != nil || len(metadata) > 64*1024 {
		return fmt.Errorf("review companion metadata exceeds bound")
	}
	for _, source := range journal.CompanionRepositories {
		root, err := frozenCompanionSource(ctx, journal.Workspace, source)
		if err != nil {
			return err
		}
		if err := boundReviewCompanion(root); err != nil {
			return err
		}
		if err := createCompanionDirectories(workspace, filepath.Dir(filepath.FromSlash(source.Path))); err != nil {
			return err
		}
		destination := filepath.Join(workspace, filepath.FromSlash(source.Path))
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("review companion source collides with published content")
		}
		// A transport clone creates independent objects and fresh sanitized metadata;
		// no worker config, ignored files or implementation transcript is copied.
		copyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, cloneErr := isolatedGit(copyCtx, workspace, "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--template=", root, destination)
		if cloneErr == nil {
			_, cloneErr = isolatedGit(copyCtx, destination, "remote", "set-url", "origin", "https://github.com/"+source.Request.Repository+".git")
		}
		if cloneErr == nil {
			_, cloneErr = isolatedGit(copyCtx, destination, "checkout", "--detach", source.Revision)
		}
		cancel()
		if cloneErr != nil {
			return fmt.Errorf("cannot copy frozen companion source into review snapshot")
		}
		revision, err := validateCompanionCheckout(ctx, destination, source.Request.Repository)
		if err != nil || revision != source.Revision {
			return fmt.Errorf("review companion source failed frozen revision validation")
		}
		if err := boundReviewCompanion(destination); err != nil {
			return err
		}
		if _, err := frozenCompanionSource(ctx, journal.Workspace, source); err != nil {
			return err
		}
	}
	return nil
}

func (runner Runner) stageReviewCompanions(ctx context.Context, directory string, j *Journal, requests []CompanionRepositoryRequest) error {
	if err := validateCompanionRepositoryRequests(requests, &j.Plan); err != nil {
		return err
	}
	if err := validateCompanionRepositories(j.CompanionRepositories, j.Plan); err != nil {
		return err
	}
	var missing []CompanionRepositoryRequest
	for _, request := range requests {
		known := false
		for _, source := range j.CompanionRepositories {
			if source.Request.ID == request.ID && !strings.EqualFold(source.Request.Repository, request.Repository) {
				return fmt.Errorf("companion request id was reused for another repository")
			}
			if strings.EqualFold(source.Request.Repository, request.Repository) {
				if _, err := frozenCompanionSource(ctx, j.Workspace, source); err != nil {
					return err
				}
				known = true
			}
		}
		if !known {
			missing = append(missing, request)
		}
	}
	if len(missing) > 0 {
		_, err := runner.stageCompanionRepositories(ctx, directory, j, missing)
		return err
	}
	return nil
}

func (runner Runner) acceptReviewSourceRequest(ctx context.Context, directory string, j *Journal) error {
	if err := validateOutcome(j.Outcome, "review"); err != nil {
		return runner.stop(directory, j, "blocked", err.Error())
	}
	if err := runner.reviewSourceCI(ctx, j); err != nil {
		return runner.stop(directory, j, "blocked", err.Error())
	}
	if err := runner.stageReviewCompanions(ctx, directory, j, j.Outcome.RepositoryRequests); err != nil {
		var access *companionSourceAccessError
		if errors.As(err, &access) {
			question := "The configured GitHub profile could not clone " + access.repository + ". Confirm that the selected account has read access, then answer and resume independent review."
			j.PendingRole = "review"
			j.Outcome.Status = "waiting_for_human"
			j.Outcome.Questions = []string{question}
			return runner.stop(directory, j, "waiting_for_human", question)
		}
		return runner.stop(directory, j, "blocked", "The controller could not stage review companion source: "+err.Error())
	}
	if err := runner.reviewSourceCI(ctx, j); err != nil {
		return runner.stop(directory, j, "blocked", err.Error())
	}
	return runner.transition(directory, j, "reviewing")
}

func (runner Runner) reviewSourceCI(ctx context.Context, j *Journal) error {
	checks, err := runner.Publisher.Checks(ctx, j.Plan, j.Publication)
	if err == nil {
		err = validateCIResult(checks, j.Publication)
	}
	if err != nil || checks.Status != "passed" {
		return fmt.Errorf("PR or CI changed during independent review source handoff")
	}
	j.CI = checks
	return nil
}
