package workrun

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdenticalContentReusesReviewAndRetainsOriginalBoundary(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	original := j.ReviewReports[len(j.ReviewReports)-1]
	if original.Content == nil {
		t.Fatal("completed review did not record content")
	}
	j.ReviewReports[0].Recommendations = []Finding{{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Repeated logic can drift.", Recommendation: "Share the mapper."}}
	j.ReviewReports[0].Limitations = []string{"External provider preview was not run."}
	j.Plan.Roles.AutoAcceptRecommendations = true
	j.State = "awaiting_reviewer"
	j.Publication.HeadSHA = testSigned
	r.Repository = reviewReuseSnapshotRepository{Repository: r.Repository, Head: testSigned, Tree: j.Evidence.Tree}
	j.Outcome = testOutcome("implemented")
	pub.heads = []string{testSigned}
	provider := &feedbackScriptProvider{}
	r.Provider = provider
	attempt := j.Attempt
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.ReviewReuse == nil || !reviewReuseCurrent(j) || len(provider.sessions) != 0 || j.Attempt != attempt || checker.calls != 1 {
		t.Fatalf("review or checks repeated: state=%s reuse=%+v sessions=%d checks=%d", j.State, j.ReviewReuse, len(provider.sessions), checker.calls)
	}
	if len(j.ReviewReports) != 1 || j.ReviewReports[0].Publication != original.Publication || j.ReviewReports[0].Coverage.HeadSHA != original.Publication.HeadSHA {
		t.Fatal("original review was rebound or fabricated")
	}
	saved, err := Load(dir)
	if err != nil || !reviewReuseCurrent(&saved) {
		t.Fatalf("reuse not durable: %v", err)
	}
	if !strings.Contains(ReviewWarningNotice(saved), "External provider preview") || !strings.Contains(ReviewRecommendationNotice(saved), "Share the mapper") {
		t.Fatal("reused review lost original warnings or recommendations")
	}
	if saved.PRFeedback != nil && saved.PRFeedback.Active {
		t.Fatal("reused report reopened accepted recommendations")
	}
	if err := ValidatePRFeedbackResume(saved); err != nil {
		t.Fatalf("reused review cannot accept later feedback: %v", err)
	}
}

func TestReviewReuseRequiresCompleteUnchangedScopeAndContent(t *testing.T) {
	for _, scenario := range []string{"tree", "base", "requirements", "metadata", "instructions", "companion", "partial", "findings", "legacy", "pending-question", "interrupted", "dirty", "published-tree", "published-head"} {
		t.Run(scenario, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.State = "awaiting_reviewer"
			j.Publication.HeadSHA = testSigned
			j.Outcome = testOutcome("implemented")
			report := &j.ReviewReports[len(j.ReviewReports)-1]
			switch scenario {
			case "tree":
				j.Evidence.Tree = testSigned
			case "base":
				j.Publication.BaseSHA = testSigned
			case "requirements":
				j.Plan.Inputs = append(j.Plan.Inputs, Input{Path: "ticket.md", SHA256: strings.Repeat("a", 64)})
			case "metadata":
				j.Plan.PRBody += "Changed claim"
			case "instructions":
				j.Instructions += "Changed scope"
			case "companion":
				j.CompanionRepositories = append(j.CompanionRepositories, CompanionRepository{})
			case "partial":
				report.Coverage.Scope = "partial"
				report.Coverage.Gaps = []string{"unfinished"}
			case "findings":
				report.Findings = []Finding{{Priority: "P2"}}
			case "legacy":
				report.Content = nil
			case "pending-question":
				j.Outcome.Questions = []string{"Decision needed"}
			case "interrupted":
				j.ReviewInterruption = &ReviewInterruption{}
			case "published-tree":
				r.Repository = reviewReuseSnapshotRepository{Repository: r.Repository, Head: testSigned, Tree: testSigned}
			case "published-head":
				r.Repository = reviewReuseSnapshotRepository{Repository: r.Repository, Head: testHead, Tree: j.Evidence.Tree}
			case "dirty":
				r.Repository = &fakeRepository{revisions: []Revision{{Head: j.Evidence.Head, Tree: j.Evidence.Tree, Clean: false}}}
			}
			reused, err := r.tryReuseReview(context.Background(), dir, j)
			if err != nil || reused || j.ReviewReuse != nil {
				t.Fatalf("unsafe reuse: %v %v", reused, err)
			}
		})
	}
}

func TestReusedReviewStillRequiresCurrentHeadCI(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	j.Publication.HeadSHA = testSigned
	r.Repository = reviewReuseSnapshotRepository{Repository: r.Repository, Head: testSigned, Tree: j.Evidence.Tree}
	j.State = "awaiting_reviewer"
	j.Outcome = testOutcome("implemented")
	if reused, err := r.tryReuseReview(context.Background(), dir, j); err != nil || !reused {
		t.Fatal(reused, err)
	}
	pub.ciErr = context.DeadlineExceeded
	done, err := r.finishCompletedReview(context.Background(), dir, j)
	if err == nil || done || j.State == "ready" {
		t.Fatal("failed current-head CI certified readiness")
	}
}

type reviewReuseSnapshotRepository struct {
	Repository
	Head, Tree string
}

func (r reviewReuseSnapshotRepository) Inspect(ctx context.Context, path string) (Revision, error) {
	if filepath.Base(path) == "fresh-review" {
		return Revision{Head: r.Head, Tree: r.Tree, Clean: true}, nil
	}
	return r.Repository.Inspect(ctx, path)
}
