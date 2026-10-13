package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func readyPRFeedbackFixture(t *testing.T) (workrun.Journal, string) {
	t.Helper()
	j, marker := attentionFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State, j.PendingRole = "ready", ""
	j.SessionID = "00000000-0000-4000-8000-000000000001"
	j.Plan.Repository = "example/project"
	j.Publication = workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: strings.Repeat("c", 40), HeadSHA: strings.Repeat("d", 40)}
	j.CI.Status = "passed"
	j.Evidence.Passed = true
	j.Outcome = workrun.Outcome{Status: "reviewed"}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	return j, marker
}

func TestPRFeedbackResumePreviewRecoversSavedProjectWithoutMutation(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	t.Chdir(t.TempDir())
	before := dashboardTree(t, dir)
	commands := [][]string{
		{"--run", j.ID[:6], "--pr-feedback", "--dry-run"},
		append(runArgs("--resume", j.ID, "--pr-feedback", "--dry-run", "--json"), "--run-root", j.Plan.Root),
	}
	for i, args := range commands {
		var out bytes.Buffer
		var err error
		if i == 0 {
			err = resumeCommand(context.Background(), args, &out)
		} else {
			err = runCommand(context.Background(), args, &out)
		}
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(out.String(), "{")
		if start < 0 {
			t.Fatal("missing offline plan", out.String())
		}
		var plan struct {
			Offline  bool         `json:"offline"`
			Feedback bool         `json:"pr_feedback"`
			RunID    string       `json:"run_id"`
			Plan     workrun.Plan `json:"plan"`
		}
		if err := json.Unmarshal(out.Bytes()[start:], &plan); err != nil || !plan.Offline || !plan.Feedback || plan.RunID != j.ID || plan.Plan.Root != j.Plan.Root || plan.Plan.Roles != j.Plan.Roles {
			t.Fatalf("saved feedback selection lost: %+v %v", plan, err)
		}
	}
	after := dashboardTree(t, dir)
	if len(before) != len(after) {
		t.Fatal("preview wrote run state")
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatal("preview changed run state", path)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview invoked a connected command")
	}
	if _, err := inspectRunAction(context.Background(), j.ID); err == nil {
		t.Fatal("ordinary resume silently selected human feedback")
	}
}

func TestPRFeedbackFetchFailureCanResumeThroughPublicCommand(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State, j.ResumeState, j.StopReason = "blocked", "pr_feedback", "feedback read unavailable"
	j.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, FetchPending: true, FetchResumeState: "ready", CandidateStart: j.Plan.StartingSHA}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID[:6], "--pr-feedback", "--dry-run"}, &out); err != nil {
		t.Fatal("documented fetch recovery rejected", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil || !bytes.Equal(before, after) || !strings.Contains(out.String(), `"pr_feedback":true`) {
		t.Fatal("fetch recovery preview changed state or lost feedback action", err, out.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("fetch recovery preview invoked a connected command")
	}
}

func TestPRFeedbackRequiresSavedRunWithoutBudgetOrAnswerChanges(t *testing.T) {
	for _, extra := range [][]string{
		{"--pr-feedback"},
		{"--resume", "abc", "--pr-feedback=false"},
		{"--resume", "abc", "--pr-feedback", "--answer-file", "answer.txt"},
		{"--resume", "abc", "--pr-feedback", "--extend-session-turn-budget", "1"},
		{"--resume", "abc", "--pr-feedback", "--initialize-ci-repair-budget"},
		{"--resume", "abc", "--pr-feedback", "--initialize-review-repair-budget", "1"},
		{"--resume", "abc", "--pr-feedback", "--model", "changed"},
	} {
		if _, err := parseRunOptions(runArgs(extra...)); err == nil {
			t.Fatalf("invalid feedback action accepted: %v", extra)
		}
	}
	options, err := parseRunOptions(runArgs("--resume", "abc", "--pr-feedback", "--dry-run"))
	if err != nil || !options.processPRFeedback {
		t.Fatal("feedback action not passed to controller", err)
	}
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", "abc", "--pr-feedback", "--extend-session-turn-budget", "1", "--dry-run"}, &out); err == nil || !strings.Contains(err.Error(), "cannot grant") {
		t.Fatal("short action accepted budget extension", err)
	}
}

func TestPRFeedbackInterruptedFetchPreviewIsResumable(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State = "pr_feedback"
	j.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, FetchPending: true, FetchResumeState: "ready", CandidateStart: j.Plan.StartingSHA}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--pr-feedback", "--dry-run"}, &out); err != nil {
		t.Fatal("interrupted fetch checkpoint cannot be resumed", err)
	}
	if !strings.Contains(out.String(), `"pr_feedback":true`) {
		t.Fatal("interrupted fetch lost its explicit action", out.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("interrupted fetch preview invoked a connected command")
	}
}
