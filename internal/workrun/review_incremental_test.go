package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const correctedTree = "5555555555555555555555555555555555555555"

type correctionRepository struct {
	*fakeRepository
	oldHead, oldTree string
	current          Revision
	diff             ReviewDiff
	final            []string
	missing          bool
}

func (r *correctionRepository) ReviewTree(_ context.Context, _ string, head string) (string, error) {
	if r.missing {
		return "", errors.New("object absent")
	}
	if head == r.oldHead {
		return r.oldTree, nil
	}
	return "", errors.New("unexpected object")
}
func (r *correctionRepository) Inspect(context.Context, string) (Revision, error) {
	return r.current, nil
}
func (r *correctionRepository) ReviewDiff(context.Context, string, string, string) (ReviewDiff, error) {
	return r.diff, nil
}
func (r *correctionRepository) ReviewChangedPaths(context.Context, string, string, string) ([]string, error) {
	return append([]string(nil), r.final...), nil
}

func correctionReviewFixture(t *testing.T) (string, *Journal, Runner, *correctionRepository) {
	t.Helper()
	dir, j, r, _, _ := feedbackReadyRun(t)
	source := &j.ReviewReports[0]
	source.Coverage.Inspection = []ReviewFileInspection{{Path: "src/a.cs", Complete: true, Evidence: "Reviewed original behaviour"}, {Path: "src/b.cs", Complete: true, Evidence: "Reviewed original consumers"}, {Path: "src/c.cs", Complete: true, Evidence: "Reviewed independent behaviour"}}
	source.Findings = []Finding{{Priority: "P2", Path: "src/b.cs", Line: 2, Scenario: "A failure drops the result.", Recommendation: "Preserve the result."}}
	source.Content = nil // Full inspection with findings is not approval reuse.
	j.Publication.HeadSHA = testSigned
	j.Evidence.Head = testSigned
	j.Evidence.Tree = correctedTree
	j.Outcome = testOutcome("implemented")
	j.State = "awaiting_reviewer"
	repo := &correctionRepository{fakeRepository: &fakeRepository{}, oldHead: testHead, oldTree: testTree, current: Revision{Head: testSigned, Tree: correctedTree, Clean: true}, diff: ReviewDiff{Files: []ReviewDiffFile{{Path: "src/a.cs", Added: 2, Deleted: 2}}}, final: []string{"src/a.cs", "src/b.cs", "src/c.cs"}}
	r.Repository = repo
	return dir, j, r, repo
}
func resolveCorrectionConcerns(j *Journal, out *Outcome) {
	out.ReviewCoverage.AffectedClosureComplete = true
	for _, c := range priorReviewConcerns(j) {
		out.ReviewCoverage.PriorConcernDispositions = append(out.ReviewCoverage.PriorConcernDispositions, ReviewConcernDisposition{ID: c.ID, Disposition: "resolved", Reason: "Inspected the corrected failure path and regression assertion."})
	}
}
func correctionOutcome(j *Journal, paths ...string) Outcome {
	out := testOutcome("reviewed")
	out.ReviewCoverage.HeadSHA = j.Publication.HeadSHA
	out.ReviewCoverage.Inspection = []ReviewFileInspection{}
	for _, p := range paths {
		out.ReviewCoverage.Inspection = append(out.ReviewCoverage.Inspection, ReviewFileInspection{Path: p, Complete: true, Evidence: "Inspected correction and affected behaviour"})
	}
	return out
}

func TestCumulativeReviewRetainsUnchangedCoverageAndResolvesFindings(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	original, _ := json.Marshal(j.ReviewReports[0])
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil || j.ReviewPlan == nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.ReviewPlan.RequiredPaths, []string{"src/a.cs", "src/b.cs"}) || len(j.ReviewPlan.Inherited) != 1 || j.ReviewPlan.Inherited[0].Path != "src/c.cs" {
		t.Fatalf("incorrect plan: %+v", j.ReviewPlan)
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &out)
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil || out.Status != "reviewed" || out.ReviewCoverage.Scope != "full" {
		t.Fatal(out, err)
	}
	if len(out.ReviewCoverage.Inspection) != 3 || out.ReviewCoverage.Inspection[2].InheritedFromAttempt != j.ReviewPlan.SourceAttempt {
		t.Fatal("inherited inspection presented as new")
	}
	j.Attempt++
	j.Outcome = out
	if err := recordReviewReport(j); err != nil {
		t.Fatal(err)
	}
	if j.ReviewReports[1].Cumulative == nil || j.ReviewReports[1].InspectionBoundary == nil {
		t.Fatal("cumulative provenance missing")
	}
	after, _ := json.Marshal(j.ReviewReports[0])
	if string(original) != string(after) {
		t.Fatal("source report rewritten")
	}
	if err := validateIncrementalReview(j); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

func TestCumulativeReviewDoesNotLoseRevertedDeletedOrRenamedRepairPaths(t *testing.T) {
	dir, j, r, repo := correctionReviewFixture(t)
	repo.diff.Files = append(repo.diff.Files, ReviewDiffFile{Path: "src/deleted.cs", Deleted: 5}, ReviewDiffFile{Path: "src/renamed.cs", Added: 5})
	repo.final = []string{"src/b.cs", "src/c.cs", "src/renamed.cs"} // a.cs reverted out of final diff.
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	out := correctionOutcome(j, "src/b.cs", "src/renamed.cs")
	resolveCorrectionConcerns(j, &out)
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("omitted repair paths certified full coverage")
	}
	pending := map[string]bool{}
	for _, i := range out.ReviewCoverage.Inspection {
		if !i.Complete {
			pending[i.Path] = true
		}
	}
	if !pending["src/a.cs"] || !pending["src/deleted.cs"] {
		t.Fatal("reverted/deleted path disappeared", pending)
	}
}

func TestCumulativeReviewRequiresExplicitClosureAndPriorDispositions(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" || !strings.Contains(strings.Join(out.ReviewCoverage.Gaps, " "), priorConcernsGap) {
		t.Fatal("prior finding silently dropped", out)
	}
	out = correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &out)
	out.ReviewCoverage.PriorConcernDispositions[0].Disposition = "still_valid"
	out.ReviewCoverage.PriorConcernDispositions[0].CurrentPath = "src/b.cs"
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("still-valid finding omitted from final findings")
	}
}

func TestCumulativeReviewCanReopenInheritedCoverageAndEscalate(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	reopened := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &reopened)
	reopened.ReviewCoverage.Inspection = append(reopened.ReviewCoverage.Inspection, ReviewFileInspection{Path: "src/c.cs", Complete: false, Evidence: "Caller semantics need inspection"})
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Status != "review_incomplete" {
		t.Fatal("reopened caller credited from baseline")
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &out)
	out.ReviewCoverage.FullReviewRequired = true
	history := append([]ReviewFileInspection(nil), j.ReviewPlan.Inherited...)
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out, history); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" || !j.ReviewPlan.Escalated {
		t.Fatal("full escalation retained inherited approval")
	}
}

func TestCumulativeReviewFallsBackOnUnprovenOrBroadChanges(t *testing.T) {
	for _, scenario := range []string{"legacy", "partial", "missing-object", "tampered-tree", "base", "requirements", "companions", "sensitive", "contract", "truncated", "large", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			dir, j, r, repo := correctionReviewFixture(t)
			switch scenario {
			case "legacy":
				j.ReviewReports[0].InspectionBoundary = nil
			case "partial":
				j.ReviewReports[0].Coverage.Scope = "partial"
				j.ReviewReports[0].Coverage.Gaps = []string{"unfinished"}
			case "missing-object":
				repo.missing = true
			case "tampered-tree":
				repo.oldTree = correctedTree
			case "base":
				j.Publication.BaseSHA = correctedTree
			case "requirements":
				j.Instructions += "New requirement"
			case "companions":
				j.CompanionRepositories = append(j.CompanionRepositories, CompanionRepository{})
			case "sensitive":
				repo.diff.Sensitive = true
			case "contract":
				repo.diff.Files[0].Path = "openapi.yaml"
			case "truncated":
				repo.diff.Truncated = true
			case "large":
				repo.diff.Files[0].Added = 1001
			case "unsupported":
				r.Repository = &fakeRepository{}
			}
			if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil || j.ReviewPlan != nil {
				t.Fatalf("unsafe inherited plan: %+v %v", j.ReviewPlan, err)
			}
		})
	}
}

func (r *correctionRepository) ReviewTrackedPaths(context.Context, string, string) ([]string, error) {
	return []string{"src/a.cs", "src/b.cs", "src/c.cs", "src/support.cs"}, nil
}
func TestReopenedSupportingCallerOutsideDiffSurvivesContinuation(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &out)
	out.ReviewCoverage.Inspection = append(out.ReviewCoverage.Inspection, ReviewFileInspection{Path: "src/support.cs", Complete: false, Evidence: "Supporting caller behaviour remains uninspected"})
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("explicitly unfinished supporting caller disappeared")
	}
	history := out.ReviewCoverage.Inspection
	done := correctionOutcome(j, "src/support.cs")
	resolveCorrectionConcerns(j, &done)
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &done, history); err != nil || done.Status != "reviewed" {
		t.Fatal(done, err)
	}
}
func TestDistinctStillValidConcernsCannotCollapseIntoOneFinding(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	second := j.ReviewReports[0].Findings[0]
	second.Scenario = "A different failure duplicates the effect."
	j.ReviewReports[0].Findings = append(j.ReviewReports[0].Findings, second)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	out.ReviewCoverage.AffectedClosureComplete = true
	out.Findings = []Finding{j.ReviewReports[0].Findings[0]}
	for _, p := range priorReviewConcerns(j) {
		out.ReviewCoverage.PriorConcernDispositions = append(out.ReviewCoverage.PriorConcernDispositions, ReviewConcernDisposition{ID: p.ID, Disposition: "still_valid", Reason: "Both remain unresolved", CurrentPath: "src/b.cs", CurrentIndex: 0})
	}
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("distinct unresolved findings silently collapsed")
	}
}
func TestSavedCorrectionPlanRejectsManifestMutationAndRecoversMissingObject(t *testing.T) {
	dir, j, r, repo := correctionReviewFixture(t)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	original := append([]string(nil), j.ReviewPlan.RequiredManifest...)
	j.ReviewPlan.RequiredManifest = []string{"src/c.cs"}
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err == nil {
		t.Fatal("mutated mandatory manifest accepted")
	}
	j.ReviewPlan.RequiredManifest = original
	repo.missing = true
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil || !j.ReviewPlan.Escalated {
		t.Fatalf("missing baseline could not recover through full review: %v", err)
	}
	out := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &out)
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("missing object retained inherited approval")
	}
}
func TestEscalationCreditsFreshInspectionAndRetiresReplacedClosureGap(t *testing.T) {
	dir, j, r, _ := correctionReviewFixture(t)
	if err := r.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	inherited := append([]ReviewFileInspection(nil), j.ReviewPlan.Inherited...)
	j.ReviewContinuation = &ReviewContinuation{Publication: j.Publication, Inspection: inherited, Gaps: []string{affectedClosureGap}}
	out := correctionOutcome(j, "src/c.cs")
	resolveCorrectionConcerns(j, &out)
	out.ReviewCoverage.FullReviewRequired = true
	out.ReviewCoverage.AffectedClosureComplete = false
	if err := r.reconcilePlannedInspection(context.Background(), j.Workspace, j, &out, inherited); err != nil {
		t.Fatal(err)
	}
	if !reviewFilesAdvanced(j, out.ReviewCoverage) {
		t.Fatal("fresh inspection of inherited file counted as no progress")
	}
	if !reviewInspectionAdvanced(j.ReviewContinuation.Gaps, out.ReviewCoverage, true) {
		t.Fatal("full escalation falsely stalled on replaced closure gap")
	}
}

func TestCumulativeEscalationContinuesSameReviewerUntilFreshCoverageCompletes(t *testing.T) {
	dir, j, r, repo := correctionReviewFixture(t)
	j.ReviewReports[0].Coverage.Inspection = append(j.ReviewReports[0].Coverage.Inspection, ReviewFileInspection{Path: "src/d.cs", Complete: true, Evidence: "Original independent inspection"})
	repo.final = append(repo.final, "src/d.cs")
	workspace, err := r.ReviewWorkspace(context.Background(), *j, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.prepareIncrementalReview(context.Background(), dir, j, workspace); err != nil {
		t.Fatal(err)
	}
	first := correctionOutcome(j, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(j, &first)
	first.ReviewCoverage.AffectedClosureComplete = false
	second := correctionOutcome(j, "src/c.cs")
	resolveCorrectionConcerns(j, &second)
	second.ReviewCoverage.FullReviewRequired = true
	second.ReviewCoverage.AffectedClosureComplete = false
	third := correctionOutcome(j, "src/d.cs")
	resolveCorrectionConcerns(j, &third)
	third.ReviewCoverage.AffectedClosureComplete = false
	provider := &feedbackScriptProvider{outcomes: []Outcome{first, second, third}}
	r.Provider = provider
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || result.Outcome.ReviewCoverage.Scope != "full" {
		t.Fatalf("progressing review stalled: %+v %v", result.Outcome, err)
	}
	if len(provider.sessions) != 3 || provider.sessions[0].ResumeID != "" || provider.sessions[1].ResumeID != testNative || provider.sessions[2].ResumeID != testNative {
		t.Fatal("escalation restarted the reviewer")
	}
	for _, entry := range result.Outcome.ReviewCoverage.Inspection {
		if !entry.Complete || entry.InheritedFromAttempt != 0 {
			t.Fatal("full escalation retained stale inherited coverage", entry)
		}
	}
}
