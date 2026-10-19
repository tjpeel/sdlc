package workrun

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

//go:embed formatter_snapshot.py
var formatterSnapshotHelper string

type pendingFormatterRepository interface {
	FormatterSnapshot(context.Context, string) (string, Revision, func(), error)
}

func (repository DockerRepository) FormatterSnapshot(ctx context.Context, workspace string) (string, Revision, func(), error) {
	directory, err := os.MkdirTemp("", "sdlc-format-snapshot-")
	if err != nil {
		return "", Revision{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err = os.Chmod(directory, 0777); err != nil {
		cleanup()
		return "", Revision{}, nil, err
	}
	data, err := repository.run(ctx, workspace, "format_snapshot", directory)
	var revision Revision
	if err != nil || json.Unmarshal(data, &revision) != nil || !revision.Clean || !objectID.MatchString(revision.Head) || !objectID.MatchString(revision.Tree) {
		cleanup()
		return "", Revision{}, nil, errors.New("private formatter snapshot could not be captured")
	}
	return filepath.Join(directory, "workspace"), revision, cleanup, nil
}

// formatterResults never runs product builds or grants verification gates.
func (runner Runner) formatterResults(ctx context.Context, directory string, j *Journal, workspace string, revision Revision, retain bool) ([]VerificationResult, bool, error) {
	var requests []VerificationRequest
	var err error
	shadow := *j
	shadow.Workspace = workspace
	if discovery, ok := runner.Repository.(formatterPreparationDiscovery); ok {
		requests, err = discovery.FormatterPreparation(ctx, &shadow, revision)
	} else if _, ok := runner.Repository.(pendingFormatterRepository); ok {
		requests, err = preparationRequests(ctx, &shadow, revision)
	} else {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	checker := runner.Checker
	if pinned, ok := checker.(diagnosticRevisionChecker); ok {
		checker = pinned.WithDiagnosticRevision(revision)
	}
	results := make([]VerificationResult, 0, len(requests))
	corrections := false
	started := time.Now()
	defer func() { j.Timings.FormatterWallMS += time.Since(started).Milliseconds() }()
	execute := func(scoped Checker) error {
		verifier, ok := scoped.(SourcePatchChecker)
		if !ok && len(requests) > 0 {
			return errors.New("isolated formatter unavailable")
		}
		queue := append([]VerificationRequest(nil), requests...)
		for len(queue) > 0 {
			request := queue[0]
			queue = queue[1:]
			key := verificationKey(&shadow, request, revision.Head, revision.Tree)
			var reused *VerificationResult
			if retain {
				for i := range j.FormatterPreparation {
					e := &j.FormatterPreparation[i]
					if e.Key == key && e.Head == revision.Head && e.Tree == revision.Tree && e.Passed {
						reused = e
						break
					}
				}
			}
			if reused != nil {
				results = append(results, *reused)
				continue
			}
			j.CheckAttempt++
			log, err := os.CreateTemp(directory, fmt.Sprintf("precommit-format-%d-*.log", j.CheckAttempt))
			if err != nil {
				return err
			}
			capture := newCommandOutputCollector(io.MultiWriter(log, runner.rawOutput))
			patch, checkErr := verifier.VerifySourcePatch(ctx, workspace, request, revision.Head, revision.Tree, capture)
			closeErr := log.Close()
			var oversized *sourcePatchDiffBudgetError
			if closeErr == nil && !retain && errors.As(checkErr, &oversized) && len(request.SourcePatch.Paths) > 1 {
				left, right := splitPendingFormatterRequest(request)
				queue = append([]VerificationRequest{left, right}, queue...)
				continue
			}
			if closeErr != nil || (checkErr != nil && !errors.Is(checkErr, ErrCheckFailed) && !sourcePatchScopeOnly(checkErr)) {
				return errors.New("isolated formatter operation failed")
			}
			result := VerificationResult{Request: request, Key: key, Head: revision.Head, Tree: revision.Tree, Log: filepath.Base(log.Name()), Output: capture.results(), Passed: checkErr == nil}
			if checkErr == nil {
				if err := validateSourcePatchResult(patch, request, revision.Head, revision.Tree); err != nil {
					return err
				}
				result.SourcePatch = patch
				result.Passed = patch.Diff == ""
			} else {
				result.Diagnostic = "Formatter failed; inspect its command evidence before retrying."
			}
			if !result.Passed {
				corrections = true
				if result.SourcePatch != nil && result.SourcePatch.Diff != "" {
					j.Timings.FormatterCorrections++
				}
			}
			results = append(results, result)
			// Return bounded corrections incrementally. The agent applies this batch
			// and repeats --format on fresh pending bytes before creating a commit.
			if !retain && result.SourcePatch != nil && result.SourcePatch.Diff != "" {
				break
			}
		}
		return nil
	}
	// SourcePatchChecker owns a credential-free formatter worker. Do not wrap
	// it in a full candidate phase: that would start unused test infrastructure
	// and stage private check inputs before a formatting-only operation.
	if len(requests) > 0 {
		err = execute(checker)
	}
	if err != nil {
		return nil, false, err
	}
	artifacts := Journal{Plan: j.Plan, ImageID: j.ImageID, EffectiveChecks: j.EffectiveChecks, Verification: results}
	if err := validateSourcePatchJournal(&artifacts); err != nil {
		return nil, false, err
	}
	if retain {
		j.FormatterPreparation = results
		boundJournalOutput(j)
	}
	if retain {
		if err := runner.save(directory, j); err != nil {
			return nil, false, err
		}
	}
	return results, corrections, nil
}

func (runner Runner) formatPendingCandidate(ctx context.Context, directory string, j *Journal, request LiveDiagnosticRequest, original Revision) (LiveDiagnosticResponse, error) {
	response := LiveDiagnosticResponse{Version: 1, ID: request.ID, FormatBaseHead: original.Head}
	repository, ok := runner.Repository.(pendingFormatterRepository)
	if !ok {
		response.Error = "Pre-commit formatter snapshots are unavailable in this runtime."
		return response, nil
	}
	workspace, revision, cleanup, err := repository.FormatterSnapshot(ctx, j.Workspace)
	if err != nil {
		return response, err
	}
	defer cleanup()
	// Select only pending source edits; earlier committed code is checked by the
	// ordinary formatter gate. The original plan and its frozen inputs stay intact.
	shadow := *j
	shadow.Plan = j.Plan
	shadow.Plan.SourceSHA = original.Head
	shadow.Timings = j.Timings
	results, _, err := runner.formatterResults(ctx, directory, &shadow, workspace, revision, false)
	// Retain operational accounting, never the private snapshot plan.
	j.CheckAttempt = shadow.CheckAttempt
	j.Timings = shadow.Timings
	if saveErr := runner.save(directory, j); saveErr != nil {
		return response, saveErr
	}
	if err != nil {
		return response, err
	}
	_, current, remove, err := repository.FormatterSnapshot(ctx, j.Workspace)
	if err != nil {
		return response, err
	}
	defer remove()
	observed, inspectErr := runner.Repository.Inspect(ctx, j.Workspace)
	if inspectErr != nil || observed.Head != original.Head || current.Tree != revision.Tree {
		response.Error = "Pending edits changed while formatting; discard corrections and retry."
		return response, nil
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return response, err
	}
	if err := verifyInputs(filepath.Join(directory, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return response, err
	}
	detached := Journal{Diagnostics: results}
	boundOutputs(&detached, 16*1024)
	response.Results = detached.Diagnostics
	return response, nil
}

// Requests here come only from the controller's discovered formatter policy;
// their trailing arguments correspond exactly to SourcePatch.Paths.
func splitPendingFormatterRequest(request VerificationRequest) (VerificationRequest, VerificationRequest) {
	paths := request.SourcePatch.Paths
	mid := len(paths) / 2
	split := func(start, end int, suffix string) VerificationRequest {
		r := request
		r.ID += suffix
		r.SourcePatch = &SourcePatchRequest{Paths: append([]string(nil), paths[start:end]...)}
		r.Commands = append([][]string(nil), request.Commands...)
		last := request.Commands[len(request.Commands)-1]
		prefix := len(last) - len(paths)
		command := append([]string(nil), last[:prefix]...)
		command = append(command, last[prefix+start:prefix+end]...)
		r.Commands[len(r.Commands)-1] = command
		return r
	}
	return split(0, mid, "-a"), split(mid, len(paths), "-b")
}
