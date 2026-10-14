package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type ciAnnotationFixture struct {
	checkName       string
	calls           []string
	views           int
	changeFinalHead bool
	responses       map[string]any
	fail            string
}

func newCIAnnotationFixture() *ciAnnotationFixture {
	run := map[string]any{"id": 17, "head_sha": testHead, "name": "SonarCloud Code Analysis", "status": "completed", "conclusion": "failure", "html_url": "https://private.invalid", "app": map[string]any{"id": 44, "slug": "sonarqubecloud"}, "output": map[string]any{"annotations_count": 4}}
	prefix := "repos/example/project"
	return &ciAnnotationFixture{responses: map[string]any{
		prefix + "/commits/" + testHead + "/check-runs?per_page=100&page=1": map[string]any{"total_count": 1, "check_runs": []any{run}},
		prefix + "/git/commits/" + testHead:                                 map[string]any{"sha": testHead, "tree": map[string]any{"sha": testTree}},
		prefix + "/git/trees/" + testTree + "?recursive=1": map[string]any{"sha": testTree, "truncated": false, "tree": []any{
			map[string]any{"path": "src", "mode": "040000", "type": "tree"},
			map[string]any{"path": "src/Example.cs", "mode": "100644", "type": "blob"},
			map[string]any{"path": "src/Link.cs", "mode": "120000", "type": "blob"},
			map[string]any{"path": ".secrets/private.cs", "mode": "100644", "type": "blob"},
		}},
		prefix + "/check-runs/17": run,
		prefix + "/check-runs/17/annotations?per_page=100&page=1": []any{
			map[string]any{"path": "src/Example.cs", "start_line": 9, "end_line": 9, "annotation_level": "warning", "title": "private literal", "message": "private warning operand", "raw_details": "private credential", "blob_href": "https://private.invalid"},
			map[string]any{"path": "src/Example.cs", "start_line": 21, "end_line": 24, "annotation_level": "failure", "title": "Refactor this method to reduce its Cognitive Complexity from 18 to the 15 allowed.", "message": "private method text", "raw_details": "private stack"},
			map[string]any{"path": "src/Link.cs", "start_line": 1, "end_line": 1, "annotation_level": "failure", "title": "private symlink"},
			map[string]any{"path": ".secrets/private.cs", "start_line": 1, "end_line": 1, "annotation_level": "failure", "title": "private excluded"},
		},
	}}
}
func (f *ciAnnotationFixture) command(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "gh" {
		return nil, errors.New("unexpected command")
	}
	if args[0] == "pr" && args[1] == "view" {
		f.views++
		head := testHead
		if f.changeFinalHead && f.views == 2 {
			head = testTree
		}
		return json.Marshal(map[string]any{"baseRefOid": testBase, "headRefOid": head, "state": "OPEN"})
	}
	if args[0] == "pr" && args[1] == "checks" {
		if f.checkName != "" {
			data, _ := json.Marshal([]map[string]string{{"name": f.checkName, "bucket": "fail", "state": "COMPLETED", "link": "https://private.invalid"}})
			return data, ciStatusExit(1)
		}
		return []byte(`[{"name":"SonarCloud Code Analysis","bucket":"fail","state":"COMPLETED","link":"https://private.invalid"}]`), ciStatusExit(1)
	}
	endpoint := args[len(args)-1]
	if endpoint == f.fail {
		return nil, errors.New("private API authentication diagnostic")
	}
	value, ok := f.responses[endpoint]
	if !ok {
		return nil, fmt.Errorf("unrecognized synthetic API")
	}
	if raw, ok := value.([]byte); ok {
		return raw, nil
	}
	return json.Marshal(value)
}

func workflowCIAnnotationFixture() *ciAnnotationFixture {
	f := newCIAnnotationFixture()
	f.checkName = "Example SonarCloud Scan / offline job"
	run := f.responses["repos/example/project/check-runs/17"].(map[string]any)
	run["name"] = f.checkName
	run["app"] = map[string]any{"id": 15368, "slug": "github-actions"}
	run["details_url"] = "https://github.com/example/project/actions/runs/88/job/99"
	run["output"] = map[string]any{"annotations_count": 0}
	base := actionsCIFailureFixture()
	job := base.responses["repos/example/project/actions/jobs/99"].(map[string]any)
	job["name"], job["check_run_url"] = f.checkName, "https://api.github.com/repos/example/project/check-runs/17"
	for endpoint, value := range base.responses {
		if strings.Contains(endpoint, "/actions/") || endpoint == "repos/example/project" {
			f.responses[endpoint] = value
		}
	}
	return f
}

func TestVerifiedActionsWorkflowMentioningSonarReturnsGenericFailedCI(t *testing.T) {
	f := workflowCIAnnotationFixture()
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.Status != "failed" || len(result.Diagnostics) != 0 || result.Details != "" || f.views != 2 {
		t.Fatal("verified Actions failure was not generic CI", result, err)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "/annotations") || strings.Contains(call, "/git/commits") || strings.Contains(call, "/git/trees") {
			t.Fatal("Actions workflow was treated as native source analysis", call)
		}
	}
	encoded, _ := json.Marshal(result)
	feedback := ciRepairFeedback(result, testHead)
	for _, value := range []string{"private warning operand", "private credential", "private.invalid"} {
		if strings.Contains(string(encoded), value) || strings.Contains(feedback, value) {
			t.Fatal("workflow metadata leaked into CI repair guidance", value)
		}
	}
	if result.FailureContext == nil || result.FailureContext.Checks[0].Kind != "actions" || !strings.Contains(feedback, "UnknownProbeFailure") {
		t.Fatal("Actions failure context was not supplied privately")
	}
}

func TestActionsSonarCandidateRequiresExactHeadAppAndDetailAuthority(t *testing.T) {
	for _, scenario := range []string{"list ID", "detail ID", "detail slug", "stale head", "stale status", "changed conclusion", "API", "duplicate", "missing name", "native claimed name", "compact native claimed name", "final PR"} {
		t.Run(scenario, func(t *testing.T) {
			f := workflowCIAnnotationFixture()
			endpoint := "repos/example/project/check-runs/17"
			original := f.responses[endpoint].(map[string]any)
			detail := map[string]any{}
			for k, v := range original {
				detail[k] = v
			}
			detail["app"] = map[string]any{"id": 15368, "slug": "github-actions"}
			f.responses[endpoint] = detail
			switch scenario {
			case "list ID":
				original["app"] = map[string]any{"id": 1, "slug": "github-actions"}
			case "detail ID":
				detail["app"] = map[string]any{"id": 1, "slug": "github-actions"}
			case "detail slug":
				detail["app"] = map[string]any{"id": 15368, "slug": "unknown-app"}
			case "stale head":
				detail["head_sha"] = testBase
			case "stale status":
				detail["status"] = "in_progress"
			case "changed conclusion":
				detail["conclusion"] = "success"
			case "API":
				f.fail = endpoint
			case "duplicate":
				listing := f.responses["repos/example/project/commits/"+testHead+"/check-runs?per_page=100&page=1"].(map[string]any)
				duplicate := map[string]any{}
				for k, v := range original {
					duplicate[k] = v
				}
				duplicate["id"] = 18
				listing["check_runs"] = []any{original, duplicate}
				listing["total_count"] = 2
			case "missing name":
				original["name"] = "Different SonarCloud Scan"
			case "native claimed name":
				f.checkName = "SonarCloud Code Analysis"
				original["name"] = f.checkName
				detail["name"] = f.checkName
			case "compact native claimed name":
				f.checkName = "SonarCloudAnalysis"
				original["name"] = f.checkName
				detail["name"] = f.checkName
			case "final PR":
				f.changeFinalHead = true
			}
			if _, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication()); err == nil {
				t.Fatal("unverified workflow failure was granted generic repair authority")
			}
		})
	}
}
func TestGitHubCIAnnotationsRejectChangedNativeAppID(t *testing.T) {
	f := newCIAnnotationFixture()
	details := 0
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "repos/example/project/check-runs/17" {
			details++
			if details == 2 {
				f.responses["repos/example/project/check-runs/17"].(map[string]any)["app"] = map[string]any{"id": 1, "slug": "sonarqubecloud"}
			}
		}
		return f.command(ctx, name, args...)
	}
	if _, err := (GitHubPublisher{Command: command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication()); err == nil {
		t.Fatal("changed native app ID was accepted after annotation collection")
	}
	if details != 2 {
		t.Fatal("native check-run was not reread after annotation collection", details)
	}
}

func annotationPublication() Publication {
	return Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
}

func TestGitHubCIAnnotationsExactHeadAndSafeFeedback(t *testing.T) {
	f := newCIAnnotationFixture()
	result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{"private literal"}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.Status != "failed" || len(result.Diagnostics) != 2 {
		t.Fatalf("missing annotations: %+v %v", result, err)
	}
	a := result.Diagnostics[0]
	if a.Head != testHead || a.Path != "src/Example.cs" || a.Line != 21 || a.Kind != "cognitive_complexity" || a.Current != 18 || a.Limit != 15 || a.Rule != "" {
		t.Fatalf("wrong failure metadata: %+v", a)
	}
	if result.Diagnostics[1].Level != "warning" || result.Diagnostics[1].Kind != "generic" || f.views != 2 {
		t.Fatal("failure ordering or final PR guard lost")
	}
	encoded, _ := json.Marshal(result)
	feedback := ciRepairFeedback(result, testHead)
	for _, private := range []string{"private", "method text", "stack", "credential", "https://", "Link.cs", ".secrets"} {
		if strings.Contains(string(encoded)+feedback, private) {
			t.Fatalf("private annotation text escaped: %s", encoded)
		}
	}
	if !strings.Contains(feedback, "src/Example.cs:21") || !strings.Contains(feedback, "from 18 to at most 15") {
		t.Fatal("safe complexity guidance missing")
	}
	if err := validateCIResult(result, annotationPublication()); err != nil {
		t.Fatal(err)
	}
}
func TestGitHubCIAnnotationOperationalFailures(t *testing.T) {
	prefix := "repos/example/project"
	for _, kind := range []string{"API", "oversize", "malformed", "stale list", "stale run", "wrong app", "tree head", "truncated tree", "location", "final PR"} {
		t.Run(kind, func(t *testing.T) {
			f := newCIAnnotationFixture()
			list := prefix + "/commits/" + testHead + "/check-runs?per_page=100&page=1"
			run := prefix + "/check-runs/17"
			tree := prefix + "/git/trees/" + testTree + "?recursive=1"
			switch kind {
			case "API":
				f.fail = list
			case "oversize":
				f.responses[list] = []byte(strings.Repeat("x", maxCIAPIBytes+1))
			case "malformed":
				f.responses[list] = []byte(`{"total_count":1}`)
			case "stale list":
				f.responses[list].(map[string]any)["check_runs"].([]any)[0].(map[string]any)["head_sha"] = testBase
			case "stale run":
				original := f.responses[run].(map[string]any)
				copy := map[string]any{}
				for k, v := range original {
					copy[k] = v
				}
				copy["id"] = 18
				f.responses[run] = copy
			case "wrong app":
				f.responses[run].(map[string]any)["app"] = map[string]any{"slug": "github-actions"}
			case "tree head":
				f.responses[tree].(map[string]any)["sha"] = testBase
			case "truncated tree":
				f.responses[tree].(map[string]any)["truncated"] = true
			case "location":
				f.responses[prefix+"/check-runs/17/annotations?per_page=100&page=1"].([]any)[0].(map[string]any)["start_line"] = 0
			case "final PR":
				f.changeFinalHead = true
			}
			result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if err == nil || len(result.Diagnostics) > 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("operational failure became repair metadata: %+v %v", result, err)
			}
		})
	}
}
func TestCIDiagnosticIPCAndJournalBounds(t *testing.T) {
	valid := CIAnnotation{Head: testHead, Path: "src/Example.cs", Line: 1, Level: "failure", Kind: "generic", Rule: "csharpsquid:S3776"}
	for _, kind := range []string{"head", "path", "control", "line", "end", "level", "rule", "kind", "complexity"} {
		t.Run(kind, func(t *testing.T) {
			a := valid
			switch kind {
			case "head":
				a.Head = testBase
			case "path":
				a.Path = "../private.cs"
			case "control":
				a.Path = "src/private\x1b.cs"
			case "line":
				a.Line = 10000000
			case "end":
				a.EndLine = -1
			case "level":
				a.Level = "private"
			case "rule":
				a.Rule = "S3776 private message"
			case "kind":
				a.Kind = "private"
			case "complexity":
				a.Kind = "cognitive_complexity"
				a.Rule = ""
				a.Current = 15
				a.Limit = 18
			}
			dir, j := testRun(t)
			j.Publication = annotationPublication()
			j.CI = CIResult{Status: "failed", Diagnostics: []CIAnnotation{a}}
			if Save(dir, j) == nil {
				t.Fatal("invalid metadata saved")
			}
			if err := saveJSON(filepath.Join(dir, "journal.json"), j); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("invalid metadata loaded")
			}
		})
	}
	dir, j := testRun(t)
	j.Publication = annotationPublication()
	j.Rounds = 12
	j.CIRepairBudget = &CIRepairBudget{Used: 1, StartRound: 11, LastGrantedHead: testHead}
	j.CI = CIResult{Status: "failed", Diagnostics: []CIAnnotation{valid}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.CI.Diagnostics[0] != valid || loaded.CIRepairBudget.Used != 1 {
		t.Fatal("typed CI metadata lost", err)
	}
}

func TestGitHubCIAnnotationPaginationAndMetadataBounds(t *testing.T) {
	f := newCIAnnotationFixture()
	prefix := "repos/example/project"
	f.responses[prefix+"/check-runs/17"].(map[string]any)["output"] = map[string]any{"annotations_count": 101}
	annotations := make([]any, 100)
	for i := range annotations {
		annotations[i] = map[string]any{"path": "src/Example.cs", "start_line": i + 1, "end_line": i + 1, "annotation_level": "warning", "title": "private literal"}
	}
	f.responses[prefix+"/check-runs/17/annotations?per_page=100&page=1"] = annotations
	f.responses[prefix+"/check-runs/17/annotations?per_page=100&page=2"] = []any{map[string]any{"path": "src/Example.cs", "start_line": 200, "end_line": 200, "annotation_level": "failure", "title": "csharpsquid:S3776", "message": "private source"}}
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || len(result.Diagnostics) != 20 || result.Diagnostics[0].Level != "failure" || result.Diagnostics[0].Rule != "csharpsquid:S3776" || result.Diagnostics[0].Line != 200 {
		t.Fatalf("bounded pagination lost failure priority: %+v %v", result, err)
	}
	for endpoint := range newCIAnnotationFixture().responses {
		f := newCIAnnotationFixture()
		f.fail = endpoint
		if _, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication()); err == nil {
			t.Fatal("API failure accepted", endpoint)
		}
	}
}

func TestGitHubCIAnnotationAllowsDocumentedUnrelatedWaitingStatuses(t *testing.T) {
	for _, status := range []string{"waiting", "requested", "pending"} {
		f := newCIAnnotationFixture()
		list := f.responses["repos/example/project/commits/"+testHead+"/check-runs?per_page=100&page=1"].(map[string]any)
		list["total_count"] = 2
		list["check_runs"] = append(list["check_runs"].([]any), map[string]any{"id": 18, "head_sha": testHead, "name": "Unrelated check", "status": status})
		result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
		if err != nil || len(result.Diagnostics) != 2 {
			t.Fatal("valid unrelated status blocked Sonar diagnostics", status, err)
		}
	}
}

func TestGitHubChecksRejectsOperationalErrorsWithValidJSON(t *testing.T) {
	for _, failure := range []error{context.Canceled, errors.New("private transport failure"), ciStatusExit(2)} {
		fixture := newCIAnnotationFixture()
		publisher := GitHubPublisher{Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			data, err := fixture.command(ctx, name, args...)
			if args[0] == "pr" && args[1] == "checks" {
				return data, failure
			}
			return data, err
		}}
		if result, err := publisher.Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication()); err == nil || len(result.Diagnostics) > 0 {
			t.Fatal("operational failure became CI repair", err)
		}
	}
}

func TestCIOutputLimitOnlyAllowsExactVerifiedJobLogArgv(t *testing.T) {
	valid := []string{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/99/logs"}
	if ciCommandOutputLimit("gh", valid) != maxActionsJobLogBytes {
		t.Fatal("generated log request did not select log intake")
	}
	artifact := []string{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/artifacts/123/zip"}
	if ciCommandOutputLimit("gh", artifact) != maxCIArtifactZIPBytes {
		t.Fatal("exact artifact ZIP argv did not select binary intake")
	}
	for _, args := range [][]string{
		{"api", "repos/example/project/actions/jobs/99/logs"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/artifacts/0123/zip"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/artifacts/123"},
		append(append([]string(nil), artifact...), "--method", "POST"),
		{"api", "--hostname", "other.invalid", "--allow-escape-sequences", valid[4]},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/0/logs"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/099/logs"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/999999999999999999999/logs"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/99"},
		{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/../project/actions/jobs/99/logs"},
		append(append([]string(nil), valid...), "--method", "POST"),
	} {
		if ciCommandOutputLimit("gh", args) != maxCIAPIBytes {
			t.Fatal("unverified argv enlarged intake", args)
		}
	}
	if ciCommandOutputLimit("other", valid) != maxCIAPIBytes {
		t.Fatal("non-gh executable enlarged intake")
	}
	for _, limit := range []int{maxCIAPIBytes, maxActionsJobLogBytes} {
		output := ciAPIOutput{limit: limit}
		output.Write([]byte(strings.Repeat("x", limit)))
		if output.exceeded || output.buffer.Len() != limit {
			t.Fatal("exact bound was rejected")
		}
		output.Write([]byte("x"))
		output.Write([]byte("later"))
		if !output.exceeded || output.buffer.Len() != limit {
			t.Fatal("oversized output retained extra bytes")
		}
	}
	if _, _, err := sanitizeCIText(strings.Repeat("x", maxCIAPIBytes+1), nil); err == nil {
		t.Fatal("metadata sanitizer bound was widened")
	}
}
