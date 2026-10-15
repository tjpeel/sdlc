package runstatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func reportView() View {
	id := strings.Repeat("a", 24)
	j := workrun.Journal{Version: 1, ID: id, State: "ready", Instructions: "excluded-instructions", Feedback: "excluded-feedback", SessionID: "excluded-session-id",
		Plan:        workrun.Plan{Repository: "example/project", Reference: "example", Ticket: "01-example.md", GitHubProfile: "excluded-account", SigningProfile: "excluded-signer", PublicationIdentity: &workrun.PublicationIdentity{GitHubVolume: "excluded-cache", SSHPublicKey: "excluded-public-key"}, CheckInputs: []workrun.Input{{Path: ".env", SHA256: strings.Repeat("b", 64)}}, Roles: workrun.DefaultModels().Codex},
		Evidence:    workrun.CheckEvidence{Tree: strings.Repeat("c", 40), Passed: true, Commands: [][]string{{"dotnet", "test"}}, Log: "excluded-log-path"},
		Publication: workrun.Publication{URL: "https://github.com/example/project/pull/1", Number: 1}, CI: workrun.CIResult{Status: "passed", Details: "excluded-ci-raw-output"},
		Diagnostics: []workrun.VerificationResult{{Request: workrun.VerificationRequest{ID: "excluded-diagnostic-id", Purpose: "excluded-diagnostic-purpose", Commands: [][]string{{"excluded-diagnostic-command"}}}, Output: []workrun.CommandOutput{{CombinedOutput: "excluded-diagnostic-output"}}}},
		Outcome:     workrun.Outcome{Summary: "Checks and independent review passed.", LocalReview: true, PRBody: "excluded-pr-body", Questions: []string{"excluded-answered-question"}},
	}
	return View{ID: id, Available: true, Journal: &j}
}

func TestReportUsesFrozenProvenanceAndExplicitUnknownLegacyValue(t *testing.T) {
	view := reportView()
	report, err := reportSnapshot(view)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report)
	if !strings.Contains(string(data), `"runtime_provenance":null`) {
		t.Fatal("legacy provenance was inferred")
	}
	view.Journal.Plan.RuntimeProvenance = &workrun.RuntimeProvenance{SkillsRevision: strings.Repeat("d", 40)}
	view.Journal.Timings = workrun.Timings{Version: 2, CIWallMS: 100, CheckOperations: []workrun.CheckTiming{{Kind: "command", Phase: "baseline", DurationMS: 20}}}
	report, err = reportSnapshot(view)
	if err != nil || report.RuntimeProvenance.SkillsRevision != strings.Repeat("d", 40) || report.Timings.CIWallMS != 100 || report.Timings.CheckOperations[0].Phase != "baseline" {
		t.Fatal("frozen provenance/timing export lost", err)
	}
}

func TestReportPreservesEvidenceAndExcludesRawCheckpointSecrets(t *testing.T) {
	v := reportView()
	report, err := reportSnapshot(v)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "excluded-") {
		t.Fatal("report copied a raw journal, authentication field or answered question")
	}
	if report.Publication.URL != v.Journal.Publication.URL || !report.Evidence.Passed || report.Evidence.Tree != v.Journal.Evidence.Tree || len(report.CheckInputs) != 1 || report.CheckInputs[0].SHA256 != v.Journal.Plan.CheckInputs[0].SHA256 {
		t.Fatal("durable verification evidence omitted")
	}
	for _, invalid := range []View{{}, {ID: "../../unsafe", Available: true, Journal: v.Journal}, {ID: strings.Repeat("b", 24), Available: true, Journal: v.Journal}} {
		if _, err := reportSnapshot(invalid); err == nil {
			t.Fatal("invalid checkpoint projected")
		}
	}
}

func TestReportRequiresPrivateRealDestinationAndNeverOverwrites(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	v := reportView()
	path, err := ExportReport(v, private)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("report was not private")
	}
	before, _ := os.ReadFile(path)
	if _, err := ExportReport(v, private); err == nil {
		t.Fatal("existing report overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("report changed on rejected overwrite")
	}
	public := filepath.Join(base, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{public, link, filepath.Join(base, "missing"), "relative"} {
		if _, err := ExportReport(v, destination); err == nil {
			t.Fatalf("unsafe destination accepted: %s", destination)
		}
	}
	second := filepath.Join(base, "second")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(second, v.ID+".report.json")
	if err := os.Symlink(path, target); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportReport(v, second); err == nil {
		t.Fatal("report symlink followed")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, target); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportReport(v, second); err == nil {
		t.Fatal("report hardlink overwritten")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("linked report changed")
	}
	checkout := filepath.Join(base, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportReport(v, checkout); err == nil {
		t.Fatal("report exported inside a checkout")
	}
}

func TestReportSizeIsBoundedBeforeCreatingFile(t *testing.T) {
	destination, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	v := reportView()
	v.Journal.Outcome.Summary = strings.Repeat("x", 1024*1024)
	if _, err := ExportReport(v, destination); err == nil {
		t.Fatal("oversized report accepted")
	}
	files, err := os.ReadDir(destination)
	if err != nil || len(files) != 0 {
		t.Fatal("oversized report created an artifact")
	}
}

func TestReportRunLimitOverrideAuditProjection(t *testing.T) {
	view := reportView()
	report, err := reportSnapshot(view)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "run_limit_override") {
		t.Fatal("legacy report included override")
	}
	granted := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	view.Journal.RunLimitOverride = &workrun.RunLimitOverride{
		GrantedAt: granted, CheckpointUpdatedAt: granted.Add(-time.Minute),
		GrantedAttempt: 24, GrantedRounds: 3, OriginalSessionID: "excluded-original-session",
		LimitKind: "native_turns", OriginalLimit: 24, PendingVerificationReplay: true,
	}
	report, err = reportSnapshot(view)
	if err != nil {
		t.Fatal(err)
	}
	audit := report.RunLimitOverride
	if audit == nil || !audit.GrantedAt.Equal(granted) || !audit.CheckpointUpdatedAt.Equal(granted.Add(-time.Minute)) || audit.GrantedAttempt != 24 || audit.GrantedRounds != 3 || audit.LimitKind != "native_turns" || audit.OriginalLimit != 24 || !audit.PendingVerificationReplay {
		t.Fatalf("audit omitted: %+v", audit)
	}
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "excluded-") || strings.Contains(string(data), "original_session_id") {
		t.Fatal("report leaked private session data")
	}
	view.Journal.RunLimitOverride.LimitKind = "excluded-checkpoint-text"
	if RunLimitOverrideStatus(view.Journal.RunLimitOverride).LimitKind != "unknown" {
		t.Fatal("unknown limit kind was not sanitized")
	}
}

func TestStableRunOverrideStatusPreservesAuditWithoutInventingLimit(t *testing.T) {
	granted := time.Now().UTC()
	status := RunLimitOverrideStatus(&workrun.RunLimitOverride{LimitKind: "stable_run", GrantedAt: granted, GrantedAttempt: 6, GrantedRounds: 2, OriginalLimit: 0, OriginalSessionID: "excluded-original-session"})
	if status == nil || status.LimitKind != "stable_run" || status.OriginalLimit != 0 || status.GrantedAttempt != 6 || status.GrantedRounds != 2 || !status.GrantedAt.Equal(granted) {
		t.Fatal("stable permission display lost audit or invented a ceiling")
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "excluded-") || strings.Contains(string(data), "original_session_id") {
		t.Fatal("stable permission display exposed session identity")
	}
}

func TestPrivateReportRetainsReviewHistoryAndRecommendations(t *testing.T) {
	view := reportView()
	coverage := &workrun.ReviewCoverage{BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), Scope: "partial", Gaps: []string{"Remaining behaviour uninspected"}}
	recommendation := workrun.Finding{Priority: "P3", Path: "app.go", Line: 1, Scenario: "Unnecessary state has operating cost", Recommendation: "Reconsider the scope"}
	view.Journal.Outcome.ReviewCoverage = coverage
	view.Journal.Outcome.Recommendations = []workrun.Finding{recommendation}
	view.Journal.ReviewReports = []workrun.ReviewReport{{Coverage: coverage, Recommendations: []workrun.Finding{recommendation}}}
	report, err := reportSnapshot(view)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ReviewCoverage.Scope != "partial" || len(decoded.ReviewReports) != 1 || len(decoded.Recommendations) != 1 || decoded.ReviewReports[0].Coverage.Gaps[0] != coverage.Gaps[0] {
		t.Fatal("private export lost review evidence")
	}
}
