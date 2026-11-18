package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type externalTestPublisher struct {
	Publisher
	observation     ExternalPRObservation
	merges          int
	metadataUpdates int
	uncertain       bool
	snapshots       int
}

func (p *externalTestPublisher) UpdateExternalPRMetadata(context.Context, Plan, Publication, ExternalPRBoundary) error {
	p.metadataUpdates++
	return nil
}

func (p *externalTestPublisher) ObserveExternalPR(context.Context, Plan, Publication, int) (ExternalPRObservation, error) {
	return p.observation, nil
}
func (p *externalTestPublisher) MergeExternalPR(_ context.Context, _ Plan, b ExternalPRBoundary) error {
	p.merges++
	p.observation.IncomingState = "MERGED"
	p.observation.MergeSHA = strings.Repeat("d", 40)
	p.observation.Boundary.ParentHeadSHA = p.observation.MergeSHA
	// GitHub can retain the base SHA observed before the merge.
	p.observation.IncomingBaseSHA = b.ParentHeadSHA
	p.observation.MergeParents = []string{b.ParentHeadSHA, b.IncomingHeadSHA}
	if p.uncertain {
		return errors.New("connection lost")
	}
	return nil
}
func (p *externalTestPublisher) Snapshot(_ context.Context, _ Plan, branch, sha, dir string) (BranchSnapshot, error) {
	p.snapshots++
	path := filepath.Join(dir, "snapshot.bundle")
	err := os.WriteFile(path, []byte("fixture"), 0600)
	return BranchSnapshot{Branch: branch, SHA: sha, Bundle: path}, err
}

type externalTestRepository struct {
	Repository
	revision Revision
	imports  int
	fail     bool
}

type externalRebasePublisher struct {
	*externalTestPublisher
	upstream *resumeTestPublisher
}

func (p *externalRebasePublisher) Observe(ctx context.Context, plan Plan, pub Publication) (RemotePR, error) {
	return p.upstream.Observe(ctx, plan, pub)
}
func (p *externalRebasePublisher) Branch(ctx context.Context, plan Plan, branch string) (BranchSnapshot, error) {
	return p.upstream.Branch(ctx, plan, branch)
}
func (p *externalRebasePublisher) ValidateResumeTarget(ctx context.Context, plan Plan, remote RemotePR) error {
	return p.upstream.ValidateResumeTarget(ctx, plan, remote)
}
func (p *externalRebasePublisher) Snapshot(ctx context.Context, plan Plan, branch, sha, dir string) (BranchSnapshot, error) {
	return p.externalTestPublisher.Snapshot(ctx, plan, branch, sha, dir)
}

type externalRebaseRepository struct{ *externalTestRepository }

func (r *externalRebaseRepository) Rebase(_ context.Context, _, _, _, _ string) (RebaseResult, error) {
	r.revision.Head = strings.Repeat("f", 40)
	return RebaseResult{}, nil
}

func (*externalTestRepository) FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error) {
	return nil, nil
}

func (r *externalTestRepository) Inspect(context.Context, string) (Revision, error) {
	return r.revision, nil
}
func (r *externalTestRepository) ImportExternalPR(_ context.Context, _, _ string, original Revision, _ ExternalPRBoundary, sha string) error {
	r.imports++
	if r.revision != original && r.revision.Head != sha {
		return errors.New("changed source")
	}
	r.revision.Head = sha
	r.revision.Tree = strings.Repeat("e", 40)
	if r.fail {
		return errors.New("interrupted")
	}
	return nil
}

func externalMergeFixture(t *testing.T) (string, *Journal, *externalTestPublisher, *externalTestRepository) {
	t.Helper()
	dir, j := testRun(t)
	b := externalBoundaryFixture()
	j.Plan.Branch, j.Plan.Base, j.Plan.BaseSHA = b.ParentBranch, b.ParentBase, b.ParentBaseSHA
	j.Publication = Publication{URL: b.ParentURL, Number: b.ParentNumber, BaseSHA: b.ParentBaseSHA, HeadSHA: b.ParentHeadSHA}
	j.State = "ready"
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	j.CI = CIResult{Status: "passed"}
	p := &externalTestPublisher{observation: ExternalPRObservation{Boundary: b, ParentTree: testTree, ParentState: "OPEN", IncomingState: "OPEN", IncomingBaseRepositoryID: b.RepositoryID, IncomingBaseBranch: b.ParentBranch, IncomingBaseSHA: b.ParentHeadSHA}}
	r := &externalTestRepository{revision: Revision{Head: testHead, Tree: testTree, Clean: true}}
	return dir, j, p, r
}

func TestExternalMergeRecoversUncertainRequestAndInvalidatesEvidence(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	p.uncertain = true
	runner := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := runner.mergeExternalPR(context.Background(), dir, j); err == nil {
		t.Fatal("uncertain merge should stop")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ExternalPR == nil || saved.ExternalPR.Completed || p.merges != 1 {
		t.Fatal("merge intent not retained")
	}
	runner.ExternalPRURL = ""
	if err := runner.mergeExternalPR(context.Background(), dir, &saved); err != nil {
		t.Fatal(err)
	}
	if p.merges != 1 || !saved.ExternalPR.Completed || saved.Publication.HeadSHA != p.observation.MergeSHA || saved.Evidence.Passed || saved.CI.Status != "" || saved.State != "implementing" || !saved.RestackNeedsChecks {
		t.Fatalf("incorrect recovery: %+v", saved.ExternalPR)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}

}

func TestExternalMergeRejectsWrongTargetBeforeRemoteMutation(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	p.observation.IncomingBaseBranch = "main"
	runner := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := runner.mergeExternalPR(context.Background(), dir, j); err == nil || p.merges != 0 || repo.imports != 0 {
		t.Fatal("wrong target was not rejected")
	}
}

func TestExternalMergeAcceptsExactPublishedHeadWithoutOldCheckEvidence(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	j.Evidence = CheckEvidence{}
	repo.revision.Head = j.Publication.HeadSHA
	runner := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := runner.mergeExternalPR(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if !j.ExternalPR.Completed || p.merges != 1 {
		t.Fatal("published head was not adopted")
	}
}

func TestExternalMergeRecoversInterruptedWorkspaceImport(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	repo.fail = true
	runner := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := runner.mergeExternalPR(context.Background(), dir, j); err == nil {
		t.Fatal("interrupted import should stop")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	repo.fail = false
	runner.ExternalPRURL = ""
	if err := runner.mergeExternalPR(context.Background(), dir, &saved); err != nil {
		t.Fatal(err)
	}
	if p.merges != 1 || repo.imports != 2 || !saved.ExternalPR.Completed {
		t.Fatal("import did not recover")
	}
}

func TestExternalMergeRunsFreshChecksAndCI(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	j.SessionID = testNative
	remote := &fakePublisher{}
	p.Publisher = remote
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &fakeChecker{}
	runner := Runner{Publisher: p, Repository: repo, Provider: provider, Checker: checker, ExternalPRURL: p.observation.Boundary.IncomingURL, Output: io.Discard, PollInterval: time.Millisecond, ReviewWorkspace: func(_ context.Context, j Journal, _ string) (string, error) {
		path := filepath.Join(j.Workspace, "fresh-review")
		return path, os.MkdirAll(path, 0700)
	}}
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checker.calls == 0 || remote.ci == 0 || p.metadataUpdates != 1 || j.CI.Status != "passed" || j.Publication.HeadSHA != p.observation.MergeSHA {
		t.Fatalf("checks or CI were not run on merged head: state=%s checks=%d ci=%d", j.State, checker.calls, remote.ci)
	}
}

func TestExternalMergeFailedCIEntersExistingRepairLoop(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	runner := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL, Output: io.Discard}
	if err := runner.mergeExternalPR(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.State, j.CIRepairBudget = testNative, "ci", &CIRepairBudget{StartRound: j.Rounds}
	remote := &fakePublisher{failures: 1}
	p.Publisher = remote
	if err := runner.checkCI(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if j.State != "repairing" || j.CIRepairBudget.LastGrantedHead != j.ExternalPR.MergeSHA || j.CIRepairBudget.Used != 1 || !strings.Contains(j.Feedback, j.ExternalPR.MergeSHA) {
		t.Fatal("failed CI did not enter repair for merged head")
	}
}

func TestExternalRequestReconcilesAdvancedBaseBeforeMerge(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	b := p.observation.Boundary
	upstream := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: b.ParentBase, BaseSHA: b.ParentBaseSHA, HeadSHA: b.ParentHeadSHA}, branchSHA: testTree}
	publisher := &externalRebasePublisher{externalTestPublisher: p, upstream: upstream}
	runner := Runner{Publisher: publisher, Repository: &externalRebaseRepository{repo}, ExternalPRURL: b.IncomingURL, CheckUpstream: true, Output: io.Discard}
	merged, err := runner.prepareExternalPR(context.Background(), dir, j)
	if err != nil {
		t.Fatal(err)
	}
	if merged || p.merges != 0 || j.ExternalPRRequest != b.IncomingURL || j.Plan.BaseSHA != testTree || j.State != "implementing" {
		t.Fatalf("request did not wait for rebase: merged=%v state=%s", merged, j.State)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	// After rebase has been checked and published, reassess the incoming PR
	// against the newly published parent and finish the merge on resume.
	newHead := strings.Repeat("9", 40)
	p.observation.Boundary.ParentBaseSHA = testTree
	p.observation.Boundary.ParentHeadSHA = newHead
	p.observation.IncomingBaseSHA = newHead
	p.observation.ParentTree = repo.revision.Tree
	upstream.remote.BaseSHA, upstream.remote.HeadSHA = testTree, newHead
	j.Publication.BaseSHA, j.Publication.HeadSHA = testTree, newHead
	j.Plan.Restack = nil
	j.Evidence = CheckEvidence{Head: repo.revision.Head, Tree: repo.revision.Tree, Passed: true}
	j.State = "ready"
	runner.ExternalPRURL = ""
	merged, err = runner.prepareExternalPR(context.Background(), dir, j)
	if err != nil {
		t.Fatal(err)
	}
	if !merged || p.merges != 1 || !j.ExternalPR.Completed || j.ExternalPRRequest != "" {
		t.Fatal("request did not continue after rebase")
	}
}

func TestPendingExternalRequestAllowsAnswerDuringRebase(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	j.ExternalPRRequest = p.observation.Boundary.IncomingURL
	j.State, j.PendingRole, j.SessionID = "waiting_for_human", "implementation", testNative
	j.Outcome.Questions = []string{"Resolve the conflict?"}
	provider := &fakeProvider{executeErr: errors.New("provider unavailable")}
	runner := Runner{Publisher: p, Repository: repo, Provider: provider, Checker: &fakeChecker{}, Output: io.Discard}
	err := runner.Run(context.Background(), dir, j, "Use the incoming change")
	if err == nil || strings.Contains(err.Error(), "cannot combine") || len(provider.calls) == 0 {
		t.Fatalf("answer could not reach the original session: %v", err)
	}
	if j.ExternalPRRequest == "" {
		t.Fatal("answer discarded the pending external PR")
	}
}
