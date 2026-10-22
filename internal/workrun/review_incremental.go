package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
)

type CumulativeReviewProof struct {
	SourceAttempt int         `json:"source_attempt"`
	Source        Publication `json:"source"`
	RequiredPaths []string    `json:"required_paths"`
}

type IncrementalReviewPlan struct {
	CumulativeReviewProof
	Publication      Publication            `json:"publication"`
	SourceTree       string                 `json:"source_tree"`
	ContextKey       string                 `json:"context_key"`
	RequiredManifest []string               `json:"required_manifest"`
	Inherited        []ReviewFileInspection `json:"inherited"`
	Escalated        bool                   `json:"escalated,omitempty"`
}

type reviewTreeReader interface {
	ReviewTree(context.Context, string, string) (string, error)
}

func (r DockerRepository) ReviewTree(ctx context.Context, workspace, commit string) (string, error) {
	if !objectID.MatchString(commit) {
		return "", fmt.Errorf("invalid review tree revision")
	}
	data, err := r.run(ctx, workspace, "review_tree:"+commit, "")
	var tree string
	if err != nil || json.Unmarshal(data, &tree) != nil || !objectID.MatchString(tree) {
		return "", fmt.Errorf("review tree unavailable")
	}
	return tree, nil
}

func boundedCorrection(diff ReviewDiff) bool {
	if diff.Sensitive || diff.Truncated || len(diff.Files) > 20 {
		return false
	}
	lines := 0
	for _, f := range diff.Files {
		lines += f.Added + f.Deleted
		p := strings.ToLower(f.Path)
		if f.Binary || !safeRelative(f.Path) {
			return false
		}
		ext := path.Ext(p)
		if ext == ".sql" || ext == ".proto" || ext == ".graphql" || ext == ".csproj" || ext == ".props" || ext == ".targets" {
			return false
		}
		for _, marker := range []string{"migration", "openapi", "/contracts/", "/auth", "credential", "secret", "security", ".github/", "appsettings", "dockerfile", "compose.", "service-behaviour", "/adr/"} {
			if strings.Contains(p, marker) {
				return false
			}
		}
		for _, name := range []string{"agents.md", "claude.md", "skill.md"} {
			if path.Base(p) == name {
				return false
			}
		}
	}
	return lines <= 1000
}

func reviewReportAt(j *Journal, attempt int) *ReviewReport {
	for i := range j.ReviewReports {
		if j.ReviewReports[i].Attempt == attempt {
			return &j.ReviewReports[i]
		}
	}
	return nil
}

func validateIncrementalReview(j *Journal) error {
	for i := range j.ReviewReports {
		r := &j.ReviewReports[i]
		if b := r.InspectionBoundary; b != nil {
			if !objectID.MatchString(b.Tree) || len(b.ScopeKey) != 64 || r.Coverage == nil || r.Coverage.Scope != "full" || len(r.Coverage.Gaps) > 0 {
				return fmt.Errorf("invalid inspection baseline")
			}
		}
		if p := r.Cumulative; p != nil {
			source := reviewReportAt(j, p.SourceAttempt)
			if source == nil || source.Attempt >= r.Attempt || source.Publication != p.Source || source.InspectionBoundary == nil {
				return fmt.Errorf("invalid cumulative review provenance")
			}
		}
	}
	p := j.ReviewPlan
	if p == nil {
		return nil
	}
	source := reviewReportAt(j, p.SourceAttempt)
	if source == nil || source.Publication != p.Source || source.InspectionBoundary == nil || source.InspectionBoundary.Tree != p.SourceTree || source.InspectionBoundary.ScopeKey != p.ContextKey || p.Publication.Number != p.Source.Number || p.Publication.URL != p.Source.URL || p.Publication.BaseSHA != p.Source.BaseSHA {
		return fmt.Errorf("invalid incremental review source")
	}
	for _, paths := range [][]string{p.RequiredPaths, p.RequiredManifest} {
		seen := map[string]bool{}
		for _, v := range paths {
			if !safeRelative(v) || seen[v] {
				return fmt.Errorf("invalid incremental review manifest")
			}
			seen[v] = true
		}
	}
	required := map[string]bool{}
	for _, v := range p.RequiredPaths {
		required[v] = true
	}
	manifest := map[string]bool{}
	for _, v := range p.RequiredManifest {
		manifest[v] = true
	}
	for v := range required {
		if !manifest[v] {
			return fmt.Errorf("required correction path absent from manifest")
		}
	}
	baseline := map[string]bool{}
	for _, v := range source.Coverage.Inspection {
		baseline[v.Path] = v.Complete
	}
	seenInherited := map[string]bool{}
	for _, v := range p.Inherited {
		if !v.Complete || !baseline[v.Path] || !manifest[v.Path] || seenInherited[v.Path] || required[v.Path] || v.InheritedFromAttempt != p.SourceAttempt {
			return fmt.Errorf("invalid inherited inspection")
		}
		seenInherited[v.Path] = true
	}
	return nil
}

func (r Runner) prepareIncrementalReview(ctx context.Context, dir string, j *Journal, workspace string) error {
	if activePRCheckpoint(j, "review") {
		j.ReviewPlan = nil
		return nil
	}
	if p := j.ReviewPlan; p != nil && p.Publication == j.Publication {
		if err := validateIncrementalReview(j); err != nil {
			return err
		}
		treeReader, ok := r.Repository.(reviewTreeReader)
		if !ok || p.ContextKey != incrementalContextKey(j) {
			return fmt.Errorf("incremental review scope changed")
		}
		tree, err := treeReader.ReviewTree(ctx, workspace, p.Source.HeadSHA)
		if err != nil || tree != p.SourceTree {
			inventory, ok := r.Repository.(reviewChangedPathRepository)
			if !ok {
				return fmt.Errorf("full review recovery inventory unavailable")
			}
			final, readErr := inventory.ReviewChangedPaths(ctx, workspace, p.Publication.BaseSHA, p.Publication.HeadSHA)
			if readErr != nil {
				return readErr
			}
			p.Escalated = true
			paths := map[string]bool{}
			for _, v := range append(append([]string(nil), final...), p.RequiredPaths...) {
				if !safeRelative(v) {
					return fmt.Errorf("invalid full review recovery path")
				}
				paths[v] = true
			}
			p.RequiredManifest = nil
			for v := range paths {
				p.RequiredManifest = append(p.RequiredManifest, v)
			}
			sort.Strings(p.RequiredManifest)
			fmt.Fprintln(r.Output, "Prior review object unavailable or changed; inherited coverage revoked, continuing full inspection and prior concern reconciliation in the same reviewer session.")
			return r.save(dir, j)
		}
		diffs, diffOK := r.Repository.(reviewDiffReader)
		inventory, pathsOK := r.Repository.(reviewChangedPathRepository)
		if !diffOK || !pathsOK {
			return fmt.Errorf("incremental review manifest cannot be verified")
		}
		diff, err := diffs.ReviewDiff(ctx, workspace, p.Source.HeadSHA, p.Publication.HeadSHA)
		if err != nil {
			return fmt.Errorf("incremental correction diff unavailable")
		}
		final, err := inventory.ReviewChangedPaths(ctx, workspace, p.Publication.BaseSHA, p.Publication.HeadSHA)
		if err != nil {
			return fmt.Errorf("incremental final manifest unavailable")
		}
		required, manifest, ok := correctionManifests(diff, final, reviewReportAt(j, p.SourceAttempt))
		if !ok || !reflect.DeepEqual(required, p.RequiredPaths) || !reflect.DeepEqual(manifest, p.RequiredManifest) {
			return fmt.Errorf("saved incremental review manifest changed")
		}
		return nil
	}
	j.ReviewPlan = nil
	if j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingInputs != nil || len(j.Outcome.Questions) > 0 || len(j.ReviewReports) == 0 {
		return nil
	}
	source := &j.ReviewReports[len(j.ReviewReports)-1]
	b := source.InspectionBoundary
	if b == nil || source.Coverage == nil || source.Coverage.Scope != "full" || len(source.Coverage.Gaps) > 0 || source.Publication == j.Publication || source.Publication.BaseSHA != j.Publication.BaseSHA || source.Publication.Number != j.Publication.Number || source.Publication.URL != j.Publication.URL || b.ScopeKey != incrementalContextKey(j) {
		return nil
	}
	trees, ok := r.Repository.(reviewTreeReader)
	if !ok {
		return nil
	}
	diffs, ok := r.Repository.(reviewDiffReader)
	if !ok {
		return nil
	}
	inventory, ok := r.Repository.(reviewChangedPathRepository)
	if !ok {
		return nil
	}
	oldTree, err := trees.ReviewTree(ctx, workspace, source.Publication.HeadSHA)
	if err != nil || oldTree != b.Tree {
		return nil
	}
	current, err := r.Repository.Inspect(ctx, workspace)
	if err != nil || !current.Clean || current.Head != j.Publication.HeadSHA || current.Tree != j.Evidence.Tree {
		return nil
	}
	diff, err := diffs.ReviewDiff(ctx, workspace, source.Publication.HeadSHA, j.Publication.HeadSHA)
	if err != nil || !boundedCorrection(diff) {
		return nil
	}
	final, err := inventory.ReviewChangedPaths(ctx, workspace, j.Publication.BaseSHA, j.Publication.HeadSHA)
	if err != nil {
		return nil
	}
	requiredPaths, manifestPaths, ok := correctionManifests(diff, final, source)
	if !ok {
		return nil
	}
	required := map[string]bool{}
	manifest := map[string]bool{}
	for _, p := range requiredPaths {
		required[p] = true
	}
	for _, p := range manifestPaths {
		manifest[p] = true
	}
	plan := &IncrementalReviewPlan{CumulativeReviewProof: CumulativeReviewProof{SourceAttempt: source.Attempt, Source: source.Publication, RequiredPaths: requiredPaths}, Publication: j.Publication, SourceTree: b.Tree, ContextKey: b.ScopeKey, RequiredManifest: manifestPaths}
	for _, item := range source.Coverage.Inspection {
		if item.Complete && manifest[item.Path] && !required[item.Path] {
			plan.Inherited = append(plan.Inherited, ReviewFileInspection{Path: item.Path, Complete: true, InheritedFromAttempt: source.Attempt, Evidence: fmt.Sprintf("Retained from review attempt %d at %s; unchanged in the verified correction diff.", source.Attempt, source.Publication.HeadSHA)})
		}
	}
	j.ReviewPlan = plan
	if err := validateIncrementalReview(j); err != nil {
		j.ReviewPlan = nil
		return err
	}
	if err := r.save(dir, j); err != nil {
		return err
	}
	fmt.Fprintf(r.Output, "Correction review: %d mandatory paths plus affected callers/tests/docs; %d unchanged file inspections retained from attempt %d.\n", len(plan.RequiredPaths), len(plan.Inherited), source.Attempt)
	return nil
}

const affectedClosureGap = "Complete affected behaviour and explicitly confirm affected_closure_complete."

func (r Runner) reconcilePlannedInspection(ctx context.Context, workspace string, j *Journal, out *Outcome, history ...[]ReviewFileInspection) error {
	p := j.ReviewPlan
	if p == nil || p.Publication != j.Publication || out.ReviewCoverage == nil {
		return r.reconcileReviewInspection(ctx, workspace, out, history...)
	}
	if err := validateIncrementalReview(j); err != nil {
		return err
	}
	c := out.ReviewCoverage
	if c.FullReviewRequired {
		p.Escalated = true
	}
	batches := [][]ReviewFileInspection{}
	if !p.Escalated {
		batches = append(batches, p.Inherited)
	}
	for _, batch := range history {
		items := []ReviewFileInspection{}
		for _, item := range batch {
			if !p.Escalated || item.InheritedFromAttempt == 0 {
				items = append(items, item)
			}
		}
		batches = append(batches, items)
	}
	c.Gaps = withoutAffectedClosureGap(c.Gaps)
	c.Gaps = withoutPriorConcernsGap(c.Gaps)
	concernsComplete := reconcilePriorConcerns(j, out)

	if err := r.reconcileInspectionPaths(ctx, workspace, out, p.RequiredManifest, batches...); err != nil {
		return err
	}
	if !p.Escalated && !c.AffectedClosureComplete && (out.Status == "reviewed" || out.Status == "review_incomplete") {
		c.Gaps = append(c.Gaps, affectedClosureGap)
		c.Scope = "partial"
		out.Status = "review_incomplete"
	}
	if !concernsComplete && (out.Status == "reviewed" || out.Status == "review_incomplete") {
		c.Gaps = append(c.Gaps, priorConcernsGap)
		c.Scope = "partial"
		out.Status = "review_incomplete"
	}
	if continuation := j.ReviewContinuation; continuation != nil {
		for _, gap := range continuation.Gaps {
			if gap == affectedClosureGap && (c.AffectedClosureComplete || p.Escalated) || gap == priorConcernsGap && concernsComplete {
				c.CompletedGaps = append(c.CompletedGaps, gap)
			}
		}
	}
	return nil
}

func withoutAffectedClosureGap(gaps []string) []string {
	out := []string{}
	for _, g := range gaps {
		if g != affectedClosureGap {
			out = append(out, g)
		}
	}
	return out
}

type ReviewConcernDisposition struct {
	ID           string `json:"id"`
	Disposition  string `json:"disposition"`
	Reason       string `json:"reason"`
	CurrentPath  string `json:"current_path"`
	CurrentIndex int    `json:"current_index"`
}
type priorReviewConcern struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Finding Finding `json:"finding"`
}

func priorReviewConcerns(j *Journal) []priorReviewConcern {
	out := []priorReviewConcern{}
	if j.ReviewPlan == nil {
		return out
	}
	source := reviewReportAt(j, j.ReviewPlan.SourceAttempt)
	if source == nil {
		return out
	}
	for _, group := range []struct {
		kind  string
		items []Finding
	}{{"finding", source.Findings}, {"recommendation", source.Recommendations}} {
		for _, item := range group.items {
			data, _ := json.Marshal(struct {
				Kind    string
				Finding Finding
			}{group.kind, item})
			sum := sha256.Sum256(data)
			out = append(out, priorReviewConcern{ID: hex.EncodeToString(sum[:]), Kind: group.kind, Finding: item})
		}
	}
	return out
}

const priorConcernsGap = "Record an evidence-backed disposition for every prior concern."

func reconcilePriorConcerns(j *Journal, out *Outcome) bool {
	c := out.ReviewCoverage
	known := map[string]ReviewConcernDisposition{}
	for _, report := range j.ReviewReports {
		if report.Publication == j.Publication && report.Coverage != nil {
			for _, d := range report.Coverage.PriorConcernDispositions {
				known[d.ID] = d
			}
		}
	}
	for _, d := range c.PriorConcernDispositions {
		known[d.ID] = d
	}
	c.PriorConcernDispositions = nil
	complete := true
	usedItems := map[string]bool{}
	for _, prior := range priorReviewConcerns(j) {
		d, ok := known[prior.ID]
		valid := ok && strings.TrimSpace(d.Reason) != ""
		switch d.Disposition {
		case "resolved", "superseded":
		case "still_valid":
			items := out.Findings
			if prior.Kind == "recommendation" {
				items = out.Recommendations
			}
			itemKey := fmt.Sprintf("%s:%d", prior.Kind, d.CurrentIndex)
			found := d.CurrentIndex >= 0 && d.CurrentIndex < len(items) && items[d.CurrentIndex].Path == d.CurrentPath && !usedItems[itemKey]
			valid = valid && found && safeRelative(d.CurrentPath)
			if valid {
				usedItems[itemKey] = true
			}

		default:
			valid = false
		}
		if valid {
			c.PriorConcernDispositions = append(c.PriorConcernDispositions, d)
		} else {
			complete = false
		}
	}
	return complete
}

func incrementalReviewInstructions(j *Journal) string {
	p := j.ReviewPlan
	if p == nil || p.Publication != j.Publication {
		return "\nNo controller-verified cross-revision cumulative plan is available. Reviews of earlier publications are evidence, not permission to inherit file inspection. Complete the current manifest; retain this session and its controller-recorded completed entries, and mark missing inspection explicitly. A delta must name remaining gaps, never claim unchanged files inherited from another publication on your own authority.\n"
	}
	packet, _ := json.Marshal(struct {
		SourceAttempt int                  `json:"source_attempt"`
		Source        Publication          `json:"source"`
		Required      []string             `json:"mandatory_inspection_paths"`
		Inherited     []string             `json:"retained_unchanged_paths"`
		Prior         []priorReviewConcern `json:"prior_concerns"`
		Full          bool                 `json:"full_review_required"`
	}{p.SourceAttempt, p.Source, p.RequiredPaths, inheritedPathNames(p), priorReviewConcerns(j), p.Escalated})
	return "\nController-verified cumulative correction review. Preserve the original PR base and intent, but do not reread every previously inspected body. Inspect the entire correction diff from source.head_sha to publication.head_sha, every mandatory path (including deletions and reverted-out-of-final-diff files), and affected callers, consumers, tests, documentation and supporting behaviour. Scan the combined PR diff for interactions and scope drift; inspect supporting bodies where the repair changes their meaning. Return only newly inspected/reopened exact path entries; the controller retains verified unchanged baseline entries. Reopen any affected inherited path, even if its bytes did not change. Explicitly confirm affected_closure_complete only when affected behaviour is covered. Dispose of every prior_concern by id with source evidence; still_valid concerns must identify a unique zero-based current_index and current_path in returned findings/recommendations. Use superseded with an evidence-backed reason for an explicitly merged/replaced concern. Reassess current PR title/body claims against current code. If scope or interaction risk cannot be bounded, set full_review_required and finish full material inspection in this same session; inherited entries will be revoked. Full scope here means cumulative coverage from the original full inspection plus this independently owned correction/impact assessment, never a claim that you reread inherited bodies. Unfinished accessible work uses review_incomplete and named gaps. Keep one independent owner; use a helper only for a concrete unresolved question with named paths and disjoint ownership, not another whole-PR/test/docs sweep. Do not repeat inventory, verification or completed mapping without a new uncertainty. Cumulative plan: " + string(packet) + "\n"
}
func inheritedPathNames(p *IncrementalReviewPlan) []string {
	paths := []string{}
	for _, i := range p.Inherited {
		paths = append(paths, i.Path)
	}
	return paths
}

func withoutPriorConcernsGap(gaps []string) []string {
	out := []string{}
	for _, g := range gaps {
		if g != priorConcernsGap {
			out = append(out, g)
		}
	}
	return out
}

func correctionManifests(diff ReviewDiff, final []string, source *ReviewReport) ([]string, []string, bool) {
	if source == nil {
		return nil, nil, false
	}
	required := map[string]bool{}
	manifest := map[string]bool{}
	for _, f := range diff.Files {
		if !safeRelative(f.Path) {
			return nil, nil, false
		}
		required[f.Path] = true
	}
	for _, f := range append(append([]Finding(nil), source.Findings...), source.Recommendations...) {
		if !safeRelative(f.Path) {
			return nil, nil, false
		}
		required[f.Path] = true
	}
	for _, p := range final {
		if !safeRelative(p) {
			return nil, nil, false
		}
		manifest[p] = true
	}
	for p := range required {
		manifest[p] = true
	}
	a, b := []string{}, []string{}
	for p := range required {
		a = append(a, p)
	}
	for p := range manifest {
		b = append(b, p)
	}
	sort.Strings(a)
	sort.Strings(b)
	return a, b, true
}
