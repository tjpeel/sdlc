package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestUsageReportsDurableMetricsReadOnly(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	j := journals[0]
	observer := runusage.NewObserver("codex", j.Plan.Roles.Implementation.Name)
	observer.Write([]byte(`{"type":"thread.started","thread_id":"00000000-0000-4000-8000-000000000099"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}` + "\n"))
	started := time.Now().UTC().Add(-time.Minute)
	if err := runusage.SaveAttempt(filepath.Dir(j.Workspace), runusage.Attempt{
		Version: runusage.Version, RunID: j.ID, Attempt: 1, Role: "implementation",
		Provider: "codex", Model: j.Plan.Roles.Implementation.Name, OptimizerMode: "off",
		StartedAt: started, EndedAt: started.Add(time.Second), Outcome: "completed",
		SessionID: observer.SessionID(), Usage: observer.Snapshot(),
	}); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := usageCommand(context.Background(), []string{"--json", "--run", j.ID}, &output); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Runs []usageRow `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Runs) != 1 || document.Runs[0].Metrics == nil || document.Runs[0].Metrics.Groups[0].Tokens.InputTokens == nil || *document.Runs[0].Metrics.Groups[0].Tokens.InputTokens != 100 {
		t.Fatalf("durable totals missing: %s", output.String())
	}
	for _, private := range []string{observer.SessionID(), j.Instructions, "private-recent-event", "session_id", "prompt", "workspace"} {
		if strings.Contains(output.String(), private) {
			t.Fatalf("private material %q exposed", private)
		}
	}
	output.Reset()
	if err := usageCommand(context.Background(), []string{"--run", j.ID}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "input 100") || !strings.Contains(output.String(), "capacity: unknown") {
		t.Fatalf("unknown capacity or counters missing: %s", output.String())
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestUsageMissingMetricsAndSelection(t *testing.T) {
	root, marker, _ := dashboardFixture(t)
	before := dashboardTree(t, root)
	var output bytes.Buffer
	if err := usageCommand(context.Background(), []string{"--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Runs []usageRow `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Runs) != 2 {
		t.Fatal("installation usage lost a repository")
	}
	for _, row := range document.Runs {
		if row.Metrics == nil || row.Metrics.Missing != 1 || row.Metrics.Attempts != 0 || row.Timings != nil {
			t.Fatalf("old run was reported as measured: %+v", row)
		}
	}
	for _, args := range [][]string{{"--since", "0d"}, {"--since", "366d"}, {"--scope", "account"}, {"--run", "12"}, {"extra"}} {
		if err := usageCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("unsupported selection accepted: %v", args)
		}
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestUsagePresentationKeepsUnknownMeasurementsAndCapacitySeparate(t *testing.T) {
	measured := int64(42)
	var output bytes.Buffer
	if err := writeUsageRows(&output, []usageRow{{RunID: "example", State: "stopped", Metrics: &runusage.Summary{
		Attempts: 1, Missing: 2, Incomplete: 1, UnknownBaseline: 1,
		Groups: []runusage.Group{{Provider: "codex", Tokens: runusage.TokenUsage{InputTokens: &measured}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "input 42") || !strings.Contains(text, "output unknown") || !strings.Contains(text, "capacity: unknown") || !strings.Contains(text, "Incomplete 1; missing 2; unknown resume baseline 1") {
		t.Fatalf("recorded values or gaps lost: %s", text)
	}
	if strings.Index(text, "input 42") > strings.Index(text, "== Gaps and capacity observations ==") || strings.Index(text, "capacity: unknown") < strings.Index(text, "== Gaps and capacity observations ==") {
		t.Fatal("capacity mixed with recorded token measurements")
	}
	if !strings.Contains(text, "Repair/check outcomes: unrecorded") || strings.Contains(text, "checks passed false") {
		t.Fatal("unrecorded outcome presented as a measured failure")
	}
}

func TestUsageJournalWithoutCheckEvidenceDoesNotClaimFailedChecks(t *testing.T) {
	_, _, journals := dashboardFixture(t)
	j := journals[0]
	evidence := j.Evidence
	j.Evidence = workrun.CheckEvidence{}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := usageCommand(context.Background(), []string{"--run", j.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "checks passed false") || !strings.Contains(out.String(), "Check result: not run") {
		t.Fatalf("unexecuted checks reported as failed: %s", out.String())
	}
	for _, passed := range []bool{false, true} {
		j.Evidence = evidence
		j.Evidence.Passed = passed
		if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := usageCommand(context.Background(), []string{"--run", j.ID}, &out); err != nil {
			t.Fatal(err)
		}
		result := "failed"
		if passed {
			result = "passed"
		}
		if !strings.Contains(out.String(), "Check result: "+result) || strings.Contains(out.String(), "Check result: not run") {
			t.Fatalf("recorded result lost: %s", out.String())
		}
	}
}
