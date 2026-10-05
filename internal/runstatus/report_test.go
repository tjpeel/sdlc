package runstatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func reportView() View {
	id := strings.Repeat("a", 24)
	j := workrun.Journal{Version: 1, ID: id, State: "ready", Instructions: "excluded-instructions", Feedback: "excluded-feedback", SessionID: "excluded-session-id",
		Plan:        workrun.Plan{Repository: "example/project", Reference: "example", Ticket: "01-example.md", GitHubProfile: "excluded-account", SigningProfile: "excluded-signer", PublicationIdentity: &workrun.PublicationIdentity{GitHubVolume: "excluded-cache", SSHPublicKey: "excluded-public-key"}, CheckInputs: []workrun.Input{{Path: ".env", SHA256: strings.Repeat("b", 64)}}, Roles: workrun.DefaultModels().Codex},
		Evidence:    workrun.CheckEvidence{Tree: strings.Repeat("c", 40), Passed: true, Commands: [][]string{{"dotnet", "test"}}, Log: "excluded-log-path"},
		Publication: workrun.Publication{URL: "https://github.com/example/project/pull/1", Number: 1}, CI: workrun.CIResult{Status: "passed", Details: "excluded-ci-raw-output"},
		Outcome: workrun.Outcome{Summary: "Checks and independent review passed.", LocalReview: true, PRBody: "excluded-pr-body", Questions: []string{"excluded-answered-question"}},
	}
	return View{ID: id, Available: true, Journal: &j}
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
