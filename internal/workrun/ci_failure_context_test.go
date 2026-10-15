package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ciFailureFixture struct {
	checks    []ciRequestedCheck
	responses map[string]any
	calls     []string
	before    func(string)
	fail      string
	views     int
	finalHead string
}

func genericCIFailureFixture() *ciFailureFixture {
	link := "https://ci.example.invalid/result/41"
	run := map[string]any{"id": 41, "name": "offline unit suite", "head_sha": testHead, "status": "completed", "conclusion": "failure", "html_url": link, "details_url": link, "app": map[string]any{"id": 90, "slug": "example-checks"}, "output": map[string]any{"annotations_count": 1, "title": "Unit suite failed", "text": "UnknownProbeFailure: expected one item at src/Example.cs:42."}}
	return &ciFailureFixture{checks: []ciRequestedCheck{{Name: "offline unit suite", Bucket: "fail", State: "COMPLETED", Link: link}}, responses: map[string]any{
		"repos/example/project/commits/" + testHead + "/check-runs?per_page=100&page=1": map[string]any{"total_count": 1, "check_runs": []any{run}},
		"repos/example/project/check-runs/41":                                           run,
		"repos/example/project/check-runs/41/annotations?per_page=100&page=1":           []any{map[string]any{"path": "src/Example.cs", "start_line": 42, "annotation_level": "failure", "title": "Unexpected result", "message": "UnknownProbeFailure: actual result differs."}},
	}}
}

func actionsCIFailureFixture() *ciFailureFixture {
	f := genericCIFailureFixture()
	check := f.responses["repos/example/project/check-runs/41"].(map[string]any)
	link := "https://github.com/example/project/actions/runs/88/job/99"
	f.checks[0].Link = link
	check["html_url"], check["details_url"], check["app"] = link, link, map[string]any{"id": 15368, "slug": "github-actions"}
	check["output"] = map[string]any{"annotations_count": 0}
	run := map[string]any{"id": 88, "run_attempt": 2, "head_sha": testHead, "status": "completed", "conclusion": "failure", "repository": map[string]any{"id": 7, "full_name": "example/project"}}
	job := map[string]any{"id": 99, "run_id": 88, "head_sha": testHead, "name": "offline unit suite", "status": "completed", "conclusion": "failure", "check_run_url": "https://api.github.com/repos/example/project/check-runs/41", "steps": []any{map[string]any{"name": "Execute unit suite", "number": 2, "status": "completed", "conclusion": "failure", "started_at": "2026-01-01T12:00:00Z", "completed_at": "2026-01-01T12:02:00Z"}}}
	f.responses["repos/example/project"] = map[string]any{"id": 7, "full_name": "example/project"}
	f.responses["repos/example/project/actions/runs/88"] = run
	f.responses["repos/example/project/actions/jobs/99"] = job
	f.responses["repos/example/project/actions/runs/88/attempts/2/jobs?per_page=100&page=1"] = map[string]any{"total_count": 1, "jobs": []any{job}}
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(strings.Repeat("2026-01-01T11:00:00Z routine setup output\n", 1200) + "2026-01-01T12:01:00Z UnknownProbeFailure: expected one item at src/Example.cs:42.\n" + strings.Repeat("2026-01-01T13:00:00Z routine cleanup output\n", 1200))
	return f
}

func TestSkippedActionsJobRetainsMetadataWithoutRequestingLogs(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "changed boundary"}[changed], func(t *testing.T) {
			f := actionsCIFailureFixture()
			f.checks[0].Bucket = "skipping"
			check := f.responses["repos/example/project/check-runs/41"].(map[string]any)
			check["conclusion"] = "skipped"
			job := f.responses["repos/example/project/actions/jobs/99"].(map[string]any)
			job["conclusion"], job["steps"] = "skipped", []any{}
			logs, jobReads := 0, 0
			command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				endpoint := args[len(args)-1]
				if strings.HasSuffix(endpoint, "/logs") {
					logs++
					return nil, &ciRequestError{Status: 404}
				}
				if endpoint == "repos/example/project/actions/jobs/99" {
					jobReads++
					if changed && jobReads == 2 {
						job["conclusion"] = "failure"
					}
				}
				return f.command(ctx, name, args...)
			}
			result, err := (GitHubPublisher{Command: command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if logs != 0 {
				t.Fatalf("requested nonexistent skipped-job logs %d times: %v", logs, err)
			}
			if changed {
				if err == nil || result.FailureContext != nil {
					t.Fatal("skipped job boundary change accepted", result, err)
				}
				return
			}
			if err != nil || result.Status != "failed" || result.FailureContext == nil || len(result.FailureContext.Checks) != 1 {
				t.Fatal("skipped metadata blocked collection or certified CI", result, err)
			}
			entry := result.FailureContext.Checks[0]
			if entry.Conclusion != "skipped" || entry.JobID != 99 || entry.RunID != 88 || entry.RunAttempt != 2 || !strings.Contains(entry.Text, "skipped") || jobReads != 2 {
				t.Fatal("verified skipped-job evidence lost", entry, jobReads)
			}
		})
	}
}

func (f *ciFailureFixture) command(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "gh" {
		return nil, errors.New("unexpected client")
	}
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if args[0] == "pr" && args[1] == "view" {
		f.views++
		head := testHead
		if f.views > 1 && f.finalHead != "" {
			head = f.finalHead
		}
		return json.Marshal(map[string]string{"baseRefOid": testBase, "headRefOid": head, "state": "OPEN"})
	}
	if args[0] == "pr" && args[1] == "checks" {
		data, err := json.Marshal(f.checks)
		if err != nil {
			return nil, err
		}
		return data, ciStatusExit(1)
	}
	if args[0] != "api" {
		return nil, errors.New("unexpected CI command")
	}
	endpoint := args[len(args)-1]
	if f.before != nil {
		f.before(endpoint)
	}
	if endpoint == f.fail {
		return nil, errors.New("opaque private transport diagnostic")
	}
	value, ok := f.responses[endpoint]
	if !ok {
		return nil, fmt.Errorf("missing offline API fixture")
	}
	if raw, ok := value.([]byte); ok {
		return raw, nil
	}
	return json.Marshal(value)
}

func TestAutomaticCIFailureContextCollectsGenericAndActionsUnknownErrors(t *testing.T) {
	for _, kind := range []string{"generic", "actions"} {
		t.Run(kind, func(t *testing.T) {
			f := genericCIFailureFixture()
			if kind == "actions" {
				f = actionsCIFailureFixture()
			}
			result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if err != nil || result.Status != "failed" || result.FailureContext == nil || len(result.FailureContext.Checks) != 1 || !strings.Contains(result.FailureContext.Checks[0].Text, "UnknownProbeFailure") || f.views != 2 {
				t.Fatal("actual unknown failure was not collected", err)
			}
			entry := result.FailureContext.Checks[0]
			if kind == "actions" && (entry.Kind != "actions" || entry.RunID != 88 || entry.RunAttempt != 2 || entry.JobID != 99 || !entry.Truncated || !strings.Contains(entry.Text, "Execute unit suite")) {
				t.Fatal("Actions authority or middle failure window missing")
			}
			if kind == "generic" && (entry.Kind != "check" || entry.AppID != 90 || !strings.Contains(entry.Text, "actual result differs")) {
				t.Fatal("custom check output/annotation omitted")
			}
			for _, call := range f.calls {
				if strings.Contains(call, "gh run") || strings.Contains(call, "gh api --hostname github.com https://") {
					t.Fatal("used unverified log transport")
				}
			}
		})
	}
}

func TestActionsLongFailedStepPreservesMiddleFailureAmongManySeverityLines(t *testing.T) {
	f := actionsCIFailureFixture()
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-01-01T12:01:00Z routine output record %d", i)
		if i%31 == 0 {
			lines[i] = fmt.Sprintf("2026-01-01T12:01:00Z ERROR: emitted assertion evidence %d", i)
		}
	}
	lines[500] = "2026-01-01T12:01:00Z UnknownProbeFailure: MiddleAssertionSentinel mismatch at src/Example.cs:42."
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(strings.Join(lines, "\n"))
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil {
		t.Fatal(err)
	}
	if !result.FailureContext.Checks[0].Truncated || !strings.Contains(result.FailureContext.Checks[0].Text, "MiddleAssertionSentinel") || !strings.Contains(ciRepairFeedback(result, testHead), "MiddleAssertionSentinel") {
		t.Fatal("timestamped failed-step boundaries or outer summaries displaced middle failure")
	}
}

func TestAutomaticCIFailureContextCollectsMultipleChecksAndCommitStatus(t *testing.T) {
	f := genericCIFailureFixture()
	f.checks = append(f.checks, ciRequestedCheck{Name: "external analysis", Bucket: "fail", Link: "https://ci.example.invalid/status/52"})
	s := map[string]any{"id": 52, "context": "external analysis", "state": "failure", "description": "UnknownAnalysisFailure at src/Other.cs:14.", "target_url": "https://ci.example.invalid/status/52", "creator": map[string]any{"id": 8}}
	f.responses["repos/example/project/commits/"+testHead+"/status?per_page=100&page=1"] = map[string]any{"sha": testHead, "total_count": 1, "statuses": []any{s}}
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || len(result.FailureContext.Checks) != 2 || result.FailureContext.Checks[1].Kind != "status" || result.FailureContext.Checks[1].StatusID != 52 || !strings.Contains(ciFailureFeedback(result), "UnknownAnalysisFailure") {
		t.Fatal("all failed checks were not handed off", err)
	}
}

func TestCIFailureAggregateBudgetRetainsEveryFailedIdentityAndError(t *testing.T) {
	f := genericCIFailureFixture()
	f.checks = nil
	runs := []any{}
	for i := 0; i < maxCIFailedChecks; i++ {
		id := 41 + i
		name := fmt.Sprintf("offline suite %d", i)
		link := fmt.Sprintf("https://ci.example.invalid/result/%d", id)
		run := map[string]any{"id": id, "name": name, "head_sha": testHead, "status": "completed", "conclusion": "failure", "html_url": link, "details_url": link, "app": map[string]any{"id": 90, "slug": "example-checks"}, "output": map[string]any{"annotations_count": 0, "text": strings.Repeat("<", 5000) + fmt.Sprintf("\nUnknownProbeFailure-%d: actual assertion evidence.\n", i) + strings.Repeat(">", 5000)}}
		f.checks = append(f.checks, ciRequestedCheck{Name: name, Bucket: "fail", Link: link})
		runs = append(runs, run)
		f.responses[fmt.Sprintf("repos/example/project/check-runs/%d", id)] = run
	}
	f.responses["repos/example/project/commits/"+testHead+"/check-runs?per_page=100&page=1"] = map[string]any{"total_count": len(runs), "check_runs": runs}
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.FailureContext)
	if len(data) > maxCIFailureContextBytes || len(result.FailureContext.Checks) != maxCIFailedChecks {
		t.Fatal("aggregate projection omitted failed checks or exceeded bound")
	}
	for i, check := range result.FailureContext.Checks {
		if !check.Truncated || !strings.Contains(check.Text, fmt.Sprintf("UnknownProbeFailure-%d", i)) {
			t.Fatal("aggregate allocation discarded relevant unknown error", i)
		}
	}
}

func TestCIOperationalCollectionFailuresCannotGrantRepair(t *testing.T) {
	for _, kind := range []string{"oversize", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, _ := ciFeedbackRun(t)
			j.State = "ci"
			j.ResumeState = ""
			j.StopReason = ""
			j.CIRepairBudget.LastGrantedHead = testBase
			r.CIFeedback = ""
			f := actionsCIFailureFixture()
			if kind == "oversize" {
				f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(strings.Repeat("x", maxActionsJobLogBytes+1))
			} else {
				f.fail = "repos/example/project/actions/jobs/99/logs"
			}
			r.Publisher = GitHubPublisher{Command: f.command}
			if err := r.checkCI(context.Background(), dir, j); !errors.Is(err, ErrStopped) || len(p.calls) != 0 || j.CIRepairBudget.Used != 2 || j.CIRepairBudget.LastGrantedHead != testBase || j.Rounds != 26 || j.CI.FailureContext != nil {
				t.Fatal("operational collection failure granted native repair", err)
			}
		})
	}
}

func TestAutomaticActionsFailureAuthorityChangesHold(t *testing.T) {
	for _, kind := range []string{"app", "head", "repo id", "repo name", "job head", "job run", "job association", "job name", "job status", "attempt", "missing attempt job", "rerun", "job changed", "final PR", "transport", "oversize", "invalid utf8", "incomplete key"} {
		t.Run(kind, func(t *testing.T) {
			f := actionsCIFailureFixture()
			check := f.responses["repos/example/project/check-runs/41"].(map[string]any)
			run := f.responses["repos/example/project/actions/runs/88"].(map[string]any)
			job := f.responses["repos/example/project/actions/jobs/99"].(map[string]any)
			switch kind {
			case "app":
				check["app"] = map[string]any{"id": 1, "slug": "github-actions"}
			case "head":
				check["head_sha"] = testBase
			case "repo id":
				run["repository"] = map[string]any{"id": 8, "full_name": "example/project"}
			case "repo name":
				run["repository"] = map[string]any{"id": 7, "full_name": "example/other"}
			case "job head":
				job["head_sha"] = testBase
			case "job run":
				job["run_id"] = 89
			case "job association":
				job["check_run_url"] = "https://api.github.com/repos/example/project/check-runs/42"
			case "job name":
				job["name"] = "another job"
			case "job status":
				job["status"] = "in_progress"
			case "attempt":
				run["run_attempt"] = 3
			case "missing attempt job":
				f.responses["repos/example/project/actions/runs/88/attempts/2/jobs?per_page=100&page=1"] = map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": 100}}}
			case "rerun":
				f.before = func(endpoint string) {
					if strings.HasSuffix(endpoint, "/logs") {
						run["run_attempt"] = 3
					}
				}
			case "job changed":
				f.before = func(endpoint string) {
					if strings.HasSuffix(endpoint, "/logs") {
						job["conclusion"] = "success"
					}
				}
			case "final PR":
				f.finalHead = testBase
			case "transport":
				f.fail = "repos/example/project/actions/jobs/99/logs"
			case "oversize":
				f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(strings.Repeat("x", maxActionsJobLogBytes+1))
			case "invalid utf8":
				f.responses["repos/example/project/actions/jobs/99/logs"] = []byte{0xff}
			case "incomplete key":
				f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(fmt.Sprintf("-----%s PRIVATE KEY-----\nopaque disposable bytes", "BEGIN"))
			}
			result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if err == nil || result.FailureContext != nil || strings.Contains(err.Error(), "opaque") {
				t.Fatal("unverified context escaped operational hold", kind)
			}
			if kind == "oversize" && !strings.Contains(err.Error(), "16777216-byte intake bound") {
				t.Fatal("oversized log hold omitted the controlled numeric bound", err)
			}
		})
	}
}

func TestActionsLogEscapeSequencesAreSanitizedBeforeEvidence(t *testing.T) {
	f := actionsCIFailureFixture()
	secret := "disposable-private-log-value"
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte("\x1b[31mUnknownProbeFailure: exact evidence remains.\x1b[0m\n" + "disposable-\x1b[32mprivate-log-value\x1b[0m\n")
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		allowEscapes := false
		for _, arg := range args {
			allowEscapes = allowEscapes || arg == "--allow-escape-sequences"
		}
		if strings.HasSuffix(args[len(args)-1], "/logs") {
			if !allowEscapes {
				return nil, errors.New("response contains terminal escape sequences")
			}
		} else if allowEscapes {
			t.Fatal("raw escape output enabled for metadata command")
		}
		return f.command(ctx, name, args...)
	}
	result, err := (GitHubPublisher{Command: command, CIPrivateValues: []string{secret}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil {
		t.Fatal("official client refused Actions log evidence", err)
	}
	encoded, err := json.Marshal(result.FailureContext)
	if err != nil || result.FailureContext == nil || len(result.FailureContext.Checks) != 1 {
		t.Fatal("missing sanitized Actions evidence", err)
	}
	entry := result.FailureContext.Checks[0]
	feedback := ciFailureFeedback(result)
	if !entry.Redacted || !strings.Contains(entry.Text, "UnknownProbeFailure") || strings.Contains(string(encoded)+feedback, secret) || strings.Contains(string(encoded), `\u001b`) || strings.Contains(feedback, "\x1b") {
		t.Fatal("raw controls or private value reached retained evidence")
	}
}

func TestCIFailureMaskingPrecedesExcerptsAndPreservesFailure(t *testing.T) {
	f := actionsCIFailureFixture()
	secret := "disposable-opaque-check-input-value"
	credentials := []string{"ghp_" + strings.Repeat("a", 36), "AKIA" + strings.Repeat("A", 16), "eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 12) + "." + strings.Repeat("c", 12), "opaque-assigned-password", "opaque-basic-value", "opaque-user", "opaque-query-value", "opaque-private-key-body", secret}
	raw := "UnknownProbeFailure: exact evidence remains.\n" + credentials[0] + "\n" + credentials[1] + "\n" + credentials[2] + "\npassword=" + credentials[3] + "\nAuthorization: Basic " + credentials[4] + "\nhttps://" + credentials[5] + ":pass@example.invalid/log?signature=" + credentials[6] + fmt.Sprintf("\n-----%s PRIVATE KEY-----\n", "BEGIN") + credentials[7] + fmt.Sprintf("\n-----%s PRIVATE KEY-----\n", "END") + secret + "\n\x1b[31mghp_\u200b" + strings.Repeat("a", 36) + "\x1b[0m\n"
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(raw + strings.Repeat("ordinary filler\n", 800))
	result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{secret}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	feedback := ciRepairFeedback(result, testHead)
	for _, value := range credentials {
		if strings.Contains(string(encoded)+feedback, value) {
			t.Fatal("credential sentinel escaped masking")
		}
	}
	if !result.FailureContext.Checks[0].Redacted || !result.FailureContext.Checks[0].Truncated || !strings.Contains(feedback, "UnknownProbeFailure") || strings.Contains(feedback, "\x1b") {
		t.Fatal("masking/truncation obscured relevant failure or lost flags")
	}
}

func TestCIPrivateLiteralMaskingSurvivesURLAndControlNormalization(t *testing.T) {
	for _, value := range []string{"https://example.invalid/private-path-sentinel?mode=test", "opaque\u200b-private-sentinel", "opaque\r-private-sentinel"} {
		f := actionsCIFailureFixture()
		f.responses["repos/example/project/actions/jobs/99/logs"] = []byte("UnknownProbeFailure evidence.\n" + value)
		result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{value}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		for _, sentinel := range []string{"private-path-sentinel", "opaque-private-sentinel", value} {
			if strings.Contains(string(encoded)+ciFailureFeedback(result), sentinel) {
				t.Fatal("private literal survived normalization")
			}
		}
		if !result.FailureContext.Checks[0].Redacted {
			t.Fatal("literal masking flag absent")
		}
	}
}

func TestCIPrivateLiteralMaskingUnionsOverlappingAndNormalizedAliases(t *testing.T) {
	for _, values := range [][]string{{"ab", "bcde"}, {"ab\u200b", "bcde"}, {"aba"}} {
		raw := "abcde"
		if len(values) == 1 {
			raw = "ababa"
		}
		clean, redacted, err := sanitizeCIText("Selected evidence: "+raw, values)
		if err != nil || !redacted || clean != "Selected evidence: [REDACTED]" {
			t.Fatal("overlapping private literal suffix escaped", clean, err)
		}
	}
}

func TestCIJSONCredentialAssignmentsMaskWithoutLocalDictionary(t *testing.T) {
	f := genericCIFailureFixture()
	f.responses["repos/example/project/check-runs/41"].(map[string]any)["output"] = map[string]any{"annotations_count": 0, "text": "UnknownProbeFailure evidence.\n{\"password\":\"opaque-disposable-password\",\"api_key\":\"opaque-disposable-api-value\"}"}
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	for _, secret := range []string{"opaque-disposable-password", "opaque-disposable-api-value"} {
		if strings.Contains(string(data)+ciRepairFeedback(result, testHead), secret) {
			t.Fatal("known JSON credential assignment escaped")
		}
	}
	if !result.FailureContext.Checks[0].Redacted || !strings.Contains(result.FailureContext.Checks[0].Text, "UnknownProbeFailure") {
		t.Fatal("JSON masking lost failure or flag")
	}
}

func TestCIPrivateDotenvSyntaxIsDecodedOrHeldBeforeMasking(t *testing.T) {
	for _, kind := range []string{"simple", "quoted", "inline comment", "escaped quote", "multiline", "yaml block"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			value := "opaque-disposable-input-value"
			data := "PASSWORD=" + value + "\n"
			switch kind {
			case "quoted":
				data = "PASSWORD=\"" + value + "\"\n"
			case "inline comment":
				data = "PASSWORD=" + value + " # comment\n"
			case "escaped quote":
				data = "PASSWORD=\"" + value + "\\\"tail\"\n"
			case "multiline":
				data = "PASSWORD=\"" + value + "\ncontinued\"\n"
			case "yaml block":
				data = "PASSWORD: |\n  " + value + ": body\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "private.env"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			values, err := ciPrivateInputValues(dir, []Input{{Path: "private.env", SHA256: feedbackHash(data)}})
			if kind != "simple" && kind != "quoted" {
				if err == nil || strings.Contains(err.Error(), value) {
					t.Fatal("ambiguous private input syntax escaped preflight hold", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clean, redacted, err := sanitizeCIText("UnknownProbeFailure: observed "+value, values)
			if err != nil || !redacted || strings.Contains(clean, value) {
				t.Fatal("supported private input value reached context", err)
			}
		})
	}
}

func TestCIPrivateMaskOccurrenceLimitHoldsInsteadOfPartialOutput(t *testing.T) {
	clean, _, err := sanitizeCIText(strings.Repeat("a", 128*1024+1), []string{"a"})
	if err == nil || clean != "" {
		t.Fatal("private overlap search exceeded work bound or returned partial context")
	}
}

func TestCIUnsafeMultilineCredentialsAndProtectedAppIdentityHold(t *testing.T) {
	for _, kind := range []string{"multiline credential", "app identity"} {
		t.Run(kind, func(t *testing.T) {
			f := genericCIFailureFixture()
			values := []string{}
			if kind == "multiline credential" {
				f.responses["repos/example/project/check-runs/41"].(map[string]any)["output"] = map[string]any{"annotations_count": 0, "text": "UnknownProbeFailure\npassword: |\n  opaque-password-body\nnext: value"}
			} else {
				values = []string{"example-checks"}
			}
			result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: values}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if err == nil || result.FailureContext != nil || strings.Contains(err.Error(), "opaque-password-body") || strings.Contains(err.Error(), "example-checks") {
				t.Fatal("unsafe selected CI string reached failure context", err)
			}
		})
	}
}

func TestActionsAnnotationsCollectedAndEmptyLogsCannotGenerateEvidence(t *testing.T) {
	f := actionsCIFailureFixture()
	f.responses["repos/example/project/check-runs/41"].(map[string]any)["output"] = map[string]any{"annotations_count": 1}
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || !strings.Contains(result.FailureContext.Checks[0].Text, "actual result differs") {
		t.Fatal("Actions annotation omitted", err)
	}
	f = actionsCIFailureFixture()
	f.responses["repos/example/project/actions/jobs/99"].(map[string]any)["steps"] = []any{}
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte{}
	result, err = (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err == nil || result.FailureContext != nil {
		t.Fatal("generated framing counted as actual failure evidence")
	}
}

func TestDockerCIInputHashDriftHoldsBeforeAuthenticatedBoundary(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secret := "disposable-private-input-sentinel"
	if err := os.WriteFile(filepath.Join(dir, "private.env"), []byte("PASSWORD="+secret), 0600); err != nil {
		t.Fatal(err)
	}
	pairCalls := 0
	publisher := DockerPublisher{CheckInputDirectory: dir, ValidatePair: func() error { pairCalls++; return nil }}
	result, err := publisher.Checks(context.Background(), Plan{CheckInputs: []Input{{Path: "private.env", SHA256: feedbackHash("old-input")}}, PublicationIdentity: frozenTestIdentity()}, annotationPublication())
	if err == nil || pairCalls != 0 || result.FailureContext != nil || strings.Contains(err.Error(), secret) {
		t.Fatal("input drift reached authenticated CI boundary", err)
	}
}

func TestCIPrivateInputValuesRequireCapturedHashesAndSecretKeyPolicy(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private.env")
	data := []byte("PASSWORD=z\nMODE=t\nENDPOINT_VALUE=opaque-disposable-input\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{Path: "private.env", SHA256: feedbackHash(string(data))}}
	values, err := ciPrivateInputValues(dir, inputs)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(values, "\n")
	if !strings.Contains(joined, "z") || strings.Contains(joined, "\nt\n") || !strings.Contains(joined, "opaque-disposable-input") {
		t.Fatal("short secret/nonsecret policy wrong")
	}
	if err := os.WriteFile(path, []byte("PASSWORD=changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ciPrivateInputValues(dir, inputs); err == nil {
		t.Fatal("changed private check input admitted")
	}
}

func TestActionsLogBeyondMetadataBoundRetainsLateMaskedFailure(t *testing.T) {
	fixture := actionsCIFailureFixture()
	secret := "disposable-private-late-value"
	raw := strings.Repeat("2026-01-01T11:00:00Z routine synthetic setup output\n", 15000) + "2026-01-01T12:01:00Z UnknownProbeFailure: LateFailureSentinel " + secret + " at src/Example.cs:42.\n" + strings.Repeat("2026-01-01T13:00:00Z routine cleanup output\n", 1200)
	fixture.responses["repos/example/project/actions/jobs/99/logs"] = []byte(raw)
	result, err := (GitHubPublisher{Command: fixture.command, CIPrivateValues: []string{secret}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.Status != "failed" || result.FailureContext == nil {
		t.Fatal("ordinary large log withheld verified failure packet", err)
	}
	entry := result.FailureContext.Checks[0]
	encoded, _ := json.Marshal(result.FailureContext)
	if !strings.Contains(entry.Text, "LateFailureSentinel") || strings.Contains(string(encoded), secret) || !entry.Redacted || len(entry.Text) > maxCIFailureText {
		t.Fatal("late failure missing, unmasked, or outside retained bound", entry)
	}
}
