package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestDashboardJSONOmitsAutomaticPrivateCIFailureEvidence(t *testing.T) {
	private := &workrun.CIFailureContext{Head: strings.Repeat("b", 40), Checks: []workrun.CIFailedCheck{{Kind: "check", Name: "offline suite", CheckID: 41, AppID: 90, AppSlug: "example-checks", Conclusion: "failure", Text: "private-ci-failure-sentinel"}}}
	j := workrun.Journal{CI: workrun.CIResult{Status: "failed", FailureContext: private}}
	v := runstatus.View{ID: strings.Repeat("a", 24), Available: true, State: "blocked", CI: j.CI, Journal: &j}
	var out bytes.Buffer
	if err := writeDashboardJSON(&out, []runstatus.View{v}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private-ci-failure-sentinel") || strings.Contains(out.String(), "failure_context") || !strings.Contains(out.String(), "ci_status") {
		t.Fatal("dashboard JSON forwarded private CI evidence")
	}
}

func TestRunCIFeedbackRequiresSingleUncombinedResume(t *testing.T) {
	for _, args := range [][]string{runArgs("--ci-feedback-file", "context.txt"), runArgs("--resume", "abc", "--ci-feedback-file", ""), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--answer-file", "answer.txt"), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--pr-feedback"), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--override-run-limits"), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--initialize-ci-repair-budget"), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--initialize-review-repair-budget", "1"), runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--extend-session-turn-budget", "1")} {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatal("invalid combined context recovery accepted", args)
		}
	}
	options, err := parseRunOptions(runArgs("--resume", "abc", "--ci-feedback-file", "context.txt", "--dry-run"))
	if err != nil || options.ciFeedbackFile != "context.txt" {
		t.Fatal(options, err)
	}
	for _, args := range [][]string{{"--run", "abc", "--ci-feedback-file", ""}, {"--run", "abc", "--ci-feedback-file", "context.txt", "--override-run-limits"}, {"--run", "abc", "--ci-feedback-file", "context.txt", "--pr-feedback"}, {"--run", "abc", "--ci-feedback-file", "context.txt", "--extend-session-turn-budget", "1"}} {
		if err := resumeCommand(context.Background(), args, io.Discard); err == nil {
			t.Fatal("combined resume recovery accepted", args)
		}
	}
}

func TestResumeCIFeedbackOfflinePreviewBindsCheckpointAndKeepsTextPrivate(t *testing.T) {
	j, marker := attentionFixture(t)
	dir := filepath.Dir(j.Workspace)
	j.State, j.ResumeState, j.SessionID = "blocked", "implementing", "original-native-session"
	j.Outcome = workrun.Outcome{Status: "blocked", Summary: "CI repair needs failing step context."}
	j.StopReason = j.Outcome.Summary
	j.Attempt, j.Rounds = 12, 26
	j.Plan.Repository = "example/project"
	j.Publication = workrun.Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("b", 40)}
	j.CI = workrun.CIResult{Status: "failed"}
	j.CIRepairBudget = &workrun.CIRepairBudget{Used: 2, StartRound: 24, LastGrantedHead: j.Publication.HeadSHA}
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context.txt")
	text := "Verified offline startup type/key/location facts; literal `$(touch example)`."
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, dir)
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID[:6], "--ci-feedback-file", path, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ci_feedback_sha256") || !strings.Contains(out.String(), j.Publication.HeadSHA) || !strings.Contains(out.String(), j.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")) || strings.Contains(out.String(), text) {
		t.Fatal(out.String())
	}
	after := dashboardTree(t, dir)
	if len(before) != len(after) {
		t.Fatal("offline preview changed job files")
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatal("offline preview wrote private state", path)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline preview invoked connected boundary")
	}
	lock, err := filelock.Acquire(filepath.Join(dir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--ci-feedback-file", path, "--dry-run"}, io.Discard); err == nil {
		t.Fatal("active controller admitted feedback")
	}
}
