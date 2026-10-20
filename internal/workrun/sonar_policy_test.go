package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tjpeel/sdlc/internal/project"
	"io"
	"net/url"
	"strings"
	"testing"
)

func newSonarPolicyFixture(t *testing.T, count int) (GitHubPublisher, Plan, Publication, *int) {
	t.Helper()
	fixture, details := sonarDetailsFixture()
	pages := 0
	api := func(ctx context.Context, endpoint string, target any) error {
		if !strings.HasPrefix(endpoint, "issues/search?") {
			return details(ctx, endpoint, target)
		}
		pages++
		u, _ := url.Parse(endpoint)
		q := u.Query()
		if q.Get("pullRequest") != "7" || q.Get("resolved") != "false" || q.Get("componentKeys") != "example_project" {
			return fmt.Errorf("wrong issue scope")
		}
		start := 0
		if q.Get("p") == "2" {
			start = 100
		}
		issues := []any{}
		for n := start; n < count && n < start+100; n++ {
			issues = append(issues, map[string]any{"key": fmt.Sprintf("issue-%03d", n), "project": "example_project", "component": "example_project:src/Example.cs", "rule": "csharpsquid:S1111", "message": "Remove unnecessary repeated work.", "status": "OPEN", "line": n + 1})
		}
		data, _ := json.Marshal(map[string]any{"paging": map[string]any{"total": count}, "issues": issues})
		return json.Unmarshal(data, target)
	}
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
			return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"pass","state":"COMPLETED"}]`), nil
		}
		return fixture.command(ctx, name, args...)
	}
	pr := annotationPublication()
	pr.Number = 7
	return GitHubPublisher{Command: command, SonarJSON: api}, Plan{Repository: "example/project", Sonar: project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}}, pr, &pages
}

func TestPassingGateCollectsAllFiveSonarIssuesAndDuplication(t *testing.T) {
	p, plan, pr, pages := newSonarPolicyFixture(t, 5)
	result, err := p.Checks(context.Background(), plan, pr)
	if err != nil || result.Status != "passed" || result.SonarFindings == nil || len(result.SonarFindings.Issues) != 5 || len(result.SonarFindings.Groups) != 1 || *pages != 1 {
		t.Fatalf("green gate lost findings: %+v %v", result, err)
	}
	if err := validateCIResult(result, pr); err != nil {
		t.Fatal(err)
	}
}
func TestSonarIssuePaginationDoesNotDropFindings(t *testing.T) {
	p, plan, pr, pages := newSonarPolicyFixture(t, 105)
	result, err := p.Checks(context.Background(), plan, pr)
	if err != nil || len(result.SonarFindings.Issues) != 105 || *pages != 2 {
		t.Fatal("issue pagination incomplete", err, *pages)
	}
}
func TestAdvisoryPolicyDoesNotCollectOrGrantSonarIssueRepairs(t *testing.T) {
	p, plan, pr, pages := newSonarPolicyFixture(t, 5)
	plan.Sonar = project.SonarPolicy{}
	result, err := p.Checks(context.Background(), plan, pr)
	if err != nil || result.Status != "passed" || result.SonarFindings != nil || *pages != 0 {
		t.Fatal("legacy advisory policy changed", result, err)
	}
}
func TestSonarPolicyRejectsStalePartialAndOutOfTreeEvidence(t *testing.T) {
	for _, scenario := range []string{"stale", "partial", "outside", "unavailable", "unknown-state"} {
		t.Run(scenario, func(t *testing.T) {
			p, plan, pr, _ := newSonarPolicyFixture(t, 5)
			base := p.SonarJSON
			p.SonarJSON = func(ctx context.Context, endpoint string, target any) error {
				if scenario == "stale" && strings.HasPrefix(endpoint, "project_pull_requests/list?") {
					return json.Unmarshal([]byte(`{"pullRequests":[{"key":"7","commit":{"sha":"`+testBase+`"}}]}`), target)
				}
				if strings.HasPrefix(endpoint, "issues/search?") {
					if scenario == "unavailable" {
						return errors.New("offline")
					}
					if scenario == "partial" {
						return json.Unmarshal([]byte(`{"paging":{"total":5},"issues":[]}`), target)
					}
					if scenario == "outside" || scenario == "unknown-state" {
						path := "example_project:missing.cs"
						status := "OPEN"
						if scenario == "unknown-state" {
							path = "example_project:src/Example.cs"
							status = "unexpected"
						}
						data, _ := json.Marshal(map[string]any{"paging": map[string]any{"total": 1}, "issues": []any{map[string]any{"key": "one", "project": "example_project", "component": path, "rule": "rule", "message": "fix", "status": status}}})
						return json.Unmarshal(data, target)
					}
				}
				return base(ctx, endpoint, target)
			}
			result, err := p.Checks(context.Background(), plan, pr)
			if scenario == "stale" {
				if err != nil || result.Status != "pending" || result.SonarFindings != nil {
					t.Fatal("stale analysis did not wait without granting clearance", result, err)
				}
			} else if err == nil {
				t.Fatal("unverified evidence certified policy", scenario)
			}
		})
	}
}

func sonarRepairFixture(t *testing.T) (string, *Journal, Runner, *fakeProvider) {
	t.Helper()
	dir, j, provider, _, r := completedReviewFixture(t)
	j.State = "ci"
	j.ResumeState = ""
	j.StopReason = ""
	j.Plan.Sonar = project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}
	j.CI = CIResult{Status: "passed", SonarFindings: &SonarFindings{Head: j.Publication.HeadSHA, Projects: []string{"example_project"}, Issues: []SonarIssue{{Key: "one", Project: "example_project", Rule: "rule", Message: "Fix repeated work.", Path: "src/Example.cs", Line: 4}}}}
	return dir, j, r, provider
}
func TestSonarPolicyGrantsRepairBeforeReviewDespiteGreenCI(t *testing.T) {
	dir, j, r, provider := sonarRepairFixture(t)
	handled, err := r.repairSonar(context.Background(), dir, j)
	if err != nil || !handled || j.State != "repairing" || len(j.SonarRepairs) != 1 || len(provider.calls) != 0 || j.CI.Status != "passed" {
		t.Fatal("policy not routed separately from green CI", handled, err)
	}
	prompt := r.prompt(*j, "implementation")
	if !strings.Contains(prompt, "Fix repeated work.") || !strings.Contains(prompt, "do not suppress findings") {
		t.Fatal("agent lacks precise authorised repair evidence")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	handled, err = r.repairSonar(context.Background(), dir, &saved)
	if !handled || !errors.Is(err, ErrStopped) || len(saved.SonarRepairs) != 1 || len(provider.calls) != 0 {
		t.Fatal("same findings started duplicate repair")
	}
}
func TestSonarRepairRejectsNoChangeWithoutRepeatingChecksOrReviews(t *testing.T) {
	dir, j, r, provider := sonarRepairFixture(t)
	_, err := r.repairSonar(context.Background(), dir, j)
	if err != nil {
		t.Fatal(err)
	}
	j.Outcome = testOutcome("checks_requested")
	if err := r.acceptImplementationOutcome(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatal("unresolved no-change repair accepted", err)
	}
	if j.State != "blocked" || len(provider.calls) != 0 {
		t.Fatal("no-progress repair launched more agents")
	}
}
func TestCleanSonarAnalysisDoesNotLaunchAnyAgent(t *testing.T) {
	dir, j, r, provider := sonarRepairFixture(t)
	j.CI.SonarFindings.Issues = nil
	handled, err := r.repairSonar(context.Background(), dir, j)
	if handled || err != nil || len(provider.calls) != 0 || len(j.SonarRepairs) != 0 {
		t.Fatal("clean analysis launched repair", handled, err)
	}
}
func TestSonarRepairsPreemptEquivalentRebaseMonitoring(t *testing.T) {
	dir, j, r, _ := sonarRepairFixture(t)
	j.RebaseMonitoring = &RebaseMonitoring{Source: j.Publication, Published: j.Publication, OldBase: testBase, Base: j.Plan.BaseSHA, Proof: RebaseProof{SourceHead: testHead, Head: testHead, Tree: testTree, Equivalent: true}}
	handled, err := r.repairSonar(context.Background(), dir, j)
	if err != nil || !handled || j.RebaseMonitoring != nil || j.State != "repairing" {
		t.Fatal("rebase bypass ignored Sonar policy", handled, err)
	}
}

type sonarCorrectionProvider struct {
	fakeProvider
	repo *fakeRepository
}

func (p *sonarCorrectionProvider) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	if s.Role == "implementation" && len(p.calls) == 0 {
		p.repo.revisions = []Revision{{Head: strings.Repeat("4", 40), Tree: strings.Repeat("5", 40), Clean: true}}
	}
	return p.fakeProvider.Execute(ctx, s, out, diag)
}
func TestSonarPolicyRepairsPublishesAndRechecksBeforeReady(t *testing.T) {
	dir, j, _, pub, r := completedReviewFixture(t)
	j.State = "ci"
	j.ResumeState = ""
	j.StopReason = ""
	j.Plan.Sonar = project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}
	findings := &SonarFindings{Head: testHead, Projects: []string{"example_project"}, Issues: []SonarIssue{{Key: "one", Project: "example_project", Rule: "rule", Message: "Repair the repeated work.", Path: "src/Example.cs", Line: 4}}}
	clean := &SonarFindings{Head: strings.Repeat("4", 40), Projects: []string{"example_project"}, Issues: []SonarIssue{}}
	pub.ciResults = []CIResult{{Status: "passed", SonarFindings: findings}, {Status: "passed", SonarFindings: clean}}
	pub.heads = []string{strings.Repeat("4", 40)}
	repo := &fakeRepository{}
	provider := &sonarCorrectionProvider{repo: repo, fakeProvider: fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}}
	checker := &fakeChecker{}
	r.Repository = repo
	r.Provider = provider
	r.Checker = checker
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || pub.calls != 1 || checker.calls != 1 || len(provider.calls) != 3 || len(j.SonarRepairs) != 1 || j.CI.SonarFindings == nil || len(j.CI.SonarFindings.Issues) != 0 {
		t.Fatalf("repair loop failed: state %s publications %d checks %d turns %d", j.State, pub.calls, checker.calls, len(provider.calls))
	}
	if provider.calls[0].Role != "implementation" || provider.calls[2].Role != "review" {
		t.Fatal("Sonar repaired after or instead of normal review")
	}
}
func TestSonarPolicyBatchesPendingHumanFeedback(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	j.Plan.Sonar = project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}
	pub.comments = []PRFeedbackComment{generalFeedback("comment:both", "Keep the documented behaviour.")}
	pub.ciResults = []CIResult{{Status: "passed", SonarFindings: &SonarFindings{Head: j.Publication.HeadSHA, Projects: []string{"example_project"}, Issues: []SonarIssue{{Key: "one", Project: "example_project", Rule: "rule", Message: "Repair the repeated work.", Path: "src/Example.cs", Line: 4}}}}}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if j.State != "repairing" || len(j.SonarRepairs) != 1 || len(pendingFeedbackEntries(j)) != 1 || checks.calls != 1 {
		t.Fatal("Sonar scheduling lost human feedback or repeated checks")
	}
	if err := r.feedbackPrompt(dir, j); err != nil {
		t.Fatal(err)
	}
	prompt := r.prompt(*j, "implementation")
	if !strings.Contains(prompt, "Keep the documented behaviour.") || !strings.Contains(prompt, "Repair the repeated work.") {
		t.Fatal("worker did not receive combined repair scope")
	}
}

func TestSonarFeedbackReadFailureResumesOriginalPendingComments(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	j.Plan.Sonar = project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}
	comment := generalFeedback("comment:sonar-retry", "Confirm existing behaviour.")
	pub.comments = []PRFeedbackComment{comment}
	pub.ciErr = errors.New("temporary Sonar collection failure")
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if !j.PRFeedback.FetchPending || len(provider.sessions) != 0 {
		t.Fatal("failed Sonar read lost recoverable fetch boundary")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub.ciErr = nil
	pub.ciResults = []CIResult{{Status: "passed", SonarFindings: &SonarFindings{Head: saved.Publication.HeadSHA, Projects: []string{"example_project"}, Issues: []SonarIssue{}}}}
	r.ProcessPRFeedback = false
	if err := r.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(provider.sessions) != 1 || checks.calls != 1 || pub.replies != 1 {
		t.Fatal("retry lost comments or repeated review")
	}
}
func TestSonarRepairIncludesFindingsInOriginalSession(t *testing.T) {
	dir, j, r, _ := sonarRepairFixture(t)
	j.SessionID = testNative
	if _, err := r.repairSonar(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	prompt := r.prompt(*j, "implementation")
	if !strings.Contains(prompt, "Continue the original") || !strings.Contains(prompt, "Fix repeated work.") || !strings.Contains(prompt, "src/Example.cs") {
		t.Fatal("original session lost Sonar repair evidence")
	}
}
func TestSonarPolicyPreemptsDirectReviewerResume(t *testing.T) {
	dir, j, provider, pub, r := completedReviewFixture(t)
	j.State = "awaiting_reviewer"
	j.ResumeState = ""
	j.StopReason = ""
	j.Outcome = testOutcome("implemented")
	j.ReviewReports = nil
	j.Plan.Sonar = project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}
	pub.ciResults = []CIResult{{Status: "passed", SonarFindings: &SonarFindings{Head: testHead, Projects: []string{"example_project"}, Issues: []SonarIssue{{Key: "one", Project: "example_project", Rule: "rule", Message: "Fix repeated work.", Path: "src/Example.cs", Line: 4}}}}}
	provider.executeErr = errors.New("intentional offline provider stop")
	_ = r.Run(context.Background(), dir, j, "")
	if len(provider.calls) != 1 || provider.calls[0].Role != "implementation" || len(j.SonarRepairs) != 1 {
		t.Fatal("review ran before policy repair", provider.calls)
	}
}
func TestSonarZeroDensityUsesAuthoritativeFileCounts(t *testing.T) {
	for _, duplicates := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicates), func(t *testing.T) {
			fixture, details := sonarDetailsFixture()
			fixture.responses["repos/example/project/check-runs/17"].(map[string]any)["output"] = map[string]any{"summary": "[0.0% Duplication on New Code](https://sonarcloud.io/component_measures?id=example_project&pullRequest=7&metric=new_duplicated_lines_density&view=list)"}
			api := func(ctx context.Context, endpoint string, target any) error {
				if strings.HasPrefix(endpoint, "issues/search?") {
					return json.Unmarshal([]byte(`{"paging":{"total":0},"issues":[]}`), target)
				}
				if !duplicates && strings.HasPrefix(endpoint, "measures/component_tree?") {
					return json.Unmarshal([]byte(`{"paging":{"total":0},"components":[]}`), target)
				}
				return details(ctx, endpoint, target)
			}
			command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
					return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"pass","state":"COMPLETED"}]`), nil
				}
				return fixture.command(ctx, name, args...)
			}
			pr := annotationPublication()
			pr.Number = 7
			result, err := (GitHubPublisher{Command: command, SonarJSON: api}).Checks(context.Background(), Plan{Repository: "example/project", Sonar: project.SonarPolicy{RepairPolicy: project.SonarRepairAllNew}}, pr)
			if err != nil || result.Status != "passed" || result.SonarFindings == nil {
				t.Fatal("zero density not authoritatively checked", err)
			}
			expected := 0
			if duplicates {
				expected = 1
			}
			if len(result.SonarFindings.Groups) != expected {
				t.Fatal("rounded density hid duplicate ranges", result.SonarFindings)
			}
			if err := validateCIResult(result, pr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSonarStrictZeroDensityRejectsIncompleteFileCounts(t *testing.T) {
	for _, body := range []string{`{}`, `{"paging":{},"components":[]}`, `{"paging":{"total":2},"components":[]}`} {
		t.Run(body, func(t *testing.T) {
			p, plan, pr, _ := newSonarPolicyFixture(t, 0)
			base := p.SonarJSON
			p.SonarJSON = func(ctx context.Context, endpoint string, target any) error {
				if strings.HasPrefix(endpoint, "measures/component_tree?") {
					return json.Unmarshal([]byte(body), target)
				}
				return base(ctx, endpoint, target)
			}
			if _, err := p.Checks(context.Background(), plan, pr); err == nil {
				t.Fatal("incomplete file listing certified no duplication")
			}
		})
	}
}

func TestSonarPolicyDoesNotCertifyAbsentAnalysis(t *testing.T) {
	dir, j, r, _ := sonarRepairFixture(t)
	j.CI.SonarFindings = nil
	if _, err := r.repairSonar(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatal("absent analysis certified clean", err)
	}
}
func TestSonarUnchangedFindingsAfterPublicationStopWithoutAnotherAgent(t *testing.T) {
	dir, j, r, provider := sonarRepairFixture(t)
	if _, err := r.repairSonar(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.Publication.HeadSHA = strings.Repeat("4", 40)
	j.CI.SonarFindings.Head = j.Publication.HeadSHA
	if _, err := r.repairSonar(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatal("identical findings started another repair", err)
	}
	if len(j.SonarRepairs) != 1 || len(provider.calls) != 0 {
		t.Fatal("no-progress guard spent another repair turn")
	}
}

func TestSonarPolicyCollectsFindingsWithoutSummaryDuplicationRow(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			p, plan, pr, pages := newSonarPolicyFixture(t, 1)
			command, api := p.Command, p.SonarJSON
			p.Command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				data, err := command(ctx, name, args...)
				if failed && len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
					return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"fail","state":"COMPLETED","link":"https://private.invalid"}]`), ciStatusExit(1)
				}
				if failed && err == nil {
					data = []byte(strings.ReplaceAll(string(data), `"success"`, `"failure"`))
				}

				if len(args) > 0 && strings.Contains(args[len(args)-1], "/check-runs?per_page=") && err == nil {
					var listing map[string]any
					if err := json.Unmarshal(data, &listing); err != nil {
						return nil, err
					}
					for _, entry := range listing["check_runs"].([]any) {
						run := entry.(map[string]any)
						run["details_url"] = "https://sonarcloud.io/dashboard?id=example_project&pullRequest=7"
						run["output"] = map[string]any{"summary": "Failed conditions: one new issue", "annotations_count": 0}
					}
					return json.Marshal(listing)
				}
				if len(args) > 0 && args[len(args)-1] == "repos/example/project/check-runs/17" && err == nil {
					var run map[string]any
					if err := json.Unmarshal(data, &run); err != nil {
						return nil, err
					}
					run["details_url"] = "https://sonarcloud.io/dashboard?id=example_project&pullRequest=7"
					run["output"] = map[string]any{"summary": "Failed conditions: one new issue", "annotations_count": 0}
					return json.Marshal(run)
				}
				return data, err
			}
			p.SonarJSON = func(ctx context.Context, endpoint string, target any) error {
				if strings.HasPrefix(endpoint, "measures/component?") {
					return json.Unmarshal([]byte(`{"component":{"key":"example_project","pullRequest":"7","measures":[{"metric":"new_duplicated_lines_density","periods":[{"value":"0.0"}]}]}}`), target)
				}
				return api(ctx, endpoint, target)
			}
			result, err := p.Checks(context.Background(), plan, pr)
			if err != nil || result.SonarFindings == nil || len(result.SonarFindings.Issues) != 1 || *pages != 1 || len(result.SonarDuplication) != 1 {
				t.Fatalf("missing summary metric lost findings: %+v %v", result, err)
			}
			if err := validateCIResult(result, pr); err != nil {
				t.Fatal(err)
			}
			expected := "passed"
			if failed {
				expected = "failed"
			}
			if result.Status != expected {
				t.Fatalf("quality-gate status changed: %s", result.Status)
			}
			currentAPI := p.SonarJSON
			p.SonarJSON = func(ctx context.Context, endpoint string, target any) error {
				if strings.HasPrefix(endpoint, "project_pull_requests/list?") {
					return json.Unmarshal([]byte(`{"pullRequests":[]}`), target)
				}
				return currentAPI(ctx, endpoint, target)
			}
			waiting, err := p.Checks(context.Background(), plan, pr)
			if err != nil || waiting.Status != "pending" || waiting.SonarFindings != nil {
				t.Fatalf("stale analysis did not wait: %+v %v", waiting, err)
			}

		})
	}
}

func TestSonarMetricDiscoveryRejectsUnscopedAndIncompleteEvidence(t *testing.T) {
	for _, scenario := range []string{"branch", "foreign-host", "wrong-pr", "duplicate-project", "missing-metric", "wrong-project", "wrong-response-pr", "invalid-value", "stale-head"} {
		t.Run(scenario, func(t *testing.T) {
			p, _, pr, _ := newSonarPolicyFixture(t, 0)
			run := ciCheckRun{ID: 17, DetailsURL: "https://sonarcloud.io/dashboard?id=example_project&pullRequest=7"}
			switch scenario {
			case "branch":
				run.DetailsURL = "https://sonarcloud.io/dashboard?id=example_project&branch=main"
			case "foreign-host":
				run.DetailsURL = "https://example.invalid/dashboard?id=example_project&pullRequest=7"
			case "wrong-pr":
				run.DetailsURL = "https://sonarcloud.io/dashboard?id=example_project&pullRequest=8"
			case "duplicate-project":
				run.DetailsURL += "&id=other"
			}
			api := p.SonarJSON
			p.SonarJSON = func(ctx context.Context, endpoint string, target any) error {
				if strings.HasPrefix(endpoint, "measures/component?") {
					data := `{"component":{"key":"example_project","pullRequest":"7","measures":[{"metric":"new_duplicated_lines_density","periods":[{"value":"0.0"}]}]}}`
					switch scenario {
					case "missing-metric":
						data = `{"component":{"key":"example_project","pullRequest":"7","measures":[]}}`
					case "wrong-project":
						data = strings.ReplaceAll(data, "example_project", "other")
					case "wrong-response-pr":
						data = strings.ReplaceAll(data, `"7"`, `"8"`)
					case "invalid-value":
						data = strings.ReplaceAll(data, "0.0", "NaN")
					}
					return json.Unmarshal([]byte(data), target)
				}
				if scenario == "stale-head" && strings.HasPrefix(endpoint, "project_pull_requests/list?") {
					return json.Unmarshal([]byte(`{"pullRequests":[]}`), target)
				}
				return api(ctx, endpoint, target)
			}
			if _, err := p.sonarMeasureFromCheck(context.Background(), pr, run); err == nil {
				t.Fatal("incomplete or unscoped evidence accepted")
			}
		})
	}
}
