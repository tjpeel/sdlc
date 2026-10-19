package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sonarMeasuresFixture() *ciAnnotationFixture {
	f := newCIAnnotationFixture()
	run := f.responses["repos/example/project/check-runs/17"].(map[string]any)
	run["conclusion"] = "success"
	run["output"] = map[string]any{"summary": "[1.0% Duplication on New Code](https://sonarcloud.io/component_measures?id=example_project&pullRequest=7&metric=new_duplicated_lines_density&view=list)", "annotations_count": 0}
	return f
}

func TestPassedSonarDuplicationReachesReviewWithoutFailingCI(t *testing.T) {
	f := sonarMeasuresFixture()
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
			return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"pass","state":"COMPLETED","link":"https://sonarcloud.io"}]`), nil
		}
		return f.command(ctx, name, args...)
	}
	pr := annotationPublication()
	pr.Number = 7
	result, err := (GitHubPublisher{Command: command, SonarJSON: func(context.Context, string, any) error { return fmt.Errorf("offline") }}).Checks(context.Background(), Plan{Repository: "example/project"}, pr)
	if err != nil || result.Status != "passed" || len(result.SonarDuplication) != 1 || result.SonarDuplication[0].Percent != 1 || len(result.Diagnostics) != 0 {
		t.Fatal(result, err)
	}
	if err := validateCIResult(result, pr); err != nil {
		t.Fatal(err)
	}
	j := Journal{Publication: pr, CI: result}
	prompt := (Runner{}).prompt(j, "review")
	if !strings.Contains(prompt, `"sonar_duplication"`) || !strings.Contains(prompt, "nonzero percentage alone is not a defect") {
		t.Fatal("missing advisory review evidence")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "Duplication on New Code") {
		t.Fatal("raw summary escaped projection")
	}
}

func TestSonarDuplicationAuthorityAndRevision(t *testing.T) {
	for _, change := range []string{"foreign-app", "stale-head", "wrong-pr", "ambiguous", "over-range"} {
		t.Run(change, func(t *testing.T) {
			f := sonarMeasuresFixture()
			run := f.responses["repos/example/project/check-runs/17"].(map[string]any)
			switch change {
			case "foreign-app":
				run["app"] = map[string]any{"id": 15368, "slug": "github-actions"}
			case "stale-head":
				run["head_sha"] = testBase
			default:
				output := run["output"].(map[string]any)
				summary := output["summary"].(string)
				if change == "wrong-pr" {
					summary = strings.ReplaceAll(summary, "pullRequest=7", "pullRequest=8")
				}
				if change == "ambiguous" {
					summary += summary
				}
				if change == "over-range" {
					summary = strings.ReplaceAll(summary, "1.0%", "101%")
				}
				output["summary"] = summary
			}
			pr := annotationPublication()
			pr.Number = 7
			if _, err := (GitHubPublisher{Command: f.command}).collectSonarDuplication(context.Background(), Plan{Repository: "example/project"}, pr); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
}

func TestSonarSummaryWithoutMeasureIsNotZeroDuplication(t *testing.T) {
	f := sonarMeasuresFixture()
	f.responses["repos/example/project/check-runs/17"].(map[string]any)["output"] = map[string]any{"summary": "Not available"}
	result, err := (GitHubPublisher{Command: f.command}).collectSonarDuplication(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
}

func sonarDetailsFixture() (*ciAnnotationFixture, func(context.Context, string, any) error) {
	f := sonarMeasuresFixture()
	tree := f.responses["repos/example/project/git/trees/"+testTree+"?recursive=1"].(map[string]any)
	tree["tree"] = append(tree["tree"].([]any), map[string]any{"path": "src/Other.cs", "mode": "100644", "type": "blob"})
	api := func(ctx context.Context, endpoint string, target any) error {
		var value any
		switch {
		case strings.HasPrefix(endpoint, "project_pull_requests/list?"):
			value = map[string]any{"pullRequests": []any{map[string]any{"key": "7", "commit": map[string]any{"sha": testHead}}}}
		case strings.HasPrefix(endpoint, "measures/component_tree?"):
			value = map[string]any{"paging": map[string]any{"total": 1}, "components": []any{map[string]any{"key": "example_project:src/Example.cs", "path": "src/Example.cs", "pullRequest": "7", "measures": []any{map[string]any{"metric": "new_duplicated_lines", "periods": []any{map[string]any{"value": "9"}}}}}}}
		case strings.HasPrefix(endpoint, "duplications/show?"):
			value = map[string]any{"duplications": []any{map[string]any{"blocks": []any{map[string]any{"from": 26, "size": 25, "_ref": "1"}, map[string]any{"from": 29, "size": 25, "_ref": "2"}}}}, "files": map[string]any{"1": map[string]any{"key": "example_project:src/Example.cs", "name": "src/Example.cs", "project": "example_project", "pullRequest": "7"}, "2": map[string]any{"key": "example_project:src/Other.cs", "name": "src/Other.cs", "project": "example_project", "pullRequest": "7"}}}
		default:
			return fmt.Errorf("unexpected endpoint")
		}
		data, _ := json.Marshal(value)
		return json.Unmarshal(data, target)
	}
	return f, api
}

func TestSonarDuplicateRangesReachAgent(t *testing.T) {
	f, api := sonarDetailsFixture()
	pr := annotationPublication()
	pr.Number = 7
	result, err := (GitHubPublisher{Command: f.command, SonarJSON: api}).collectSonarDuplication(context.Background(), Plan{Repository: "example/project"}, pr)
	if err != nil || len(result) != 1 || result[0].DetailsStatus != "collected" || len(result[0].Groups) != 1 {
		t.Fatal(result, err)
	}
	ranges := result[0].Groups[0].Ranges
	if len(ranges) != 2 || ranges[0].Path != "src/Example.cs" || ranges[0].Line != 26 || ranges[0].EndLine != 50 || ranges[1].Path != "src/Other.cs" || ranges[1].EndLine != 53 {
		t.Fatal(ranges)
	}
	j := Journal{Publication: pr, CI: CIResult{Status: "passed", SonarDuplication: result}}
	if err := validateCIResult(j.CI, pr); err != nil {
		t.Fatal(err)
	}
	prompt := (Runner{}).prompt(j, "review")
	for _, want := range []string{"src/Example.cs", "src/Other.cs", `"line": 26`, `"end_line": 53`, "individual groups may include existing duplication"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("agent missing %s", want)
		}
	}
}

func TestSonarDetailFailurePreservesMeasureWithoutInventedLocations(t *testing.T) {
	for _, kind := range []string{"analysis-drift", "unsafe-path", "api-unavailable", "source-tree-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			f, base := sonarDetailsFixture()
			calls := 0
			api := func(ctx context.Context, endpoint string, target any) error {
				if kind == "api-unavailable" {
					return fmt.Errorf("private raw failure")
				}
				if err := base(ctx, endpoint, target); err != nil {
					return err
				}
				data, _ := json.Marshal(target)
				if strings.HasPrefix(endpoint, "project_pull_requests/list?") {
					calls++
					if kind == "analysis-drift" && calls == 2 {
						data = []byte(strings.ReplaceAll(string(data), testHead, testBase))
					}
				}
				if kind == "unsafe-path" && strings.HasPrefix(endpoint, "duplications/show?") {
					data = []byte(strings.ReplaceAll(string(data), "src/Other.cs", ".secrets/private.cs"))
				}
				return json.Unmarshal(data, target)
			}
			if kind == "source-tree-unavailable" {
				f.fail = "repos/example/project/git/commits/" + testHead
			}
			pr := annotationPublication()
			pr.Number = 7
			result, err := (GitHubPublisher{Command: f.command, SonarJSON: api}).collectSonarDuplication(context.Background(), Plan{Repository: "example/project"}, pr)
			if err != nil || len(result) != 1 || result[0].DetailsStatus != "unavailable" || len(result[0].Groups) != 0 || result[0].Percent != 1 {
				t.Fatal(result, err)
			}
			if err := validateCIResult(CIResult{Status: "passed", SonarDuplication: result}, pr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBranchScopedSonarSummaryReportsOperationalEvidenceGap(t *testing.T) {
	for _, percent := range []string{"0.0%", "0.5%"} {
		t.Run(percent, func(t *testing.T) {
			f := sonarMeasuresFixture()
			run := f.responses["repos/example/project/check-runs/17"].(map[string]any)
			output := run["output"].(map[string]any)
			summary := strings.ReplaceAll(output["summary"].(string), "pullRequest=7", "branch=main")
			output["summary"] = strings.ReplaceAll(summary, "1.0%", percent)
			pr := annotationPublication()
			pr.Number = 7
			measures, err := (GitHubPublisher{Command: f.command}).collectSonarDuplication(context.Background(), Plan{Repository: "example/project"}, pr)
			if err == nil || !strings.Contains(err.Error(), "branch-scoped") || !strings.Contains(err.Error(), "PR #7") || len(measures) != 0 {
				t.Fatalf("branch analysis must remain an explicit evidence gap, got %v, %v", measures, err)
			}
		})
	}
}

func TestBranchScopeRecognitionPreservesSonarURLBoundary(t *testing.T) {
	for _, address := range []string{
		"https://example.invalid/component_measures?id=example_project&branch=main&metric=new_duplicated_lines_density&view=list",
		"https://sonarcloud.io/component_measures?id=example_project&branch=main&branch=other&metric=new_duplicated_lines_density&view=list",
		"https://sonarcloud.io/component_measures?id=example_project&branch=main&pullRequest=7&metric=new_duplicated_lines_density&view=list",
	} {
		if branchScopedSonarMeasuresURL(address, 7) {
			t.Fatalf("accepted ambiguous or untrusted scope URL: %s", address)
		}
	}
}

func TestBranchSonarMeasuresDoNotBlockPassedRequiredChecks(t *testing.T) {
	f := sonarMeasuresFixture()
	run := f.responses["repos/example/project/check-runs/17"].(map[string]any)
	output := run["output"].(map[string]any)
	output["summary"] = strings.ReplaceAll(output["summary"].(string), "pullRequest=7", "branch=main")
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "checks" {
			return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"pass","state":"COMPLETED","link":"https://sonarcloud.io"}]`), nil
		}
		return f.command(ctx, name, args...)
	}
	pr := annotationPublication()
	pr.Number = 7
	result, err := (GitHubPublisher{Command: command}).Checks(context.Background(), Plan{Repository: "example/project"}, pr)
	if err != nil || result.Status != "passed" || len(result.SonarDuplication) != 0 || !strings.Contains(result.Details, "PR-specific duplication evidence unavailable") {
		t.Fatalf("advisory scope mismatch blocked CI or masqueraded as PR evidence: %+v, %v", result, err)
	}
	if err := validateCIResult(result, pr); err != nil {
		t.Fatal(err)
	}
}
