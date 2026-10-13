package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiagnoseCommandOfflineRedactionAndNoMutation(t *testing.T) {
	j, marker := attentionFixture(t)
	const excluded = "excluded-private-fixture"
	j.StopReason = excluded
	j.Feedback = excluded
	j.Instructions = excluded
	j.Plan.Checks = [][]string{{excluded}}
	j.Outcome.Summary = excluded
	j.Outcome.Questions = []string{excluded}
	j.Plan.GitHubProfile = excluded
	j.CI.Details = excluded
	directory := filepath.Dir(j.Workspace)
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, directory)
	var output bytes.Buffer
	if err := diagnoseCommand(context.Background(), []string{"--run", j.ID[:8], "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), excluded) || strings.Contains(output.String(), j.Plan.Root) || strings.Contains(output.String(), "commands") || strings.Contains(output.String(), "stop_reason") {
		t.Fatal("diagnose exposed private checkpoint data", output.String())
	}
	var snapshot runstatus.Diagnostic
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "waiting_for_human" || snapshot.ReviewQuestions != 1 || snapshot.NextAction.Command != "sdlc answer --run "+j.ID {
		t.Fatal("saved state or human recovery action missing")
	}
	if !reflect.DeepEqual(before, dashboardTree(t, directory)) {
		t.Fatal("diagnose changed saved state")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline diagnose reached account/native/runtime boundary")
	}
}

func TestRunAttentionUsesSharedNumericAndUnknownRecoveryAction(t *testing.T) {
	j, _ := overrideResumeFixture(t, false)
	var output bytes.Buffer
	if err := printRunAttention(&output, j); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sdlc resume --run "+j.ID+" --override-run-limits") || !strings.Contains(output.String(), "sdlc diagnose --run "+j.ID+" --json") {
		t.Fatal("stopped run omitted next action or dump", output.String())
	}
	output.Reset()
	j.StopReason = "excluded-private-unknown-failure"
	if err := printRunAttention(&output, j); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "excluded") || strings.Contains(output.String(), "--override-run-limits") || !strings.Contains(output.String(), "Inspect this saved-state dump") {
		t.Fatal("unknown stop was guessed or exposed")
	}
}
