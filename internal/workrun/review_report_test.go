package workrun

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDeltaReviewPreservesEarlierGapsAndRecommendations(t *testing.T) {
	dir, j := testRun(t)
	first := reviewedFinding("P2")
	first.ReviewCoverage = coverage("partial", "Assertions in the remaining tests were not inspected.")
	first.Limitations = []string{"A required behaviour could not be confirmed."}
	first.Recommendations = []Finding{{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Two copies of a rule may drift.", Recommendation: "Consider sharing the rule."}}
	last := testOutcome("reviewed")
	last.ReviewCoverage = coverage("delta", "Only the repair was inspected; earlier changes were not reviewed again.")
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), first, testOutcome("implemented"), last}}
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
	if len(saved.ReviewReports) != 2 {
		t.Fatal("review history lost")
	}
	for _, text := range []string{"remaining tests", "required behaviour", "Only the repair", "Consider sharing the rule"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("human warning lost %q: %s", text, output.String())
		}
	}
	if !strings.Contains(p.calls[3].Prompt, "previous_review_report") || !strings.Contains(p.calls[3].Prompt, "remaining tests") {
		t.Fatal("reviewer did not receive earlier coverage")
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

func TestLaterFullReviewDoesNotEraseEarlierRecommendations(t *testing.T) {
	item := Finding{Priority: "P3", Path: "app.go", Line: 1, Scenario: "An advertised workflow has no supported input.", Recommendation: "Reconsider its public surface."}
	j := Journal{Outcome: testOutcome("reviewed"), ReviewReports: []ReviewReport{
		{Publication: Publication{HeadSHA: testHead}, Coverage: coverage("full"), Recommendations: []Finding{item}},
		{Coverage: coverage("full")},
	}}
	if !strings.Contains(ReviewRecommendationNotice(j), item.Recommendation) {
		t.Fatal("full inspection silently resolved a scope recommendation")
	}
}

func TestStandaloneReviewDisplaysCoverageAndRecommendations(t *testing.T) {
	dir, j, r, pub, provider, checks := checkpointFixture(t, "review")
	out := testOutcome("reviewed")
	out.ReviewCoverage = coverage("partial", "Uninspected dependency behaviour")
	out.Recommendations = []Finding{{Priority: "P3", Path: "api.go", Line: 1, Scenario: "Duplicated policy could drift.", Recommendation: "Consider one policy boundary."}}
	provider.outcomes = []Outcome{out}
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || checks.calls != 0 || pub.calls != 0 || len(provider.sessions) != 1 {
		t.Fatal("standalone review started repairs")
	}
	if !strings.Contains(output.String(), "Uninspected dependency") || !strings.Contains(output.String(), "one policy boundary") {
		t.Fatal("standalone review hid evidence", output.String())
	}
}
