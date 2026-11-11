package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type unpublishedRebasePublisher interface {
	Branch(context.Context, Plan, string) (BranchSnapshot, error)
	Snapshot(context.Context, Plan, string, string, string) (BranchSnapshot, error)
	VerifyUnpublished(context.Context, Plan) error
}

// VerifyUnpublished refuses to rewrite a candidate if a previous publication
// attempt may have created a remote branch or pull request.
func (p GitHubPublisher) VerifyUnpublished(ctx context.Context, plan Plan) error {
	if p.Frozen == nil || !githubRepository.MatchString(plan.Repository) {
		return fmt.Errorf("unpublished rebase requires frozen repository identity")
	}
	if err := p.Frozen.Validate(); err != nil {
		return err
	}
	if err := p.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := p.verifyRepositoryIdentity(ctx, plan); err != nil {
		return err
	}
	if _, err := isolatedGit(ctx, os.TempDir(), "check-ref-format", "--branch", plan.Branch); err != nil {
		return fmt.Errorf("invalid destination branch")
	}
	data, err := p.command(ctx, "gh", "api", "repos/"+plan.Repository+"/git/matching-refs/heads/"+url.PathEscape(plan.Branch))
	var refs []struct {
		Ref string `json:"ref"`
	}
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(data)), "[") || json.Unmarshal(data, &refs) != nil {
		return fmt.Errorf("cannot verify destination branch")
	}
	for _, ref := range refs {
		if ref.Ref == "refs/heads/"+plan.Branch {
			return fmt.Errorf("destination branch already exists; recover publication before rebasing")
		}
	}
	owner := strings.Split(plan.Repository, "/")[0]
	data, err = p.command(ctx, "gh", "api", "repos/"+plan.Repository+"/pulls?state=all&head="+url.QueryEscape(owner+":"+plan.Branch)+"&per_page=1")
	var prs []json.RawMessage
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(data)), "[") || json.Unmarshal(data, &prs) != nil {
		return fmt.Errorf("cannot verify destination pull requests")
	}
	if len(prs) != 0 {
		return fmt.Errorf("destination pull request exists; recover publication before rebasing")
	}
	return nil
}

func (p DockerPublisher) VerifyUnpublished(ctx context.Context, plan Plan) error {
	return p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { return g.VerifyUnpublished(ctx, plan) })
}

func (r Runner) rebaseUnpublished(ctx context.Context, directory string, j *Journal) (bool, error) {
	pending := j.ResumeRebase && j.Reconciliation != nil
	if !pending && !r.Rebase {
		return false, nil
	}
	if j.Publication.URL != "" || j.Plan.DependencyContext != nil || j.Plan.ProvisionalParent != nil || j.Plan.Restack != nil || j.Plan.Adoption != nil || j.PendingInputs != nil || j.PushRepair != nil || j.PRFeedback != nil || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.DeferredAnswer != "" || pendingCIFeedback(j) != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending {
		return false, fmt.Errorf("unpublished rebase requires a standalone run without pending recovery")
	}
	if !pending && (j.State != "blocked" || j.ResumeState != "publishing" || !j.Evidence.Passed || j.Reconciliation != nil) {
		return false, fmt.Errorf("unpublished rebase requires a checked candidate blocked by a changed base")
	}
	p, ok := r.Publisher.(unpublishedRebasePublisher)
	if !ok {
		return false, fmt.Errorf("publisher cannot verify unpublished candidate")
	}
	if err := p.VerifyUnpublished(ctx, j.Plan); err != nil {
		return false, err
	}
	target, err := p.Branch(ctx, j.Plan, j.Plan.Base)
	if err != nil {
		return false, err
	}
	if target.Branch != j.Plan.Base || !objectID.MatchString(target.SHA) || target.Bundle != "" {
		return false, fmt.Errorf("invalid live base observation")
	}
	repo, ok := r.Repository.(rebaseExecutor)
	if !ok {
		return false, fmt.Errorf("repository cannot rebase")
	}
	if !pending {
		if target.SHA == j.Plan.BaseSHA {
			return false, fmt.Errorf("base no longer differs; resume without --rebase")
		}
		revision, err := r.Repository.Inspect(ctx, j.Workspace)
		if err != nil {
			return false, err
		}
		if !revision.Clean || revision.Head != j.Evidence.Head || revision.Tree != j.Evidence.Tree {
			return false, fmt.Errorf("unpublished rebase requires the clean checked candidate")
		}
		parent := filepath.Join(directory, "resume-rebase-source")
		if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
			return false, err
		}
		if err := realDirectory(parent); err != nil {
			return false, err
		}
		request, err := os.MkdirTemp(parent, "snapshot-")
		if err != nil {
			return false, err
		}
		snapshot, err := p.Snapshot(ctx, j.Plan, target.Branch, target.SHA, request)
		if err != nil {
			os.RemoveAll(request)
			return false, err
		}
		if snapshot.Branch != target.Branch || snapshot.SHA != target.SHA {
			os.RemoveAll(request)
			return false, fmt.Errorf("snapshot differs from verified base")
		}
		if err := validateSnapshotBundle(snapshot.Bundle); err != nil {
			return false, err
		}
		if err := BeginReconciliation(j, snapshot); err != nil {
			return false, err
		}
		j.Reconciliation.OldSource = j.Plan.BaseSHA
		j.ResumeRebase = true
		if err := r.save(directory, j); err != nil {
			return false, err
		}
	}
	record := j.Reconciliation
	if record.OldSource != j.Plan.BaseSHA || record.Snapshot.Branch != target.Branch || record.Snapshot.SHA != target.SHA {
		return false, fmt.Errorf("base moved during saved rebase; inspect reconciliation")
	}
	parent := filepath.Join(directory, "resume-rebase-source") + string(os.PathSeparator)
	if !strings.HasPrefix(record.Snapshot.Bundle, parent) {
		return false, fmt.Errorf("invalid saved rebase bundle location")
	}
	if err := validateSnapshotBundle(record.Snapshot.Bundle); err != nil {
		return false, err
	}
	result, err := ReplayReconciliation(ctx, repo, j)
	if err != nil {
		return false, err
	}
	if err := r.save(directory, j); err != nil {
		return false, err
	}
	snapshot := record.Snapshot
	j.Plan.BaseSHA, j.Plan.SourceSHA, j.Plan.StartingSHA = snapshot.SHA, snapshot.SHA, snapshot.SHA
	j.Evidence, j.CI, j.Outcome = CheckEvidence{}, CIResult{}, Outcome{}
	j.FormatterPreparation, j.LiveDiagnostics, j.Diagnostics, j.Verification = nil, nil, nil, nil
	j.DiagnosticFeedbackKey, j.VerificationFeedbackKey = "", ""
	j.ReviewAssessment, j.ReviewContinuation, j.ReviewHumanInput, j.ReviewSkip = nil, nil, nil, nil
	j.CandidatePhasePending = false
	j.SelfReview = nil
	j.State, j.ResumeState, j.StopReason, j.PendingRole = "implementing", "", "", ""
	j.RestackNeedsChecks, j.RebaseMonitoring = true, nil
	j.Feedback = "The controller rebased the unpublished candidate onto " + snapshot.Branch + " at " + snapshot.SHA + ". Preserve the rebased commits and original ticket scope. Obtain fresh checks and local review before publication."
	if result.Conflict {
		j.Feedback += " Resolve the unfinished Git rebase and run git rebase --continue. Conflicting paths: " + strings.Join(result.Paths, ", ") + "."
	}
	j.Reconciliation, j.ResumeRebase = nil, false
	if err := r.save(directory, j); err != nil {
		return false, err
	}
	_ = os.Remove(snapshot.Bundle)
	return true, nil
}
