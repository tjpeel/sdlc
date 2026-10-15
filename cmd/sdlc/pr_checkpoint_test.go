package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestPRStartOptionsRequireExplicitEntryWithoutOtherRecovery(t *testing.T) {
	for _, mode := range []string{"awaiting-feedback", "checks", "review", "feedback"} {
		o, err := parseRunOptions([]string{"--pr", "https://github.com/example/project/pull/1", "--start-at", mode, "--human-reviewed", "--dry-run"})
		if err != nil || o.startAt != mode || !o.humanReviewed {
			t.Fatal(o, err)
		}
	}
	for _, args := range [][]string{
		{"--pr", "https://github.com/example/project/pull/1", "--human-reviewed"},
		runArgs("--start-at", "awaiting-feedback"),
		runArgs("--resume", "run-id", "--start-at", "ready"),
		runArgs("--resume", "run-id", "--start-at", "awaiting-feedback", "--pr-feedback"),
		runArgs("--resume", "run-id", "--start-at", "awaiting-feedback", "--override-run-limits"),
		runArgs("--resume", "run-id", "--start-at", "awaiting-feedback", "--answer-file", "answer.txt"),
	} {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatal("invalid checkpoint selection accepted", args)
		}
	}
}

func TestPRCheckpointResumePreviewDoesNotMutateOrDispatch(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State, j.ResumeState, j.StopReason = "blocked", "implementing", "claimed feedback commit is absent from candidate history"
	j.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: j.Publication.HeadSHA}
	j.Outcome = workrun.Outcome{Status: "checks_requested"}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--start-at", "awaiting-feedback", "--human-reviewed", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Action   string `json:"action"`
		Stage    string `json:"start_at"`
		Reviewed bool   `json:"human_reviewed"`
		Offline  bool   `json:"offline"`
	}
	if err := json.Unmarshal(out.Bytes()[bytes.IndexByte(out.Bytes(), '{'):], &plan); err != nil || plan.Action != "pr_checkpoint" || plan.Stage != "awaiting-feedback" || !plan.Reviewed || !plan.Offline {
		t.Fatal(plan, err, out.String())
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed journal")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview invoked runtime")
	}
}

func TestParkedPRFeedbackPreviewAcceptsMissingImplementationSession(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State = "awaiting_feedback"
	j.SessionID = ""
	j.Evidence = workrun.CheckEvidence{}
	j.CI = workrun.CIResult{}
	j.Outcome = workrun.Outcome{}
	j.PRCheckpoints = []workrun.PRCheckpoint{{StartAt: "awaiting-feedback", Head: j.Publication.HeadSHA, Tree: strings.Repeat("e", 40), RequestedAt: time.Now().UTC(), PreviousState: "adopting", Archive: "pr-checkpoints/fixture.json", Completed: true}}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, j); err != nil {
		t.Fatal(err)
	}
	for _, fileFeedback := range []bool{false, true} {
		var out bytes.Buffer
		var err error
		if fileFeedback {
			err = feedbackCommand(context.Background(), []string{"--run", j.ID, "--stdin", "--dry-run"}, strings.NewReader("New private feedback"), &out)
		} else {
			err = resumeCommand(context.Background(), []string{"--run", j.ID, "--pr-feedback", "--dry-run"}, &out)
		}
		if err != nil {
			t.Fatal("parked feedback command rejected", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview dispatched work")
	}
}
