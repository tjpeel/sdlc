package runtimeupdates

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func summaryReport() Report {
	report := Report{PackageCount: 300, Results: []Result{
		{Name: "Displayed Codex", Kind: "npm", Source: "@openai/codex", Installed: "1.0.0", Candidate: "1.1.0", Status: Update},
		{Name: "Claude Code", Kind: "npm", Source: "@anthropic-ai/claude-code", Installed: "2.0.0", Candidate: "2.0.0", Status: Current},
		{Name: "npm", Kind: "npm", Source: "npm", Installed: "11.0.0", Candidate: "11.0.0", Status: Current},
		{Name: "yarn", Kind: "npm", Source: "yarn", Installed: "1.22.0", Candidate: "1.22.0", Status: Current},
		{Name: "Node.js", Kind: "node", Source: "nodejs", Installed: "24.1.0", Candidate: "24.2.0", Track: "24", Status: Update},
		{Name: "GitHub CLI", Kind: "github-release", Source: "cli/cli", Installed: "2.0.0", Candidate: "2.0.0", Status: Current},
		{Name: "Skills catalogue", Kind: "git", Source: "tjpeel/skills", Installed: strings.Repeat("a", 40), Candidate: strings.Repeat("a", 40), Track: "main", Status: Current},
		{Name: "Node exact base image", Kind: "image", Source: "library/node", Installed: "node:24.1.0-bookworm@sha256:" + strings.Repeat("a", 64), Candidate: "node:24.2.0-bookworm@sha256:" + strings.Repeat("b", 64), Status: Update},
	}}
	for i := 0; i < 50; i++ {
		report.Results = append(report.Results, Result{Name: fmt.Sprintf("Child %d", i), Kind: "npm", Source: fmt.Sprintf("public-child-%d", i), Installed: "1.0.0", Candidate: "2.0.0", Status: Update})
	}
	report.Results = append(report.Results, Result{Name: "@openai/codex-linux-arm64", Kind: "npm", Source: "@openai/codex", Track: "linux-arm64", Installed: "1.0.0-linux-arm64", Candidate: "1.1.0-linux-arm64", Status: Update})
	for i := 0; i < 300; i++ {
		report.Packages = append(report.Packages, runtimeimage.PackageUpdate{Name: fmt.Sprintf("public-package-%d", i), Architecture: "arm64", Installed: "1.0", Candidate: "2.0", Update: true})
	}
	return report
}

func TestSummaryShowsMajorToolsAndCountsBundledAndDebianPackages(t *testing.T) {
	report := summaryReport()
	var concise, full bytes.Buffer
	if err := report.PrintSummary(&concise); err != nil {
		t.Fatal(err)
	}
	if err := report.Print(&full); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Displayed Codex", "Claude Code", "npm", "yarn", "Node.js", "GitHub CLI", "Skills catalogue", "Node exact base image"} {
		if !strings.Contains(concise.String(), name) {
			t.Fatal("primary tool hidden", name, concise.String())
		}
	}
	for _, fragment := range []string{"Parent-managed npm packages: 51 checked; 51 newer upstream versions; 0 unavailable", "Debian packages: 300 checked; 300 updates; 0 unavailable", "Published parents control their installed versions", "status --all"} {
		if !strings.Contains(concise.String(), fragment) {
			t.Fatal("summary missing", fragment, concise.String())
		}
	}
	if strings.Contains(concise.String(), "Child 0") || strings.Contains(concise.String(), "public-package-0") || strings.Contains(concise.String(), "codex-linux-arm64") || strings.Count(concise.String(), "\n") > 20 {
		t.Fatal("summary flooded with package rows", concise.String())
	}
	if !strings.Contains(full.String(), "Child 49") || !strings.Contains(full.String(), "Debian public-package-299:arm64") || !strings.Contains(full.String(), "@openai/codex-linux-arm64") {
		t.Fatal("complete report lost package detail", full.String())
	}
	if strings.Contains(concise.String(), strings.Repeat("a", 64)) || !strings.Contains(full.String(), strings.Repeat("a", 64)) {
		t.Fatal("summary/full image identity formatting changed")
	}
}

func TestSummaryKeepsHiddenMetadataFailureMeaningVisible(t *testing.T) {
	report := summaryReport()
	for i := 8; i < 18; i++ {
		report.Results[i].Status = Unavailable
		report.Results[i].Detail = "metadata returned HTTP 429"
	}
	report.Packages[0].Candidate = ""
	var output bytes.Buffer
	if err := report.PrintSummary(&output); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"10 unavailable", "Parent-managed npm metadata unavailable (10)", "Child 0 (metadata returned HTTP 429)", "5 more (use --all)", "Debian candidates unavailable (1): public-package-0:arm64", "Dependency check incomplete: 10 component checks and 1 package checks unavailable"} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatal("hidden metadata failure lost", fragment, output.String())
		}
	}
	if strings.Contains(output.String(), "No direct tool") {
		t.Fatal("incomplete metadata reported current")
	}
	report.PackageError = "Debian metadata refresh failed or timed out"
	output.Reset()
	if err := report.PrintSummary(&output); err != nil || !strings.Contains(output.String(), report.PackageError) || !strings.Contains(output.String(), "300 package checks unavailable") {
		t.Fatal("Debian failure hidden", err, output.String())
	}
}

func TestSummaryOfflineAndOnlyParentUpdatesDoNotImplyDirectUpdates(t *testing.T) {
	report := summaryReport()
	for i := range report.Results {
		if primaryResult(report.Results[i]) {
			report.Results[i].Status = Current
		}
	}
	for i := range report.Packages {
		report.Packages[i].Update = false
	}
	var output bytes.Buffer
	if err := report.PrintSummary(&output); err != nil || !strings.Contains(output.String(), "No direct tool, image or Debian updates found") || !strings.Contains(output.String(), "51 newer upstream versions") {
		t.Fatal(err, output.String())
	}
	report.Offline = true
	output.Reset()
	if err := report.PrintSummary(&output); err != nil || !strings.Contains(output.String(), "Parent-managed npm packages: 51 installed; upstream checks skipped") || !strings.Contains(output.String(), "300 installed; upstream checks skipped (--offline)") || strings.Contains(output.String(), "No direct tool") {
		t.Fatal(err, output.String())
	}
}

func TestPlanPrintGroupsDebianActionsAndParentDifferences(t *testing.T) {
	plan := Plan{Report: summaryReport()}
	plan.Changes = append(plan.Changes, Change{Dependency: "Codex CLI", Kind: "npm", Installed: "1.0.0", Candidate: "1.1.0", Action: "pin in private build recipe"})
	for i := 0; i < 300; i++ {
		plan.Changes = append(plan.Changes, Change{Dependency: fmt.Sprintf("Debian public-package-%d:arm64", i), Kind: "debian", Installed: "1.0", Candidate: "2.0", Action: "refresh Debian packages during rebuild"})
	}
	plan.Remaining = plan.Report.Results[8:]
	var output bytes.Buffer
	if err := plan.Print(&output); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"Planned build actions: 301", "Codex CLI: 1.0.0 -> 1.1.0", "Debian rebuild: refresh 300 packages", "Remaining parent-managed npm differences: 51; keep versions selected by their published parents"} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatal("plan summary missing", fragment, output.String())
		}
	}
	if strings.Contains(output.String(), "Child 0") || strings.Contains(output.String(), "Debian public-package-0") || strings.Count(output.String(), "\n") > 25 {
		t.Fatal("plan expanded package actions", output.String())
	}
}

type failedSummaryWriter struct{}

func (failedSummaryWriter) Write([]byte) (int, error) {
	return 0, errors.New("disposable output failure")
}
func TestSummaryPropagatesOutputFailure(t *testing.T) {
	if err := summaryReport().PrintSummary(failedSummaryWriter{}); err == nil {
		t.Fatal("summary output failure ignored")
	}
}
