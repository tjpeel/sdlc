package workrun

import (
	"context"
	"errors"
	"github.com/tjpeel/sdlc/internal/featureguard"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func publishedFeatureRuntimeFixture(t *testing.T) (*Journal, runtimeimage.State) {
	t.Helper()
	_, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "ci", "CI unavailable"
	j.SessionID = testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.Plan.RuntimeName = "old"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	j.Outcome = testOutcome("implemented")
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true, Key: strings.Repeat("1", 64)}
	j.CI.Status = "passed"
	return j, runtimeimage.State{Name: "", ImageID: "sha256:" + strings.Repeat("b", 64)}
}

func TestPublishedFeatureRuntimePreparationPreservesPRAndInvalidatesEvidence(t *testing.T) {
	original, target := publishedFeatureRuntimeFixture(t)
	next, err := PreparePublishedFeatureRuntimeMigration(*original, "", target)
	if err != nil {
		t.Fatal(err)
	}
	if next.Publication != original.Publication || next.Plan.Root != original.Plan.Root || next.ID != original.ID {
		t.Fatal("publication or run identity changed")
	}
	if next.Evidence.Passed || next.Evidence.Key != "" || next.CI.Status != "" || next.SessionID != "" || next.ResumeState != "runtime_recheck" || next.State != "blocked" {
		t.Fatal("prior runtime evidence or session remained current")
	}
	if next.RuntimeRecheck == nil || !next.RuntimeRecheck.Pending || next.RuntimeRecheck.Publication != original.Publication || next.RuntimeRecheck.CandidateHead != original.Evidence.Head || next.RuntimeRecheck.CandidateTree != original.Evidence.Tree {
		t.Fatal("unchanged PR and candidate boundaries not retained for recheck")
	}
	if len(next.RuntimeMigrations) != 1 || next.RuntimeMigrations[0].PreviousSessionID != original.SessionID || next.RuntimeMigrations[0].PreviousImageID != original.ImageID {
		t.Fatal("session lineage not retained")
	}
	if !original.Evidence.Passed || original.SessionID == "" || original.CI.Status != "passed" {
		t.Fatal("original checkpoint mutated")
	}
}

func TestPublishedFeatureRuntimePreparationRejectsPendingWork(t *testing.T) {
	for _, mutate := range []func(*Journal){
		func(j *Journal) { j.ReviewReports = []ReviewReport{{}} },
		func(j *Journal) { j.PendingInputs = &PendingInputRecovery{} },
		func(j *Journal) { j.Outcome.Findings = []Finding{{}} },
		func(j *Journal) { j.State = "ready" },
		func(j *Journal) { j.Publication.HeadSHA = testTree[:20] },
		func(j *Journal) { j.PRFeedback = &PRFeedbackHistory{} },
	} {
		original, target := publishedFeatureRuntimeFixture(t)
		mutate(original)
		if _, err := PreparePublishedFeatureRuntimeMigration(*original, "", target); err == nil {
			t.Fatal("unsafe checkpoint admitted")
		}
	}
}

func TestPendingFeatureRuntimeMigrationBlocksOrdinaryRunAccess(t *testing.T) {
	dir, j := testRun(t)
	root := t.TempDir()
	j.Plan.Root = root
	series := filepath.Join(root, ".sdlc", "work", j.Plan.Reference, "series")
	if err := os.MkdirAll(series, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(series, featureguard.RuntimeMigrationPending)
	if err := os.WriteFile(marker, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("ordinary load bypassed pending migration")
	}
	if err := Save(dir, j); err == nil {
		t.Fatal("ordinary save bypassed pending migration")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

type runtimeRecheckRepository struct{ *fakeRepository }

func (*runtimeRecheckRepository) FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error) {
	return nil, nil
}

func runtimeRecheckFixture(t *testing.T) (string, *Journal, *fakeChecker, *ciHeldPublisher, *runtimeRecheckRepository, Runner) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "ci", "old CI hold"
	j.SessionID = testNative
	j.ImageID = "sha256:" + strings.Repeat("a", 64)
	j.Plan.RuntimeName = "old"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	j.Outcome = testOutcome("implemented")
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	target := runtimeimage.State{ImageID: "sha256:" + strings.Repeat("b", 64)}
	next, err := PreparePublishedFeatureRuntimeMigration(*j, "", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, &next); err != nil {
		t.Fatal(err)
	}
	checker := &fakeChecker{}
	pub := &ciHeldPublisher{fakePublisher: &fakePublisher{}}
	repo := &runtimeRecheckRepository{fakeRepository: &fakeRepository{}}
	runner := fakeRunner(&fakeProvider{}, checker, pub.fakePublisher, repo.fakeRepository)
	runner.Publisher = pub
	runner.Repository = repo
	return dir, &next, checker, pub, repo, runner
}

func TestFeatureRuntimeRecheckReturnsToCIWithoutPublishing(t *testing.T) {
	dir, j, checker, pub, _, runner := runtimeRecheckFixture(t)
	prior := j.Publication
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ci" || j.StopReason != "old CI hold" {
		t.Fatalf("CI hold not restored: %v", err)
	}
	if j.Publication != prior || pub.calls != 0 || pub.ci != 0 || checker.calls != 1 || !j.Evidence.Passed || j.RuntimeRecheck.Pending {
		t.Fatal("recheck published, skipped checks or changed PR")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Publication != prior || !saved.Evidence.Passed || saved.RuntimeRecheck.Pending {
		t.Fatal("recheck checkpoint did not survive restart")
	}
}

func TestFeatureRuntimeRecheckStopsOnChangedCandidateOrPR(t *testing.T) {
	for _, tc := range []string{"candidate_before", "candidate_after", "pr_before", "pr_after", "check_failure"} {
		t.Run(tc, func(t *testing.T) {
			dir, j, checker, pub, repo, runner := runtimeRecheckFixture(t)
			prior := j.Publication
			switch tc {
			case "candidate_before":
				repo.revisions = []Revision{{Head: testTree, Tree: testTree, Clean: true}}
			case "candidate_after":
				repo.revisions = []Revision{{Head: testHead, Tree: testTree, Clean: true}, {Head: testTree, Tree: testTree, Clean: true}}
			case "pr_before":
				pub.failAt = 1
			case "pr_after":
				pub.failAt = 2
			case "check_failure":
				checker.failures = 1
			}
			if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
				t.Fatalf("expected held recheck: %v", err)
			}
			if j.State != "blocked" || j.ResumeState != "runtime_recheck" || !j.RuntimeRecheck.Pending || j.Evidence.Passed || pub.calls != 0 || j.Publication != prior {
				t.Fatal("failed recheck published or certified stale work")
			}
		})
	}
}
