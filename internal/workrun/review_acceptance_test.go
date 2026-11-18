package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewAcceptanceResumesThroughLiveVerification(t *testing.T) {
	dir, j := overrideFixture(t)
	if err := GrantReviewAcceptance(dir, j.UpdatedAt, "No affected data was deployed."); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, &loaded, ""); err != nil {
		t.Fatal(err)
	}
	if loaded.State != "ready" || len(p.calls) != 0 || len(loaded.Outcome.Findings) != 1 || pub.ci < 2 {
		t.Fatal("accepted review was not retained through verification")
	}
	if err := ValidatePRFeedbackResume(loaded); err != nil {
		t.Fatalf("accepted findings prevented later feedback: %v", err)
	}
}

func TestReviewAcceptanceRejectsChangedLiveBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		pub  *fakePublisher
		repo *fakeRepository
	}{
		{"ci", &fakePublisher{statuses: []string{"failed"}}, &fakeRepository{}},
		{"source", &fakePublisher{}, &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}}},
		{"published tree", &fakePublisher{}, &fakeRepository{revisions: []Revision{{Head: testHead, Tree: testTree, Clean: true}, {Head: testHead, Tree: testBase, Clean: true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, j := overrideFixture(t)
			if err := GrantReviewAcceptance(dir, j.UpdatedAt, "reason"); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			p := &fakeProvider{}
			r := fakeRunner(p, &fakeChecker{}, tc.pub, tc.repo)
			if err := r.Run(context.Background(), dir, &loaded, ""); err == nil || loaded.State == "ready" || len(p.calls) != 0 {
				t.Fatal("changed boundary allowed through")
			}
		})
	}
}

func overrideFixture(t *testing.T) (string, *Journal) {
	t.Helper()
	dir, j := legacyReviewRun(t)
	if err := os.WriteFile(filepath.Join(dir, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	j.StopReason = reviewUnchangedBound
	j.Outcome.ReviewCoverage = coverage("full")
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3, Used: 1, StartRound: j.Rounds - 1, LastGrantedHead: j.Publication.HeadSHA}
	j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Findings: append([]Finding(nil), j.Outcome.Findings...), StartedAttempt: j.Attempt - 1, Acknowledged: true, Summary: "disagreed"}
	if err := recordReviewReport(j); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j
}

func TestReviewAcceptanceRetainsDecisionAndDoesNotAlterReview(t *testing.T) {
	dir, j := overrideFixture(t)
	if err := GrantReviewAcceptance(dir, j.UpdatedAt, "No affected data has been deployed."); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !currentReviewAcceptance(&got) || !completedReviewAwaitingCI(&got) || len(got.Outcome.Findings) != 1 || got.ReviewAcceptances[0].Reason != "No affected data has been deployed." || got.State != "blocked" {
		t.Fatal("decision or original review was lost")
	}
	got.Publication.HeadSHA = testBase
	if currentReviewAcceptance(&got) || completedReviewAwaitingCI(&got) {
		t.Fatal("override reused for another head")
	}
}

func TestReviewAcceptanceRejectsAlteredRecordedReview(t *testing.T) {
	dir, j := overrideFixture(t)
	if err := GrantReviewAcceptance(dir, j.UpdatedAt, "reason"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got.ReviewReports[len(got.ReviewReports)-1].Coverage.Scope = "partial"
	if currentReviewAcceptance(&got) || validateReviewAcceptances(&got) == nil {
		t.Fatal("altered review accepted")
	}
}

func TestReviewAcceptanceRejectsUnrelatedOrStaleHolds(t *testing.T) {
	dir, j := overrideFixture(t)
	if err := GrantReviewAcceptance(dir, j.UpdatedAt.Add(-time.Second), "reason"); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	for _, reason := range []string{"", "   ", strings.Repeat("a", 8193)} {
		if ValidateReviewAcceptance(*j, reason) == nil {
			t.Fatal("invalid reason accepted")
		}
	}
	for _, mutate := range []func(*Journal){
		func(j *Journal) { j.StopReason = "other failure" },
		func(j *Journal) { j.CI.Status = "failed" },
		func(j *Journal) { j.Evidence.Tree = testBase },
		func(j *Journal) { j.Outcome.Questions = []string{"question"} },
		func(j *Journal) { j.Outcome.Findings[0].Scenario = "different finding" },
		func(j *Journal) { j.ReviewReports[len(j.ReviewReports)-1].Coverage.Scope = "partial" },
	} {
		_, candidate := overrideFixture(t)
		mutate(candidate)
		if ValidateReviewAcceptance(*candidate, "reason") == nil {
			t.Fatal("unrelated hold accepted")
		}
	}
}
