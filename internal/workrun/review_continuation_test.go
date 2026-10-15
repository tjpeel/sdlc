package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/tjpeel/sdlc/internal/providerauth"
	"os"
	"path/filepath"
	"testing"
)

func unfinishedReview(gaps ...string) Outcome {
	out := testOutcome("reviewed")
	out.Status = "review_incomplete"
	out.ReviewCoverage = coverage("partial", gaps...)
	return out
}

func continuationFixture(t *testing.T, outcomes ...Outcome) (string, *Journal, Runner, *fakeProvider, string) {
	t.Helper()
	dir, j := testRun(t)
	j.State = "reviewing"
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: testBase, HeadSHA: testHead}
	j.SessionID = "00000000-0000-4000-8000-000000000009"
	p := &fakeProvider{outcomes: outcomes}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ReviewWorkspace = func(context.Context, Journal, string) (string, error) {
		workspace := filepath.Join(dir, "review-source-fixture")
		return workspace, os.MkdirAll(workspace, 0700)
	}
	workspace, err := r.prepareIndependentReview(context.Background(), dir, j)
	if err != nil {
		t.Fatal(err)
	}
	return dir, j, r, p, workspace
}

func TestUnfinishedReviewResumesSameOwnerWithoutRepair(t *testing.T) {
	first := unfinishedReview("Search tests", "Recovery guidance")
	second := unfinishedReview("Recovery guidance")
	second.ReviewCoverage.CompletedGaps = []string{"Search tests"}
	dir, j, r, p, workspace := continuationFixture(t, first, second, testOutcome("reviewed"))
	original := j.SessionID
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome.Status != "reviewed" || len(p.calls) != 3 || j.ReviewContinuation != nil || j.SessionID != original || j.Rounds != 0 || j.ReviewAssessment != nil {
		t.Fatal("inspection changed implementation state")
	}
	for _, call := range p.calls[1:] {
		if call.ResumeID != testNative || call.Directory != p.calls[0].Directory || call.Workspace != workspace || call.Model != p.calls[0].Model {
			t.Fatal("review session or snapshot changed")
		}
	}
	if len(j.ReviewReports) != 2 {
		t.Fatal("unfinished reports lost")
	}
}

func TestReviewWithoutProgressStopsInsteadOfRestarting(t *testing.T) {
	for _, next := range []Outcome{unfinishedReview("Tests"), unfinishedReview("The same tests, paraphrased")} {
		dir, j, r, p, workspace := continuationFixture(t, unfinishedReview("Tests"), next)
		result, err := r.independentReview(context.Background(), dir, j, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome.Status != "blocked" || j.ReviewContinuation == nil || len(p.calls) != 2 || j.ReviewContinuation.Gaps[0] != "Tests" {
			t.Fatal("unfinished inspection looped or lost its original work")
		}
	}
}

func TestNativeFailureRetainsSessionAndUnfinishedEvidence(t *testing.T) {
	first := unfinishedReview("Tests")
	first.Findings = []Finding{{Priority: "P2", Path: "api.go", Line: 1, Scenario: "Boundary validation is missing", Recommendation: "Validate before writing"}}
	dir, j, r, p, workspace := continuationFixture(t, first)
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err == nil || result.ReviewInterruption == nil || j.ReviewContinuation == nil || len(result.ReviewInterruption.Findings) != 1 {
		t.Fatal("native failure erased unfinished review")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.outcomes = []Outcome{testOutcome("reviewed")}
	result, err = r.independentReview(context.Background(), dir, &saved, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || p.calls[2].ResumeID != testNative || p.calls[2].Directory != p.calls[0].Directory {
		t.Fatal("saved continuation restarted owner", err)
	}
	if len(saved.ReviewReports[0].Findings) != 1 {
		t.Fatal("interim finding audit was lost")
	}
}

func TestGenuineBlockerDoesNotReceiveAutomaticContinuation(t *testing.T) {
	blocker := testOutcome("blocked")
	blocker.Summary = "Required external evidence is unavailable"
	blocker.ReviewCoverage = coverage("partial", "Unavailable external evidence")
	dir, j, r, p, workspace := continuationFixture(t, blocker)
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "blocked" || len(p.calls) != 1 || j.ReviewContinuation != nil {
		t.Fatal("retried genuine blocker", err)
	}
}

func TestContinuedReviewRefusesChangedExecutionOrSnapshot(t *testing.T) {
	for _, kind := range []string{"head", "base", "model", "speed", "tree", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, workspace := continuationFixture(t, unfinishedReview("Tests"))
			_, _ = r.independentReview(context.Background(), dir, j, workspace)
			p.outcomes = []Outcome{testOutcome("reviewed")}
			switch kind {
			case "head":
				j.Publication.HeadSHA = testBase
			case "base":
				j.Publication.BaseSHA = testHead
			case "model":
				j.Plan.Roles.Review.Name = "other-model"
			case "speed":
				j.Plan.Roles.ReviewSpeed = "fast"
			case "tree":
				r.Repository = &fakeRepository{revisions: []Revision{{Clean: true, Head: testHead, Tree: testBase}}}
			case "symlink":
				if err := os.Remove(workspace); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), workspace); err != nil {
					t.Fatal(err)
				}
			}
			before := len(p.calls)
			if _, err := r.independentReview(context.Background(), dir, j, workspace); err == nil || len(p.calls) != before {
				t.Fatal("changed boundary reached reviewer")
			}
		})
	}
}

func TestFinishedInspectionMayRetainExternalVerificationLimitations(t *testing.T) {
	out := testOutcome("reviewed")
	out.Limitations = []string{"Deployed dependency settings were unavailable"}
	dir, j, r, p, workspace := continuationFixture(t, out)
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || len(p.calls) != 1 || len(result.Outcome.Limitations) != 1 {
		t.Fatal("external verification limitation forced code rereview", err)
	}
}

func TestIncompleteOutcomeIsReviewOnly(t *testing.T) {
	out := unfinishedReview("Tests")
	if validateOutcome(out, "review") != nil || validateOutcome(out, "implementation") == nil {
		t.Fatal("wrong incomplete role semantics")
	}
	out.ReviewCoverage = coverage("full")
	if validateOutcome(out, "review") == nil {
		t.Fatal("incomplete review accepted full coverage")
	}
}

type usageAfterIncompleteReview struct {
	first     *fakeProvider
	exhausted *exhaustedReviewProvider
}

func (p *usageAfterIncompleteReview) Status(ctx context.Context, name string) (string, error) {
	return p.first.Status(ctx, name)
}
func (p *usageAfterIncompleteReview) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	if s.Role == "review" && s.ResumeID != "" {
		return p.exhausted.Execute(ctx, s, out, diag)
	}
	return p.first.Execute(ctx, s, out, diag)
}

func TestUsageLimitCannotSkipUnfinishedReview(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.State, j.Outcome = "reviewing", testOutcome("implemented")
			if provider == "codex" {
				j.Plan.Roles = DefaultModels().Claude
				j.ReviewEffort = nil
			}
			quota := &exhaustedReviewProvider{event: rejectedClaudeReview, err: errNativeIncomplete}
			if provider == "codex" {
				quota.event = rejectedCodexReview
				quota.err = &providerauth.NativeExecutionError{Provider: provider}
			}
			first := &fakeProvider{outcomes: []Outcome{unfinishedReview("Search tests")}}
			r.Provider = &usageAfterIncompleteReview{first: first, exhausted: quota}
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
				t.Fatal("usage failure did not stop", err)
			}
			saved, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.State != "blocked" || saved.ReviewSkip != nil || saved.ReviewContinuation == nil || saved.ReviewInterruption == nil || len(quota.calls) != 1 || quota.calls[0].ResumeID != testNative {
				t.Fatal("usage exhaustion bypassed unfinished inspection")
			}
			if len(saved.ReviewInterruption.Coverage.Gaps) != 1 {
				t.Fatal("unfinished inspection disappeared")
			}
		})
	}
}

func TestIncompleteNativeHandoffUsesBothProviderFormats(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		out := unfinishedReview("Search tests")
		raw := codexEvents(out)
		if provider == "claude" {
			data, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": out})
			raw = string(data)
		}
		writer := eventWriter{provider: provider}
		if _, err := writer.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := writer.finish("review"); err != nil || writer.result.Outcome.Status != "review_incomplete" {
			t.Fatal("native unfinished handoff rejected", provider, err)
		}
	}
}

func TestCompanionRequestClearsContinuationButRetainsPriorFindings(t *testing.T) {
	first := unfinishedReview("Producer behavior")
	first.Findings = []Finding{{Priority: "P2", Path: "api.go", Line: 1, Scenario: "The producer contract is uncertain", Recommendation: "Verify the producer boundary"}}
	request := testOutcome("checks_requested")
	request.RepositoryRequests = []CompanionRepositoryRequest{{ID: "producer", Repository: "example/producer", Purpose: "Inspect the selected producer boundary"}}
	dir, j, r, _, workspace := continuationFixture(t, first, request)
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "checks_requested" || j.ReviewContinuation != nil {
		t.Fatal("source expansion retained stale continuation", err)
	}
	if len(j.ReviewReports) != 1 || len(j.ReviewReports[0].Findings) != 1 {
		t.Fatal("source request discarded prior findings")
	}
}
