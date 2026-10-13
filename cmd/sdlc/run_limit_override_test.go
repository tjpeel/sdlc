package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func overrideResumeFixture(t *testing.T, persisted bool) (workrun.Journal, string) {
	t.Helper()
	j, marker := attentionFixture(t)
	j.State, j.ResumeState = "blocked", "repairing"
	j.StopReason = "implementation reached the configured session-turn bound"
	j.Attempt = 24
	j.SessionID = "00000000-0000-4000-8000-000000000001"
	j.Outcome.Questions = nil
	if persisted {
		j.RunLimitOverride = &workrun.RunLimitOverride{GrantedAt: time.Now().UTC(), CheckpointUpdatedAt: j.UpdatedAt, GrantedAttempt: 24, OriginalSessionID: j.SessionID, LimitKind: "native_turns", OriginalLimit: 24}
	}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	t.Chdir(j.Plan.Root)
	return j, marker
}

func decodeOverridePreview(t *testing.T, output []byte) map[string]json.RawMessage {
	t.Helper()
	start := bytes.IndexByte(output, '{')
	if start < 0 {
		t.Fatalf("missing preview: %s", output)
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(output[start:], &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRunLimitOverrideRequiresSingleResumeWithoutOtherRecovery(t *testing.T) {
	for _, args := range [][]string{
		runArgs("--override-run-limits"),
		runArgs("--resume", "example", "--override-run-limits=false"),
		runArgs("--resume", "example", "--override-run-limits", "--answer-file", "answer.txt"),
		runArgs("--resume", "example", "--override-run-limits", "--pr-feedback"),
		runArgs("--resume", "example", "--override-run-limits", "--extend-session-turn-budget", "1"),
		runArgs("--resume", "example", "--override-run-limits", "--initialize-ci-repair-budget"),
		runArgs("--resume", "example", "--override-run-limits", "--initialize-review-repair-budget", "1"),
		runArgs("--resume", "example", "--override-run-limits", "--model", "replacement"),
	} {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("invalid override accepted: %v", args)
		}
	}
	for _, args := range [][]string{
		{"--run", "example", "--override-run-limits", "--pr-feedback"},
		{"--run", "example", "--override-run-limits", "--extend-session-turn-budget", "1"},
		{"--run", "example", "--override-run-limits", "--timeout", "30s"},
	} {
		if err := resumeCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid short override accepted: %v", args)
		}
	}
}

func TestRunLimitOverrideOfflinePreviewAndDeadlines(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		for _, scenario := range []string{"implicit", "explicit", "parent"} {
			t.Run(fmt.Sprintf("persisted=%t/%s", persisted, scenario), func(t *testing.T) {
				j, marker := overrideResumeFixture(t, persisted)
				before := dashboardTree(t, filepath.Dir(j.Workspace))
				ctx := context.Background()
				if scenario == "parent" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Hour)
					defer cancel()
				}
				short := []string{"--run", j.ID[:6], "--dry-run"}
				long := runArgs("--resume", j.ID, "--dry-run", "--json")
				if !persisted {
					short = append(short, "--override-run-limits")
					long = append(long, "--override-run-limits")
				}
				if scenario == "explicit" {
					short = append(short, "--timeout", "1m")
					long = append(long, "--timeout", "1m")
				}
				for i, args := range [][]string{short, long} {
					var output bytes.Buffer
					var err error
					if i == 0 {
						err = resumeCommand(ctx, args, &output)
					} else {
						err = runCommand(ctx, args, &output)
					}
					if err != nil {
						t.Fatal(err)
					}
					plan := decodeOverridePreview(t, output.Bytes())
					if string(plan["execution_has_deadline"]) != fmt.Sprint(scenario != "implicit") || string(plan["offline"]) != "true" {
						t.Fatalf("incorrect deadline or offline status: %s", output.Bytes())
					}
					key := "override_run_limits"
					if persisted {
						key = "run_limits_overridden"
					}
					if string(plan[key]) != "true" {
						t.Fatalf("override selection missing: %s", output.Bytes())
					}
				}
				after := dashboardTree(t, filepath.Dir(j.Workspace))
				if len(before) != len(after) {
					t.Fatal("preview changed run files")
				}
				for path, state := range before {
					if after[path] != state {
						t.Fatalf("preview changed %s", path)
					}
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("preview ran a connected command")
				}
			})
		}
	}
}

func TestPersistedRunLimitOverridePreviewPreservesAnswer(t *testing.T) {
	j, _ := overrideResumeFixture(t, true)
	j.State, j.ResumeState = "waiting_for_human", ""
	j.Outcome.Questions = []string{"Which format should the report use?"}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	const answer = "Use the documented format."
	path := filepath.Join(t.TempDir(), "answer.txt")
	if err := os.WriteFile(path, []byte(answer), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runCommand(context.Background(), runArgs("--resume", j.ID, "--answer-file", path, "--dry-run", "--json"), &output); err != nil {
		t.Fatal(err)
	}
	plan := decodeOverridePreview(t, output.Bytes())
	want := fmt.Sprintf("\"%x\"", sha256.Sum256([]byte(answer)))
	if string(plan["answer_sha256"]) != want || bytes.Contains(output.Bytes(), []byte(answer)) {
		t.Fatalf("preview lost answer binding or exposed text: %s", output.Bytes())
	}
}

func TestPersistedRunLimitOverridePreviewPreservesFeedback(t *testing.T) {
	j, _ := readyPRFeedbackFixture(t)
	j.Attempt = 24
	j.SessionID = "00000000-0000-4000-8000-000000000001"
	j.RunLimitOverride = &workrun.RunLimitOverride{GrantedAt: time.Now().UTC(), CheckpointUpdatedAt: j.UpdatedAt, GrantedAttempt: 24, OriginalSessionID: j.SessionID, LimitKind: "native_turns", OriginalLimit: 24}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--pr-feedback", "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	plan := decodeOverridePreview(t, output.Bytes())
	if string(plan["pr_feedback"]) != "true" || string(plan["run_limits_overridden"]) != "true" {
		t.Fatalf("preview lost feedback action: %s", output.Bytes())
	}
}
