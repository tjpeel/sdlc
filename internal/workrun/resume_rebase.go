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

// Resume upstream checks are independent of feedback and recommendation policy.
type resumeUpstreamPublisher interface {
	Observe(context.Context, Plan, Publication) (RemotePR, error)
	Branch(context.Context, Plan, string) (BranchSnapshot, error)
	Snapshot(context.Context, Plan, string, string, string) (BranchSnapshot, error)
	ValidateResumeTarget(context.Context, Plan, RemotePR) error
}

// ValidateResumeTarget permits a retarget only to the default branch after the
// former parent PR has merged there. Observe already verifies the child identity.
func (p GitHubPublisher) ValidateResumeTarget(ctx context.Context, plan Plan, remote RemotePR) error {
	if remote.Base == plan.Base {
		return nil
	}
	data, err := p.command(ctx, "gh", "api", "repos/"+plan.Repository)
	if err != nil {
		return err
	}
	var repo struct {
		Default string `json:"default_branch"`
	}
	if json.Unmarshal(data, &repo) != nil || repo.Default == "" || remote.Base != repo.Default {
		return fmt.Errorf("PR target changed outside the supported merged-parent transition")
	}
	owner := strings.Split(plan.Repository, "/")[0]
	endpoint := "repos/" + plan.Repository + "/pulls?state=closed&head=" + url.QueryEscape(owner+":"+plan.Base) + "&base=" + url.QueryEscape(remote.Base) + "&per_page=100"
	data, err = p.command(ctx, "gh", "api", endpoint)
	if err != nil {
		return err
	}
	var parents []struct {
		MergedAt *string `json:"merged_at"`
		Base     struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if json.Unmarshal(data, &parents) != nil {
		return fmt.Errorf("cannot verify merged parent PR")
	}
	if len(parents) >= 100 {
		return fmt.Errorf("parent PR history exceeds verification page; inspect target transition")
	}
	count := 0
	for _, parent := range parents {
		if parent.MergedAt != nil && parent.Head.Ref == plan.Base && parent.Head.Repo.FullName == plan.Repository && parent.Base.Ref == remote.Base {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("retarget requires one verified merged parent PR into the default branch")
	}
	return nil
}

func (p DockerPublisher) ValidateResumeTarget(ctx context.Context, plan Plan, remote RemotePR) error {
	if plan.Restack == nil {
		return fmt.Errorf("missing resume rebase publication boundary")
	}
	return p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error {
		current, err := g.Observe(ctx, plan, Publication{Number: plan.Restack.Number})
		if err != nil {
			return err
		}
		if current != remote {
			return fmt.Errorf("PR changed while verifying rebase target")
		}
		return g.ValidateResumeTarget(ctx, plan, remote)
	})
}

// checkResumeUpstream runs under the normal run lock before any provider turn.
// It checkpoints the exact bundle before touching the worker's Git history.
func (r Runner) checkResumeUpstream(ctx context.Context, directory string, j *Journal) (bool, error) {
	if j.ResumeRebase && j.Reconciliation == nil {
		return false, fmt.Errorf("missing saved resume rebase checkpoint")
	}
	if j.Publication.Number == 0 {
		if r.Rebase {
			return false, fmt.Errorf("rebase requires a published PR")
		}
		return false, nil
	}
	p, ok := r.Publisher.(resumeUpstreamPublisher)
	if !ok {
		return false, fmt.Errorf("publisher cannot verify resume upstream")
	}
	remote, err := p.Observe(ctx, j.Plan, j.Publication)
	if err != nil {
		return false, err
	}
	if remote.State != "OPEN" || remote.HeadSHA != j.Publication.HeadSHA || !objectID.MatchString(remote.BaseSHA) {
		return false, fmt.Errorf("PR closed or published head changed; refusing upstream reconciliation")
	}
	// GitHub can retain the PR's previous baseRefOid after its target branch
	// advances. Observe pins the PR lease; Branch pins the actual rebase target.
	target, err := p.Branch(ctx, j.Plan, remote.Base)
	if err != nil {
		return false, err
	}
	if target.Branch != remote.Base || !objectID.MatchString(target.SHA) || target.Bundle != "" {
		return false, fmt.Errorf("invalid live PR target branch observation")
	}
	pending := j.ResumeRebase && j.Reconciliation != nil
	if !pending && remote.Base == j.Plan.Base && target.SHA == j.Plan.BaseSHA {
		return false, nil
	}
	if !r.Rebase && !pending {
		return false, fmt.Errorf("PR upstream changed to %s at %s; resume with --rebase to replay this PR's commits onto its current target", remote.Base, target.SHA)
	}
	if !pending {
		if j.Reconciliation != nil || j.Plan.DependencyContext != nil || j.PendingInputs != nil || j.State == "waiting_for_human" || len(j.Outcome.Questions) != 0 || j.PushRepair != nil && j.PushRepair.Pending != nil || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.DeferredAnswer != "" || pendingCIFeedback(j) != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || unfinishedRebaseFeedback(j) || j.Plan.Restack != nil {
			return false, fmt.Errorf("finish pending recovery or feature reconciliation before rebasing")
		}
		_, ok := r.Repository.(rebaseExecutor)
		if !ok {
			return false, fmt.Errorf("repository cannot rebase")
		}
		revision, err := r.Repository.Inspect(ctx, j.Workspace)
		if err != nil {
			return false, err
		}
		publishedSource := revision.Head == j.Publication.HeadSHA || j.Evidence.Passed && revision.Head == j.Evidence.Head && revision.Tree == j.Evidence.Tree
		if !revision.Clean || !publishedSource {
			return false, fmt.Errorf("rebase requires a clean workspace at the published PR source revision")
		}
		plan := j.Plan
		plan.Restack = &RestackBoundary{Base: remote.Base, BaseSHA: remote.BaseSHA, HeadSHA: remote.HeadSHA, Number: j.Publication.Number}
		if err := p.ValidateResumeTarget(ctx, plan, remote); err != nil {
			return false, err
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
		// An adoption describes the original child snapshot, not this upstream.
		plan.Adoption = nil
		snapshot, err := p.Snapshot(ctx, plan, target.Branch, target.SHA, request)
		if err != nil {
			os.RemoveAll(request)
			return false, err
		}
		if snapshot.Branch != target.Branch || snapshot.SHA != target.SHA {
			os.RemoveAll(request)
			return false, fmt.Errorf("snapshot differs from verified target branch tip")
		}
		if err := validateSnapshotBundle(snapshot.Bundle); err != nil {
			return false, err
		}
		// Adoption pins the original source and base. An explicitly authorized rebase
		// replaces that boundary; retain it as audit history, not an active invariant.
		if j.Plan.Adoption != nil {
			j.RebasedAdoptions = append(j.RebasedAdoptions, *j.Plan.Adoption)
			j.Plan.Adoption = nil
		}

		j.Plan.Restack = plan.Restack
		if err := BeginReconciliation(j, snapshot); err != nil {
			return false, err
		}
		// An adopted run starts at the PR head. Rebase the entire PR range,
		// not merely corrections made since adoption.
		j.Reconciliation.OldSource = j.Plan.BaseSHA
		j.ResumeRebase = true
		if err := r.save(directory, j); err != nil {
			return false, err
		}
	}
	record := j.Reconciliation
	if j.Plan.Restack == nil || remote.HeadSHA != j.Plan.Restack.HeadSHA || remote.Base != record.Snapshot.Branch || target.SHA != record.Snapshot.SHA || remote.BaseSHA != j.Plan.Restack.BaseSHA && remote.BaseSHA != record.Snapshot.SHA {
		return false, fmt.Errorf("PR or upstream moved during saved rebase; inspect the recorded reconciliation before retrying")
	}
	parent := filepath.Join(directory, "resume-rebase-source") + string(os.PathSeparator)
	if !strings.HasPrefix(record.Snapshot.Bundle, parent) {
		return false, fmt.Errorf("invalid saved rebase bundle location")
	}
	if err := validateSnapshotBundle(record.Snapshot.Bundle); err != nil {
		return false, err
	}
	repo, ok := r.Repository.(rebaseExecutor)
	if !ok {
		return false, fmt.Errorf("repository cannot rebase")
	}
	result, err := ReplayReconciliation(ctx, repo, j)
	if err != nil {
		return false, err
	}
	if err := r.save(directory, j); err != nil {
		return false, err
	}
	snapshot := record.Snapshot
	j.Plan.Base, j.Plan.BaseSHA = snapshot.Branch, snapshot.SHA
	j.Plan.SourceSHA, j.Plan.StartingSHA = snapshot.SHA, snapshot.SHA
	j.Evidence, j.CI, j.Outcome = CheckEvidence{}, CIResult{}, Outcome{}
	j.FormatterPreparation, j.LiveDiagnostics, j.Diagnostics, j.Verification = nil, nil, nil, nil
	j.DiagnosticFeedbackKey, j.VerificationFeedbackKey = "", ""
	j.ReviewAssessment, j.ReviewContinuation, j.ReviewHumanInput, j.ReviewSkip = nil, nil, nil, nil
	j.CandidatePhasePending = false
	j.State, j.ResumeState, j.StopReason, j.PendingRole = "implementing", "", "", ""
	// Settled feedback was not re-opened or re-assessed by this operation. Keep
	// every assessment and receipt, but do not re-enter its old feedback cycle.
	if j.PRFeedback != nil {
		j.PRFeedback.Active = false
	}
	j.RestackNeedsChecks = true
	j.Feedback = "The controller performed a Git rebase onto the PR target " + snapshot.Branch + " at " + snapshot.SHA + ". Preserve the rebased commits and the original ticket scope. Do not recreate or squash the implementation. Obtain fresh checks and local review before publication. Existing independent-review and recommendation settings remain in effect."
	if result.Conflict {
		j.Feedback += " Resolve the unfinished Git rebase, preserving the existing commits, then git rebase --continue. Conflicting paths: " + strings.Join(result.Paths, ", ") + ". Do not publish an unfinished rebase."
	}
	j.Reconciliation, j.ResumeRebase = nil, false
	if err := r.save(directory, j); err != nil {
		return false, err
	}
	_ = os.Remove(snapshot.Bundle)
	return true, nil
}

func unfinishedRebaseFeedback(j *Journal) bool {
	if j.PRFeedback == nil {
		return false
	}
	status := PRFeedbackStatus(*j)
	return j.PRFeedback.FetchPending || j.PRFeedback.LocalPending != nil || status.Pending > 0 || status.OutstandingReplies > 0 || status.Unresolved > 0
}
