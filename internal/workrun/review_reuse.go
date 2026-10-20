package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ReviewedContent is recorded only after a complete independent review with no
// unresolved findings or questions. Missing legacy proof never implies equality.
type ReviewedContent struct {
	Tree     string `json:"tree"`
	ScopeKey string `json:"scope_key"`
}

// ReviewReuse retains the original reviewer report without claiming another
// native review took place or rebinding its original coverage to a new commit.
type ReviewReuse struct {
	Publication   Publication `json:"publication"`
	SourceAttempt int         `json:"source_attempt"`
	Tree          string      `json:"tree"`
	ScopeKey      string      `json:"scope_key"`
}

func reviewScopeKey(j *Journal) string {
	data, _ := json.Marshal(struct {
		Requirements                                      reviewRequirements
		Base, BaseSHA, Title, Body, Instructions, ImageID string
		Runtime                                           *RuntimeProvenance
		Companions                                        []CompanionRepository
		HumanInput                                        *ReviewHumanInput
		Checks, EffectiveChecks                           [][]string
		DockerTests                                       bool
	}{selectedReviewRequirements(j.Plan), j.Plan.Base, j.Plan.BaseSHA, j.Plan.PRTitle, j.Plan.PRBody, j.Instructions, j.ImageID, j.Plan.RuntimeProvenance, j.CompanionRepositories, j.ReviewHumanInput, j.Plan.Checks, j.EffectiveChecks, j.Plan.DockerTests})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reusableReport(j *Journal) *ReviewReport {
	if len(j.ReviewReports) == 0 || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingInputs != nil || j.CandidatePhasePending || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || len(j.Outcome.Findings) > 0 || len(j.Outcome.Questions) > 0 {
		return nil
	}
	report := &j.ReviewReports[len(j.ReviewReports)-1]
	c := report.Coverage
	if report.Content == nil || len(report.Findings) > 0 || c == nil || c.Scope != "full" || len(c.Gaps) > 0 || report.Publication.BaseSHA != j.Publication.BaseSHA || report.Publication.Number != j.Publication.Number || report.Publication.URL != j.Publication.URL || !j.Evidence.Passed || report.Content.Tree != j.Evidence.Tree || report.Content.ScopeKey != reviewScopeKey(j) {
		return nil
	}
	return report
}

func reviewReuseCurrent(j *Journal) bool {
	p := j.ReviewReuse
	if p == nil || p.Publication != j.Publication || p.Tree != j.Evidence.Tree || p.ScopeKey != reviewScopeKey(j) {
		return false
	}
	r := reusableReport(j)
	return r != nil && r.Attempt == p.SourceAttempt
}

func validateReviewReuse(j *Journal) error {
	for _, r := range j.ReviewReports {
		if c := r.Content; c != nil {
			key, err := hex.DecodeString(c.ScopeKey)
			if err != nil || len(key) != sha256.Size || !objectID.MatchString(c.Tree) || len(r.Findings) > 0 || r.Coverage == nil || r.Coverage.Scope != "full" || len(r.Coverage.Gaps) > 0 {
				return fmt.Errorf("invalid reviewed content boundary")
			}
		}
	}
	p := j.ReviewReuse
	if p == nil {
		return nil
	}
	for _, r := range j.ReviewReports {
		if r.Attempt == p.SourceAttempt && r.Content != nil && p.Tree == r.Content.Tree && p.ScopeKey == r.Content.ScopeKey && objectID.MatchString(p.Publication.HeadSHA) && p.Publication.BaseSHA == r.Publication.BaseSHA && p.Publication.Number == r.Publication.Number && p.Publication.URL == r.Publication.URL {
			return nil
		}
	}
	return fmt.Errorf("invalid independent review reuse proof")
}

func (r Runner) tryReuseReview(ctx context.Context, dir string, j *Journal) (bool, error) {
	report := reusableReport(j)
	if report == nil || report.Publication == j.Publication {
		return false, nil
	}
	revision, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil || !revision.Clean || revision.Head != j.Evidence.Head || revision.Tree != report.Content.Tree {
		return false, nil
	}
	// Verify the published tree itself, not just the worker/check evidence.
	// A changed external head must never inherit review from a stale workspace.
	if r.ReviewWorkspace == nil {
		return false, nil
	}
	snapshot, err := r.ReviewWorkspace(ctx, *j, dir)
	if err != nil {
		return false, nil
	}
	published, err := r.Repository.Inspect(ctx, snapshot)
	if err != nil || !published.Clean || published.Head != j.Publication.HeadSHA || published.Tree != report.Content.Tree {
		return false, nil
	}
	j.ReviewSkip = nil
	j.ReviewReuse = &ReviewReuse{Publication: j.Publication, SourceAttempt: report.Attempt, Tree: report.Content.Tree, ScopeKey: report.Content.ScopeKey}
	if err := r.save(dir, j); err != nil {
		return false, err
	}
	if r.Output != nil {
		fmt.Fprintf(r.Output, "Reusing independent review attempt %d: complete Git tree, base and review scope are unchanged; current-head CI and Sonar remain required.\n", report.Attempt)
	}
	return true, nil
}

func (r Runner) acceptRecommendationsUnlessReused(ctx context.Context, dir string, j *Journal) (bool, error) {
	if reviewReuseCurrent(j) {
		return false, nil
	}
	return r.autoAcceptRecommendations(ctx, dir, j)
}
