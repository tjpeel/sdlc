package workrun

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIncrementalReviewCompanionChangeRequiresFreshFullInspection(t *testing.T) {
	dir, j, runner, repo := correctionReviewFixture(t)
	if err := runner.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil || j.ReviewPlan == nil {
		t.Fatalf("initial plan: %v", err)
	}
	originalReport, _ := json.Marshal(j.ReviewReports)
	originalFeedback, _ := json.Marshal(j.PRFeedback)
	publication, evidence := j.Publication, j.Evidence
	j.CompanionRepositories = append(j.CompanionRepositories, CompanionRepository{Request: CompanionRepositoryRequest{ID: "docs", Repository: "example/companion", Purpose: "Review documentation"}, Path: ".sdlc/repositories/example/companion", Revision: strings.Repeat("a", 40)})
	if err := runner.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	plan := j.ReviewPlan
	if !plan.Escalated || plan.FullContextKey != incrementalContextKey(j) || plan.ContextKey == plan.FullContextKey {
		t.Fatal("changed context did not revoke inherited inspection")
	}
	if !strings.Contains(incrementalReviewInstructions(j), "earlier inspection is not credited") {
		t.Fatal("full review prompt did not revoke inherited inspection")
	}
	afterReport, _ := json.Marshal(j.ReviewReports)
	afterFeedback, _ := json.Marshal(j.PRFeedback)
	if string(originalReport) != string(afterReport) || string(originalFeedback) != string(afterFeedback) || j.Publication != publication || !reflect.DeepEqual(j.Evidence, evidence) {
		t.Fatal("escalation changed existing evidence or feedback")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.prepareIncrementalReview(context.Background(), dir, &saved, saved.Workspace); err != nil {
		t.Fatalf("saved full plan did not resume: %v", err)
	}
	out := correctionOutcome(&saved, "src/a.cs", "src/b.cs")
	resolveCorrectionConcerns(&saved, &out)
	if err := runner.reconcilePlannedInspection(context.Background(), saved.Workspace, &saved, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "review_incomplete" {
		t.Fatal("old inherited file was credited in full review")
	}
	out = correctionOutcome(&saved, "src/a.cs", "src/b.cs", "src/c.cs")
	resolveCorrectionConcerns(&saved, &out)
	if err := runner.reconcilePlannedInspection(context.Background(), saved.Workspace, &saved, &out); err != nil || out.Status != "reviewed" {
		t.Fatalf("fresh full inspection failed: %v %s", err, out.Status)
	}
	repo.final = append(repo.final, "src/new.cs")
	if err := runner.prepareIncrementalReview(context.Background(), dir, &saved, saved.Workspace); err == nil {
		t.Fatal("changed publication manifest was accepted")
	}
}

func TestIncrementalReviewScopeChangeKeepsUnfinishedReviewerWork(t *testing.T) {
	dir, j, runner, _ := correctionReviewFixture(t)
	if err := runner.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err != nil {
		t.Fatal(err)
	}
	j.ReviewContinuation = &ReviewContinuation{SessionID: "existing-session"}
	j.Instructions += " changed requirement"
	if err := runner.prepareIncrementalReview(context.Background(), dir, j, j.Workspace); err == nil || j.ReviewContinuation == nil || j.ReviewPlan.Escalated {
		t.Fatal("unfinished reviewer work was discarded")
	}
}
