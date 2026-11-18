package workrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ExternalPRMerge is saved before the remote request and remains as provenance.
type ExternalPRMerge struct {
	Boundary  ExternalPRBoundary `json:"boundary"`
	Original  Revision           `json:"original"`
	MergeSHA  string             `json:"merge_sha,omitempty"`
	Completed bool               `json:"completed,omitempty"`
}

func validateExternalPRMerge(j *Journal) error {
	if j.ExternalPRRequest != "" {
		repository, number, err := ParsePRURL(j.ExternalPRRequest)
		if err != nil || repository != j.Plan.Repository || number == j.Publication.Number || j.Publication.Number < 1 {
			return fmt.Errorf("invalid pending external PR request")
		}
	}
	m := j.ExternalPR
	if m == nil {
		return nil
	}
	if err := m.Boundary.validate(); err != nil {
		return err
	}
	if m.Boundary.Repository != j.Plan.Repository || m.Boundary.ParentNumber != j.Publication.Number || m.Boundary.ParentURL != j.Publication.URL || m.Boundary.ParentBranch != j.Plan.Branch || !m.Original.Clean || !objectID.MatchString(m.Original.Head) || !objectID.MatchString(m.Original.Tree) || (m.MergeSHA != "" && !objectID.MatchString(m.MergeSHA)) || m.Completed && m.MergeSHA == "" {
		return fmt.Errorf("invalid external PR merge checkpoint")
	}
	if !m.Completed && j.Publication.HeadSHA != m.Boundary.ParentHeadSHA {
		return fmt.Errorf("external PR merge publication changed")
	}
	if !m.Completed && j.ExternalPRRequest != "" && j.ExternalPRRequest != m.Boundary.IncomingURL {
		return fmt.Errorf("external PR request differs from merge checkpoint")
	}
	return nil
}

type externalPRPublisher interface {
	ObserveExternalPR(context.Context, Plan, Publication, int) (ExternalPRObservation, error)
	MergeExternalPR(context.Context, Plan, ExternalPRBoundary) error
	UpdateExternalPRMetadata(context.Context, Plan, Publication, ExternalPRBoundary) error
	Snapshot(context.Context, Plan, string, string, string) (BranchSnapshot, error)
}
type externalPRImporter interface {
	ImportExternalPR(context.Context, string, string, Revision, ExternalPRBoundary, string) error
}

func externalPublishedWorkspace(j *Journal, revision Revision, publishedTree string) bool {
	if !revision.Clean || revision.Tree != publishedTree {
		return false
	}
	return revision.Head == j.Publication.HeadSHA || j.Evidence.Passed && revision.Head == j.Evidence.Head && revision.Tree == j.Evidence.Tree
}

func validateExternalPRStart(j *Journal) error {
	if err := ValidatePRCheckpointResume(*j); err != nil {
		return err
	}
	if j.Plan.ProvisionalParent != nil || j.Plan.DependencyContext != nil || j.Plan.Restack != nil || j.Reconciliation != nil || j.ResumeRebase || j.RuntimeRecheck != nil && j.RuntimeRecheck.Pending {
		return fmt.Errorf("resolve pending reconciliation before merging an external PR")
	}
	feedback := PRFeedbackStatus(*j)
	if j.CIHeldReview != nil && j.CIHeldReview.Pending || j.PRFeedback != nil && (j.PRFeedback.FetchPending || feedback.Pending != 0 || feedback.Unresolved != 0 || feedback.OutstandingReplies != 0) {
		return fmt.Errorf("resolve pending review or feedback before merging an external PR")
	}
	return nil
}

// prepareExternalPR saves the selection before an optional upstream rebase.
// The incoming PR is checked before the parent branch is changed and checked
// again against the new head before the merge request.
func (r Runner) prepareExternalPR(ctx context.Context, dir string, j *Journal) (bool, error) {
	if r.ExternalPRURL != "" && j.ExternalPRRequest != "" && r.ExternalPRURL != j.ExternalPRRequest {
		return false, fmt.Errorf("another external PR request is pending")
	}
	if j.ExternalPR != nil && !j.ExternalPR.Completed {
		return true, r.mergeExternalPR(ctx, dir, j)
	}
	if j.ExternalPRRequest == "" && r.ExternalPRURL != "" {
		repository, number, err := ParsePRURL(r.ExternalPRURL)
		if err != nil || repository != j.Plan.Repository || number == j.Publication.Number {
			return false, fmt.Errorf("external PR must be a different PR in the saved repository")
		}
		if err := validateExternalPRStart(j); err != nil {
			return false, err
		}
		if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
			return false, err
		}
		if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
			return false, err
		}
		if _, ok := r.Repository.(externalPRImporter); !ok {
			return false, fmt.Errorf("repository cannot import external PRs")
		}
		p, ok := r.Publisher.(externalPRPublisher)
		if !ok {
			return false, fmt.Errorf("publisher cannot merge external PRs")
		}
		o, err := p.ObserveExternalPR(ctx, j.Plan, j.Publication, number)
		if err != nil {
			return false, err
		}
		b := o.Boundary
		if b.ParentNumber != j.Publication.Number || b.ParentURL != j.Publication.URL || b.ParentBranch != j.Plan.Branch || b.ParentBase != j.Plan.Base || b.ParentBaseSHA != j.Publication.BaseSHA || b.ParentHeadSHA != j.Publication.HeadSHA {
			return false, fmt.Errorf("saved PR changed before external merge")
		}
		if err := ValidateExternalPRBeforeMerge(b, o); err != nil {
			return false, err
		}
		revision, err := r.Repository.Inspect(ctx, j.Workspace)
		if err != nil {
			return false, err
		}
		if !externalPublishedWorkspace(j, revision, o.ParentTree) {
			return false, fmt.Errorf("external merge requires a clean workspace matching the published PR")
		}
		j.ExternalPRRequest = r.ExternalPRURL
		if err := r.save(dir, j); err != nil {
			return false, err
		}
	}
	if j.ExternalPRRequest == "" || j.State != "ready" && j.State != "awaiting_feedback" {
		return false, nil
	}
	if r.CheckUpstream || j.ResumeRebase {
		upstream := r
		upstream.Rebase = true
		changed, err := upstream.checkResumeUpstream(ctx, dir, j)
		if err != nil {
			return false, err
		}
		if changed {
			return false, nil
		}
	}
	return true, r.mergeExternalPR(ctx, dir, j)
}

func (r Runner) mergeExternalPR(ctx context.Context, dir string, j *Journal) error {
	p, ok := r.Publisher.(externalPRPublisher)
	if !ok {
		return fmt.Errorf("publisher cannot merge external PRs")
	}
	repo, ok := r.Repository.(externalPRImporter)
	if !ok {
		return fmt.Errorf("repository cannot import external PRs")
	}
	m := j.ExternalPR
	if m == nil || m.Completed {
		requested := r.ExternalPRURL
		if requested == "" {
			requested = j.ExternalPRRequest
		}
		repository, number, err := ParsePRURL(requested)
		if err != nil {
			return err
		}
		if repository != j.Plan.Repository || number == j.Publication.Number {
			return fmt.Errorf("external PR must be a different PR in the saved repository")
		}
		if err := validateExternalPRStart(j); err != nil {
			return err
		}
		if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
			return err
		}
		if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
			return err
		}
		revision, err := r.Repository.Inspect(ctx, j.Workspace)
		if err != nil {
			return err
		}
		observed, err := p.ObserveExternalPR(ctx, j.Plan, j.Publication, number)
		if err != nil {
			return err
		}
		b := observed.Boundary
		if b.ParentNumber != j.Publication.Number || b.ParentURL != j.Publication.URL || b.ParentBranch != j.Plan.Branch || b.ParentBase != j.Plan.Base || b.ParentBaseSHA != j.Publication.BaseSHA || b.ParentHeadSHA != j.Publication.HeadSHA {
			return fmt.Errorf("saved PR changed before external merge")
		}
		if err := ValidateExternalPRBeforeMerge(b, observed); err != nil {
			return err
		}
		if !externalPublishedWorkspace(j, revision, observed.ParentTree) {
			return fmt.Errorf("external merge requires a clean workspace matching the published PR")
		}
		m = &ExternalPRMerge{Boundary: b, Original: revision}
		j.ExternalPR = m
		if err := r.save(dir, j); err != nil {
			return err
		}
		if r.Output != nil {
			fmt.Fprintf(r.Output, "Saved merge intent for %s into %s.\n", b.IncomingURL, b.ParentURL)
		}
	} else if r.ExternalPRURL != "" && r.ExternalPRURL != m.Boundary.IncomingURL {
		return fmt.Errorf("another external PR merge is pending")
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return err
	}
	if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return err
	}
	observed, err := p.ObserveExternalPR(ctx, j.Plan, j.Publication, m.Boundary.IncomingNumber)
	if err != nil {
		return err
	}
	if observed.IncomingState == "OPEN" {
		if err := ValidateExternalPRBeforeMerge(m.Boundary, observed); err != nil {
			return err
		}
		// An uncertain request is reconciled on the next resume by observing both PRs.
		if err := p.MergeExternalPR(ctx, j.Plan, m.Boundary); err != nil {
			return fmt.Errorf("external PR merge request failed or its result is unknown; resume to reconcile: %w", err)
		}
		observed, err = p.ObserveExternalPR(ctx, j.Plan, j.Publication, m.Boundary.IncomingNumber)
		if err != nil {
			return err
		}
	}
	if err := ValidateExternalPRMerge(m.Boundary, observed); err != nil {
		return err
	}
	if r.Output != nil {
		fmt.Fprintf(r.Output, "Verified external PR merge at %s.\n", observed.MergeSHA)
	}
	if m.MergeSHA != "" && m.MergeSHA != observed.MergeSHA {
		return fmt.Errorf("external merge result changed")
	}
	m.MergeSHA = observed.MergeSHA
	if err := r.save(dir, j); err != nil {
		return err
	}
	parent := filepath.Join(dir, "external-pr-snapshots")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if err := realDirectory(parent); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	plan := j.Plan
	plan.Adoption = nil
	snapshot, err := p.Snapshot(ctx, plan, m.Boundary.ParentBranch, m.MergeSHA, tmp)
	if err != nil {
		return err
	}
	if snapshot.Branch != m.Boundary.ParentBranch || snapshot.SHA != m.MergeSHA {
		return fmt.Errorf("external merge snapshot changed")
	}
	if err := repo.ImportExternalPR(ctx, j.Workspace, snapshot.Bundle, m.Original, m.Boundary, m.MergeSHA); err != nil {
		return err
	}
	revision, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil || !revision.Clean || revision.Head != m.MergeSHA {
		return fmt.Errorf("external merge import did not produce a clean verified head")
	}
	after, err := p.ObserveExternalPR(ctx, j.Plan, j.Publication, m.Boundary.IncomingNumber)
	if err != nil {
		return err
	}
	if err := ValidateExternalPRMerge(m.Boundary, after); err != nil {
		return err
	}
	// Route through the implementation session so it assesses the incoming change,
	// obtains fresh full checks and self-review, and returns normal PR metadata.
	if j.Plan.Adoption != nil {
		j.RebasedAdoptions = append(j.RebasedAdoptions, *j.Plan.Adoption)
		j.Plan.Adoption = nil
	}
	j.Plan.SourceSHA, j.Plan.StartingSHA = m.MergeSHA, m.MergeSHA
	j.Publication.HeadSHA = m.MergeSHA
	j.Evidence, j.CI, j.Outcome = CheckEvidence{}, CIResult{}, Outcome{}
	j.MissingChecksSince = time.Time{}
	j.FormatterPreparation, j.LiveDiagnostics = nil, nil
	j.DiagnosticFeedbackKey, j.VerificationFeedbackKey = "", ""
	j.ReviewAssessment, j.ReviewContinuation, j.ReviewHumanInput, j.ReviewSkip = nil, nil, nil, nil
	j.SelfReview, j.ReviewReuse, j.RebaseMonitoring, j.ReviewPlan = nil, nil, nil, nil
	if j.CIHeldReview != nil && !j.CIHeldReview.Pending {
		j.CIHeldReview.Invalidated = true
	}
	j.CandidatePhasePending = false
	if j.PRFeedback != nil {
		j.PRFeedback.Active = false
	}
	j.RestackNeedsChecks = true
	j.State, j.ResumeState, j.StopReason, j.PendingRole = "implementing", "", "", ""
	j.Feedback = "An external PR was merged into the published branch. Assess the merged change against the run requirements, preserve it, update the PR as needed, and obtain fresh full checks and self-review before publication."
	m.Completed = true
	j.ExternalPRRequest = ""
	return r.save(dir, j)
}
