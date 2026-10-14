package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
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

func stableOverrideFixture(t *testing.T) (workrun.Journal, string) {
	t.Helper()
	j, marker, _ := capturedAttentionFixture(t)
	ctx := context.Background()
	directory := filepath.Dir(j.Workspace)
	git := func(args ...string) string {
		t.Helper()
		output, err := workrun.SafeGit(ctx, j.Workspace, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output)
	}
	git("-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "--allow-empty", "-qm", "Create disposable candidate")
	head := git("rev-parse", "HEAD")
	tree := git("rev-parse", "HEAD^{tree}")
	signed := git("-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit-tree", tree, "-p", head, "-m", "Create disposable published boundary")
	j.Plan.Repository = "example/project"
	j.Plan.PublicationIdentity = &workrun.PublicationIdentity{GitHubProfile: "example", RepositoryID: 1, RepositoryName: "example/project", ProfileID: "example", GitHubID: 1, GitHubLogin: "example", GitName: "Example User", GitEmail: "example@example.invalid", SSHPublicKey: "ssh-ed25519 AAAA", SSHFingerprint: "SHA256:example"}
	published := filepath.Join(directory, "publisher", "publication.git")
	if err := os.MkdirAll(filepath.Dir(published), 0700); err != nil {
		t.Fatal(err)
	}
	git("init", "--bare", "--template=", published)
	git("--git-dir="+published, "fetch", "--no-tags", j.Workspace, signed+":refs/heads/publication")
	j.State, j.ResumeState, j.StopReason, j.PendingRole = "ready", "", "", ""
	j.Attempt = 6
	j.Rounds = 2
	j.SessionID = "00000000-0000-4000-8000-000000000001"
	j.Outcome = workrun.Outcome{Status: "reviewed"}
	j.CI = workrun.CIResult{Status: "passed"}
	j.Publication = workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: j.Plan.StartingSHA, HeadSHA: signed}
	// Build the saved controller key for this disposable ready fixture.
	data, err := json.Marshal(struct {
		Request           workrun.VerificationRequest
		Head, Tree, Image string
		Inputs            []workrun.Input
		Commands          [][]string
		Docker            bool
		Daemon            string
		DaemonMode        string `json:",omitempty"`
	}{workrun.VerificationRequest{}, head, tree, j.ImageID, j.Plan.CheckInputs, j.Plan.Checks, j.Plan.DockerTests, j.Plan.DaemonImage, j.Plan.DaemonMode})
	if err != nil {
		t.Fatal(err)
	}
	j.Evidence = workrun.CheckEvidence{Passed: true, Head: head, Tree: tree, Key: fmt.Sprintf("%x", sha256.Sum256(data))}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	return j, marker
}

func TestStableRunOverrideCLIRecordsPermissionWithoutStartingWork(t *testing.T) {
	j, marker := stableOverrideFixture(t)
	directory := filepath.Dir(j.Workspace)
	before := dashboardTree(t, directory)
	var output bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--override-run-limits", "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dashboardTree(t, directory)) {
		t.Fatal("ready preview changed saved run")
	}
	plan := decodeOverridePreview(t, output.Bytes())
	if string(plan["state"]) != `"ready"` || string(plan["override_run_limits"]) != "true" || string(plan["pr_feedback"]) == "true" {
		t.Fatal("preview did not retain ready-only action")
	}
	output.Reset()
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--override-run-limits"}, &output); err != nil {
		t.Fatal("ready opt-in failed", err)
	}
	saved, err := workrun.Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RunLimitOverride == nil || saved.RunLimitOverride.LimitKind != "stable_run" || saved.RunLimitOverride.GrantedAttempt != 6 || saved.RunLimitOverride.StableReady.Publication != j.Publication || saved.RunLimitOverride.StableReady.Head == j.Publication.HeadSHA {
		t.Fatal("stable ready audit missing or signed mapping changed")
	}
	audit := *saved.RunLimitOverride
	expected := j
	expected.RunLimitOverride = saved.RunLimitOverride
	expected.UpdatedAt = saved.UpdatedAt
	if !reflect.DeepEqual(expected, saved) {
		t.Fatal("ready opt-in changed counters, evidence, session or checkpoint state")
	}
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--override-run-limits"}, &output); err != nil {
		t.Fatal("idempotent ready opt-in failed", err)
	}
	again, err := workrun.Load(directory)
	if err != nil || !reflect.DeepEqual(audit, *again.RunLimitOverride) || !again.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatal("ready retry renewed grant", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ready grant reached native, Docker or GitHub commands")
	}
	output.Reset()
	if err := resumeCommand(context.Background(), []string{"--run", j.ID, "--pr-feedback", "--dry-run"}, &output); err != nil {
		t.Fatal("future feedback lost saved override", err)
	}
	plan = decodeOverridePreview(t, output.Bytes())
	if string(plan["pr_feedback"]) != "true" || string(plan["run_limits_overridden"]) != "true" {
		t.Fatal("future feedback did not retain override")
	}
}

func TestStableRunOverrideCLIRejectsUnsafeReadyBoundary(t *testing.T) {
	for _, scenario := range []string{"dirty", "source-head", "evidence-key", "ci", "review", "pending-input", "active-feedback", "ownership", "published-tree", "private-input"} {
		t.Run(scenario, func(t *testing.T) {
			j, _ := stableOverrideFixture(t)
			directory := filepath.Dir(j.Workspace)
			switch scenario {
			case "dirty":
				if err := os.WriteFile(filepath.Join(j.Workspace, "README.md"), []byte("Changed candidate"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source-head":
				j.Evidence.Head = strings.Repeat("a", 40)
			case "evidence-key":
				j.Evidence.Key = strings.Repeat("a", 64)
			case "ci":
				j.CI.Status = "failed"
			case "review":
				j.Outcome.Limitations = []string{"Review incomplete"}
			case "pending-input":
				// An unfinished transaction is rejected by action admission before any grant.
				j.PendingInputs = &workrun.PendingInputRecovery{InputRecovery: workrun.InputRecovery{Scope: "checks", PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: []workrun.Input{{Path: "fixture.txt", SHA256: strings.Repeat("a", 64)}}}, Stage: "input-recovery-pending"}
			case "active-feedback":
				j.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: j.Evidence.Head}
			case "published-tree":
				j.Publication.HeadSHA = strings.Repeat("b", 40)
			case "private-input":
				if err := os.WriteFile(filepath.Join(directory, "check-inputs", j.Plan.CheckInputs[0].Path), []byte("Changed private fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := workrun.Save(directory, &j); err != nil {
				t.Fatal(err)
			}
			if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
				t.Fatal(err)
			}
			var lock *os.File
			if scenario == "ownership" {
				var err error
				lock, err = filelock.Acquire(filepath.Join(directory, "run.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			before, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
			var output bytes.Buffer
			err := resumeCommand(context.Background(), []string{"--run", j.ID, "--override-run-limits"}, &output)
			if err == nil {
				t.Fatal("unsafe ready grant admitted")
			}
			if scenario == "ownership" && !errors.Is(err, filelock.ErrBusy) {
				t.Fatal("active ownership not enforced", err)
			}
			after, _ := os.ReadFile(filepath.Join(directory, "journal.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("rejected grant changed checkpoint")
			}
		})
	}
}

// A saved ordinary run must have the same unlimited invocation lifetime as an
// overridden run. The preview reports the execution context before any provider
// work, so this catches an accidental whole-workflow deadline without waiting.
func TestOrdinaryResumeInvocationDeadlines(t *testing.T) {
	for _, command := range []string{"run", "resume"} {
		for _, scenario := range []string{"implicit", "explicit", "parent"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				j, marker := overrideResumeFixture(t, false)
				ctx := context.Background()
				if scenario == "parent" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 48*time.Hour)
					defer cancel()
				}
				args := runArgs("--resume", j.ID, "--dry-run", "--json")
				if command == "resume" {
					args = []string{"--run", j.ID[:6], "--dry-run"}
				}
				if scenario == "explicit" {
					args = append(args, "--timeout", "1m")
				}
				var output bytes.Buffer
				var err error
				if command == "resume" {
					err = resumeCommand(ctx, args, &output)
				} else {
					err = runCommand(ctx, args, &output)
				}
				if err != nil {
					t.Fatal(err)
				}
				plan := decodeOverridePreview(t, output.Bytes())
				if string(plan["execution_has_deadline"]) != fmt.Sprint(scenario != "implicit") {
					t.Fatalf("unexpected execution deadline: %s", output.Bytes())
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("preview ran connected command")
				}
			})
		}
	}
}

func TestRunCommandsHonorParentCancellation(t *testing.T) {
	for _, command := range []string{"run", "resume", "feature"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%t", command, expired), func(t *testing.T) {
				j, marker := overrideResumeFixture(t, false)
				ctx, cancel := context.WithCancel(context.Background())
				if expired {
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				} else {
					cancel()
				}
				defer cancel()
				args := runArgs("--resume", j.ID, "--dry-run", "--json")
				if command == "feature" {
					args = []string{"--reference", "TASK-1", "--all", "--dry-run"}
				}
				var err error
				if command == "resume" {
					err = resumeCommand(ctx, []string{"--run", j.ID[:6], "--dry-run"}, &bytes.Buffer{})
				} else {
					err = runCommand(ctx, args, &bytes.Buffer{})
				}
				expected := error(context.Canceled)
				if expired {
					expected = context.DeadlineExceeded
				}
				if !errors.Is(err, expected) {
					t.Fatalf("parent cancellation not preserved: %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("canceled controller ran connected command")
				}
			})
		}
	}
}

func TestExplicitRunTimeoutStopsControllerPreparation(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	options, err := parseRunOptions(runArgs("--timeout", "1m", "--dry-run"))
	if err != nil {
		t.Fatal(err)
	}
	// Keep the real command/controller boundary and shorten only the selected
	// duration after parsing, avoiding a minute-long offline regression.
	options.timeout = time.Nanosecond
	err = runSelectedCommand(context.Background(), options, &bytes.Buffer{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("explicit invocation timeout did not stop preparation: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("timed out controller ran connected command")
	}
}
