package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEmptyCIStartupResponseHandlesChecksAppearing(t *testing.T) {
	for _, tc := range []struct {
		name, rollup, want string
		changed            bool
	}{
		{"empty", "[]", "missing", false}, {"null", "null", "missing", false},
		{"appeared", `[{"name":"tests","status":"IN_PROGRESS"}]`, "pending", false},
		{"changed", `[]`, "", true}, {"missing-field", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			views := 0
			p := GitHubPublisher{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "pr checks") {
					return nil, ciStatusExit(1)
				}
				views++
				base := testBase
				if tc.changed && views > 1 {
					base = testTree
				}
				m := map[string]any{"baseRefOid": base, "headRefOid": testHead, "state": "OPEN"}
				if views > 1 && tc.rollup != "" {
					m["statusCheckRollup"] = json.RawMessage(tc.rollup)
				}
				return json.Marshal(m)
			}}
			got, err := p.Checks(context.Background(), Plan{Repository: "example/project"}, Publication{Number: 1, BaseSHA: testBase, HeadSHA: testHead})
			if tc.want == "" {
				if err == nil {
					t.Fatal("unverified response accepted")
				}
				return
			}
			if err != nil || got.Status != tc.want {
				t.Fatalf("startup response: %+v %v; want %s", got, err, tc.want)
			}
		})
	}
}

func completedReviewFixture(t *testing.T) (string, *Journal, *fakeProvider, *fakePublisher, Runner) {
	t.Helper()
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "reviewing"
	j.StopReason = "PR or CI changed during independent review"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	j.Attempt = 2
	j.Outcome = testOutcome("reviewed")
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	if err := recordReviewReport(j); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	pub := &fakePublisher{}
	runner := fakeRunner(provider, &fakeChecker{}, pub, &fakeRepository{})
	return dir, j, provider, pub, runner
}

func TestCompletedReviewSurvivesCIReadFailureAndResume(t *testing.T) {
	dir, j, provider, pub, runner := completedReviewFixture(t)
	pub.ciErr = &ciRequestError{Status: 503}
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || !strings.Contains(j.StopReason, "HTTP 503") || len(provider.calls) != 0 {
		t.Fatalf("review lost or error hidden: %v, %s, turns %d", err, j.StopReason, len(provider.calls))
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub.ciErr = nil
	if err := runner.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(provider.calls) != 0 || len(saved.ReviewReports) != 1 {
		t.Fatalf("review repeated: state %s, turns %d reports %d", saved.State, len(provider.calls), len(saved.ReviewReports))
	}
}

func TestCompletedReviewWaitsForPendingCIWithoutAnotherProviderTurn(t *testing.T) {
	dir, j, provider, pub, runner := completedReviewFixture(t)
	pub.statuses = []string{"pending", "passed"}
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(provider.calls) != 0 || pub.ci != 2 || len(j.ReviewReports) != 1 {
		t.Fatalf("did not retain review through CI polling: state %s turns %d reads %d", j.State, len(provider.calls), pub.ci)
	}
}

func TestCompletedReviewUsesConfiguredManualWorkflowEvidence(t *testing.T) {
	dir, j, provider, _, runner := completedReviewFixture(t)
	j.State, j.ResumeState, j.StopReason = "reviewing", "", ""
	j.Plan.Base = "parent"
	f := &dispatchFixture{plan: j.Plan, pr: j.Publication, status: "in_progress", head: j.Publication.HeadSHA, base: "main"}
	workflow := "check.yml"
	runner.MissingCheckGrace = time.Minute
	runner.NonDefaultCIWorkflow = &workflow
	runner.Publisher = GitHubPublisher{Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "pr checks") {
			return []byte(`[{"name":"tests","bucket":"pass","state":"SUCCESS"}]`), nil
		}
		return f.command(ctx, name, args...)
	}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	// This run already dispatched validation when its PR rollup was empty.
	if _, err := runner.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"}); err != nil {
		t.Fatal(err)
	}
	// A green PR rollup alone cannot overrule its still-running manual workflow.
	done, err := runner.finishCompletedReview(context.Background(), dir, j)
	if err != nil || done || j.State == "ready" || j.CI.Status != "pending" || f.posts != 1 {
		t.Fatalf("manual workflow ignored: %v %v %+v posts %d", done, err, j.CI, f.posts)
	}
	f.status = "completed"
	f.runVisible = true
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(provider.calls) != 0 || f.posts != 1 {
		t.Fatalf("workflow not reconciled without review or redispatch: %s turns %d posts %d", j.State, len(provider.calls), f.posts)
	}
}

func TestCompletedReviewDoesNotCertifyChangedPublication(t *testing.T) {
	for _, changed := range []string{"head", "base"} {
		t.Run(changed, func(t *testing.T) {
			dir, j, _, _, runner := completedReviewFixture(t)
			if changed == "head" {
				j.Publication.HeadSHA = testTree
			} else {
				j.Publication.BaseSHA = testTree
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), dir, j, ""); err == nil || j.State == "ready" {
				t.Fatalf("old review certified changed %s", changed)
			}
		})
	}
}

type afterReviewFailurePublisher struct {
	*fakePublisher
	fail  bool
	reads int
}

func (p *afterReviewFailurePublisher) Checks(ctx context.Context, plan Plan, pr Publication) (CIResult, error) {
	p.reads++
	if p.fail && p.reads >= 3 {
		return CIResult{}, fmt.Errorf("CI credentials temporarily unavailable")
	}
	return p.fakePublisher.Checks(ctx, plan, pr)
}
func TestNewlyCompletedReviewIsSavedBeforeFinalCIRead(t *testing.T) {
	dir, j := testRun(t)
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	pub := &afterReviewFailurePublisher{fakePublisher: &fakePublisher{}, fail: true}
	runner := fakeRunner(provider, &fakeChecker{}, pub.fakePublisher, &fakeRepository{})
	runner.Publisher = pub
	err := runner.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || !strings.Contains(j.StopReason, "CI credentials temporarily unavailable") {
		t.Fatalf("actual cause lost: %v %s", err, j.StopReason)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ReviewReports) != 1 || saved.Outcome.Status != "reviewed" || len(provider.calls) != 2 {
		t.Fatal("completed review was not checkpointed")
	}
	pub.fail = false
	if err := runner.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(provider.calls) != 2 || len(saved.ReviewReports) != 1 {
		t.Fatal("ordinary resume repeated the completed review")
	}
}
