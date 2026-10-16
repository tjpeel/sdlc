package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func coverage(scope string, gaps ...string) *ReviewCoverage {
	if gaps == nil {
		gaps = []string{}
	}
	return &ReviewCoverage{BaseSHA: testBase, HeadSHA: testHead, Scope: scope, Gaps: gaps}
}

func TestNativeReviewRequiresCoverageForBothProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			out := testOutcome("reviewed")
			out.ReviewCoverage = nil
			data := codexEvents(out)
			if provider == "claude" {
				raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": out})
				data = string(raw)
			}
			w := eventWriter{provider: provider}
			if _, err := w.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
			if err := w.finish("review"); err == nil {
				t.Fatal("completed review accepted without structured coverage")
			}
		})
	}
}

func TestReviewRejectsUnstatedGapsAndWrongBoundary(t *testing.T) {
	for _, scope := range []string{"partial", "delta"} {
		out := testOutcome("reviewed")
		out.ReviewCoverage = coverage(scope)
		if validateOutcome(out, "review") == nil {
			t.Fatalf("%s review accepted without gaps", scope)
		}
	}
	_, j := testRun(t)
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: testBase, HeadSHA: testHead}
	j.Outcome = testOutcome("reviewed")
	j.Outcome.ReviewCoverage = coverage("full")
	j.Outcome.ReviewCoverage.HeadSHA = testBase
	if recordReviewReport(j) == nil || len(j.ReviewReports) != 0 {
		t.Fatal("wrong review boundary retained")
	}
}

func TestRecommendationsReachHumanWithoutRepair(t *testing.T) {
	dir, j := testRun(t)
	review := testOutcome("reviewed")
	review.Recommendations = []Finding{{Priority: "P2", Path: "app.go", Line: 1, Scenario: "The advertised workflow has no supported source of data.", Recommendation: "Reconsider the scope with the owner."}}
	review.ReviewCoverage = coverage("full")
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), review}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(p.calls) != 2 || saved.ReviewAssessment != nil || len(saved.ReviewReports) != 1 || !strings.Contains(output.String(), "Reconsider the scope") {
		t.Fatalf("recommendation repaired or lost: %s", output.String())
	}
}

func TestDeltaReviewContinuesAndRetainsEarlierEvidence(t *testing.T) {
	dir, j := testRun(t)
	first := testOutcome("reviewed")
	first.ReviewCoverage = coverage("delta", "Only the repair was inspected")
	first.Recommendations = []Finding{{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Two copies of a rule may drift.", Recommendation: "Consider sharing the rule."}}
	final := testOutcome("reviewed")
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), first, final}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(saved.ReviewReports) != 2 || len(p.calls) != 3 || p.calls[2].ResumeID != testNative || p.calls[2].Directory != p.calls[1].Directory {
		t.Fatal("delta review completed without same-session inspection")
	}
	if saved.ReviewReports[0].Coverage.Gaps[0] != first.ReviewCoverage.Gaps[0] || len(saved.ReviewReports[0].Recommendations) != 1 || saved.ReviewReports[0].Recommendations[0] != first.Recommendations[0] {
		t.Fatal("earlier evidence lost")
	}
	if strings.Contains(output.String(), first.Recommendations[0].Recommendation) {
		t.Fatal("superseded recommendation still shown", output.String())
	}

	if !strings.Contains(p.calls[2].Prompt, "remaining_inspection") || !strings.Contains(p.calls[2].Prompt, "Only the repair") {
		t.Fatal("focused continuation omitted named work")
	}
}

func TestFullReviewSupersedesEarlierCoverageWithoutDeletingHistory(t *testing.T) {
	j := Journal{Outcome: testOutcome("reviewed"), ReviewReports: []ReviewReport{
		{Coverage: coverage("partial", "Earlier gap")},
		{Coverage: coverage("full")},
	}}
	if warning := ReviewWarningNotice(j); warning != "" {
		t.Fatal(warning)
	}
	if len(j.ReviewReports) != 2 {
		t.Fatal("history deleted")
	}
	j.ReviewReports = nil
	j.Outcome.ReviewCoverage = nil
	if !strings.Contains(ReviewWarningNotice(j), "not recorded") {
		t.Fatal("legacy review appears complete")
	}
}

func TestReviewRoleSchemaAcceptsCoverageAndRecommendations(t *testing.T) {
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(reviewOutcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"recommendations", "review_coverage"} {
		if len(schema.Properties[key]) == 0 {
			t.Fatalf("native review schema excludes %s", key)
		}
		found := false
		for _, field := range schema.Required {
			if field == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("native review schema does not require %s", key)
		}
	}
}

func TestInterruptedReviewRetainsRecommendationsAndCoverage(t *testing.T) {
	_, j := testRun(t)
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: testBase, HeadSHA: testHead}
	j.Attempt = 1
	for _, kind := range []string{"recommendation", "gap"} {
		out := testOutcome("reviewed")
		out.ReviewCoverage = coverage("full")
		if kind == "recommendation" {
			out.Recommendations = []Finding{{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Unnecessary retained state increases operating cost.", Recommendation: "Reconsider the retained state."}}
		} else {
			out.ReviewCoverage = coverage("partial", "Uninspected state transitions")
		}
		raw, _ := json.Marshal(out)
		interruption, err := unresolvedReviewHandoff(raw)
		if err != nil || interruption == nil {
			t.Fatal("interrupted evidence lost", kind, err)
		}
		interruption.Publication, interruption.Attempt = j.Publication, 1
		interruption.Provider, interruption.Model = j.Plan.Roles.Review.Provider, j.Plan.Roles.Review.Name
		j.ReviewInterruption = interruption
		if err := validateReviewInterruption(j); err != nil {
			t.Fatal(err)
		}
		interruption.Coverage.HeadSHA = testBase
		if validateReviewInterruption(j) == nil {
			t.Fatal("wrong interrupted review boundary accepted")
		}
	}
}

func TestRecommendationNoticeUsesSupersedingFullReview(t *testing.T) {
	old := Finding{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Unused capability.", Recommendation: "Defer until needed."}
	current := Finding{Priority: "P3", Path: "other.go", Line: 2, Scenario: "Duplicated policy could drift.", Recommendation: "Share the policy."}
	for _, tc := range []struct {
		name, scope          string
		recommendations      []Finding
		wantOld, wantCurrent bool
	}{
		{name: "resolved by full review", scope: "full"},
		{name: "remaining recommendation", scope: "full", recommendations: []Finding{old}, wantOld: true},
		{name: "replacement recommendation", scope: "full", recommendations: []Finding{current}, wantCurrent: true},
		{name: "partial cannot clear", scope: "partial", wantOld: true},
		{name: "delta cannot clear", scope: "delta", recommendations: []Finding{current}, wantOld: true, wantCurrent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := Journal{Outcome: testOutcome("reviewed"), ReviewReports: []ReviewReport{
				{Publication: Publication{HeadSHA: testHead}, Coverage: coverage("full"), Recommendations: []Finding{old}},
				{Publication: Publication{HeadSHA: testSigned}, Coverage: coverage(tc.scope), Recommendations: tc.recommendations},
			}}
			notice := ReviewRecommendationNotice(j)
			if strings.Contains(notice, old.Recommendation) != tc.wantOld || strings.Contains(notice, current.Recommendation) != tc.wantCurrent {
				t.Fatalf("unexpected notice: %s", notice)
			}
			if !tc.wantOld && !tc.wantCurrent && notice != "" {
				t.Fatalf("resolved recommendations still advertised: %s", notice)
			}
			if len(j.ReviewReports) != 2 || j.ReviewReports[0].Recommendations[0] != old {
				t.Fatal("reporting changed retained history")
			}
		})
	}
}

func TestRecommendationNoticePreservesLegacyOutcome(t *testing.T) {
	item := Finding{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Unused capability.", Recommendation: "Defer until needed."}
	j := Journal{Outcome: testOutcome("reviewed")}
	j.Outcome.Recommendations = []Finding{item}
	if !strings.Contains(ReviewRecommendationNotice(j), item.Recommendation) {
		t.Fatal("legacy recommendation omitted")
	}
}

func TestStandaloneReviewDisplaysCoverageAndRecommendations(t *testing.T) {
	dir, j, r, pub, provider, checks := checkpointFixture(t, "review")
	out := testOutcome("reviewed")
	out.ReviewCoverage = coverage("partial", "Uninspected dependency behaviour")
	out.Recommendations = []Finding{{Priority: "P3", Path: "api.go", Line: 1, Scenario: "Duplicated policy could drift.", Recommendation: "Consider one policy boundary."}}
	completed := testOutcome("reviewed")
	completed.Recommendations = out.Recommendations
	provider.outcomes = []Outcome{out, completed}
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || checks.calls != 0 || pub.calls != 0 || len(provider.sessions) != 2 {
		t.Fatal("standalone review started repairs")
	}
	if !strings.Contains(output.String(), "one policy boundary") {
		t.Fatal("standalone review hid evidence", output.String())
	}
}

func TestImplementationSelfReviewUsesRoleRestrictedSchema(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	var implementation, review map[string]any
	if err := json.Unmarshal([]byte(p.calls[0].Schema), &implementation); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(p.calls[1].Schema), &review); err != nil {
		t.Fatal(err)
	}
	props := implementation["properties"].(map[string]any)
	if props["recommendations"].(map[string]any)["maxItems"] != float64(0) || props["review_coverage"].(map[string]any)["type"] != "null" {
		t.Fatal("implementation can emit independent-review details")
	}
	for _, status := range props["status"].(map[string]any)["enum"].([]any) {
		if status == "reviewed" || status == "review_incomplete" {
			t.Fatal("implementation can claim independent review")
		}
	}
	reviewProps := review["properties"].(map[string]any)
	if reviewProps["recommendations"].(map[string]any)["maxItems"] != float64(32) || reviewProps["review_coverage"].(map[string]any)["anyOf"] == nil {
		t.Fatal("independent-review reporting was restricted")
	}
}

func TestNativeImplementationSelfReviewRoleBoundary(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, invalidCoverage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/coverage=%t", provider, invalidCoverage), func(t *testing.T) {
				out := testOutcome("checks_requested")
				out.Summary = "Self-review trace and non-blocking scope recommendation retained here."
				out.LocalReview = true
				if invalidCoverage {
					out.ReviewCoverage = coverage("full")
				}
				data := codexEvents(out)
				if provider == "claude" {
					raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": out})
					data = string(raw)
				}
				w := eventWriter{provider: provider}
				if _, err := w.Write([]byte(data)); err != nil {
					t.Fatal(err)
				}
				err := w.finish("implementation")
				if invalidCoverage && err == nil {
					t.Fatal("implementation review coverage accepted")
				}
				if !invalidCoverage && err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
