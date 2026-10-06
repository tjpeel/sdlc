package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func attentionFixture(t *testing.T) (workrun.Journal, string) {
	t.Helper()
	root := runGitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, root)
	id := "abcdef0123456789abcdef01"
	directory, err := workrun.RunDirectory(root, "TASK-1", "01-selected.md", id, true)
	if err != nil {
		t.Fatal(err)
	}
	j := workrun.Journal{Version: 1, ID: id, State: "waiting_for_human", PendingRole: "implementation", Workspace: filepath.Join(directory, "workspace"), Plan: workrun.Plan{Root: root, Reference: "TASK-1", Ticket: ".sdlc/work/TASK-1/tickets/01-selected.md", StartingSHA: strings.Repeat("a", 40), SourceSHA: strings.Repeat("b", 40), Roles: workrun.DefaultModels().Codex}}
	j.Outcome.Questions = []string{"Should missing items return 404?", "Which format should the report use?"}
	if err := workrun.Save(directory, &j); err != nil {
		t.Fatal(err)
	}
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(directory, j); err != nil {
		t.Fatal(err)
	}
	return j, marker
}

func TestAnswerCommandRecoversRecordedRootAndKeepsLiteralTextPrivate(t *testing.T) {
	j, marker := attentionFixture(t)
	t.Chdir(t.TempDir())
	text := "literal `$(touch anything)`\n.\n/cancel\nUnicode: café\n"
	before := dashboardTree(t, filepath.Dir(j.Workspace))
	var output bytes.Buffer
	if err := answerCommand(context.Background(), []string{"--run", j.ID[:6], "--text", text, "--dry-run"}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), j.Outcome.Questions[0]) || !strings.Contains(output.String(), `"answer_sha256"`) || strings.Contains(output.String(), text) {
		t.Fatal(output.String())
	}
	directory := filepath.Dir(j.Workspace)
	after := dashboardTree(t, directory)
	if len(before) != len(after) {
		t.Fatal("dry run wrote an answer")
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatal("dry run changed private state")
		}
	}
	action, err := resolveRunAction(context.Background(), j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveInlineAnswer(action, text); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "answers", "*.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("answer files: %v %v", files, err)
	}
	data, _ := os.ReadFile(files[0])
	if string(data) != text {
		t.Fatal("literal answer changed")
	}
	info, _ := os.Stat(files[0])
	dirInfo, _ := os.Stat(filepath.Dir(files[0]))
	if info.Mode().Perm() != 0600 || dirInfo.Mode().Perm() != 0700 {
		t.Fatal("answer is not private")
	}
	action, err = resolveRunAction(context.Background(), j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	args := runActionArguments(action, files[0])
	if strings.Contains(strings.Join(args, " "), text) {
		t.Fatal("raw answer entered argv")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("connected command invoked")
	}
}

func TestAttentionResolutionRejectsUnsafeOrUnsupportedRuns(t *testing.T) {
	for _, test := range []string{"live", "busy", "ready", "no question", "unknown stage", "stale checkpoint", "wrong directory", "ambiguous", "bad id"} {
		t.Run(test, func(t *testing.T) {
			j, _ := attentionFixture(t)
			directory := filepath.Dir(j.Workspace)
			id := j.ID
			switch test {
			case "live":
				tracker, err := runstatus.Begin(directory, j)
				if err != nil {
					t.Fatal(err)
				}
				defer tracker.Close()
			case "busy":
				lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "ready":
				j.State = "ready"
			case "no question":
				j.Outcome.Questions = nil
			case "unknown stage":
				j.State = "blocked"
				j.ResumeState = "mystery"
			case "stale checkpoint":
				action, err := resolveRunAction(context.Background(), id, true)
				if err != nil {
					t.Fatal(err)
				}
				j.Outcome.Questions = []string{"New question"}
				if err := workrun.Save(directory, &j); err != nil {
					t.Fatal(err)
				}
				if _, err := saveInlineAnswer(action, "old answer"); err == nil {
					t.Fatal("stale answer accepted")
				}
				return
			case "wrong directory":
				parent, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(parent, id)
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				j.Workspace = filepath.Join(other, "workspace")
				directory = other
			case "ambiguous":
				other := j
				other.ID = "abcdef0123456789abcdef02"
				dir, err := workrun.RunDirectory(j.Plan.Root, j.Plan.Reference, filepath.Base(j.Plan.Ticket), other.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				other.Workspace = filepath.Join(dir, "workspace")
				if err := workrun.Save(dir, &other); err != nil {
					t.Fatal(err)
				}
				if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Register(dir, other); err != nil {
					t.Fatal(err)
				}
				id = "abcdef"
			case "bad id":
				id = "ab"
			}
			if test == "ready" || test == "no question" || test == "unknown stage" || test == "wrong directory" {
				if err := workrun.Save(directory, &j); err != nil {
					t.Fatal(err)
				}
				registry := runstatus.New(os.Getenv("SDLC_STATE_DIR"))
				if test == "wrong directory" {
					if err := os.Remove(filepath.Join(os.Getenv("SDLC_STATE_DIR"), "runs", j.ID+".json")); err != nil {
						t.Fatal(err)
					}
				}
				if err := registry.Register(directory, j); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := resolveRunAction(context.Background(), id, true); err == nil {
				t.Fatal("unsafe action accepted")
			}
		})
	}
}

func TestResumeCommandAndPreviewBindCheckpointAndAnswer(t *testing.T) {
	j, _ := attentionFixture(t)
	ctx := context.Background()
	if err := resumeCommand(ctx, []string{"--run", j.ID[:3], "--dry-run"}, io.Discard); err == nil || !strings.Contains(err.Error(), "sdlc answer") {
		t.Fatalf("waiting resume: %v", err)
	}
	action, err := resolveRunAction(ctx, j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := saveInlineAnswer(action, "first answer")
	if err != nil {
		t.Fatal(err)
	}
	args := runActionArguments(action, path)
	first, err := offlinePlan(ctx, args, j.Plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed answer"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := offlinePlan(ctx, args, j.Plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	if planHash(first) == planHash(second) {
		t.Fatal("changed answer retained preview identity")
	}
	j.State = "blocked"
	j.ResumeState = "checking"
	j.Outcome.Questions = nil
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	if _, err := offlinePlan(ctx, args, j.Plan.Root); err == nil || !strings.Contains(err.Error(), "checkpoint changed") {
		t.Fatalf("stale preview accepted: %v", err)
	}
	t.Chdir(t.TempDir())
	if err := resumeCommand(ctx, []string{"--run", j.ID[:3], "--dry-run"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Forgotten checkpoints still resolve in the selected ticket's private directory.
	if err := runstatus.New(os.Getenv("SDLC_STATE_DIR")).Forget(j.ID); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{j.ID[:3], j.ID[:4], j.ID[:5]} {
		if err := runCommand(ctx, append(runArgs("--resume", prefix, "--dry-run"), "--run-root", j.Plan.Root), io.Discard); err != nil {
			t.Fatalf("retained resume %q: %v", prefix, err)
		}
	}
	// Legacy explicit answer files fail before connected preflight when no question exists.
	if err := runCommand(ctx, append(runArgs("--resume", j.ID, "--answer-file", path), "--run-root", j.Plan.Root), io.Discard); err == nil || !strings.Contains(err.Error(), "no pending human question") {
		t.Fatalf("unexpected early answer rejection: %v", err)
	}
}

func TestBoundedTerminalAnswerAndCancellation(t *testing.T) {
	value, err := readTerminalAnswer(strings.NewReader("one\n..\n//cancel\n\\literal\n.\n"))
	if err != nil || value != "one\n.\n/cancel\n\\literal\n" {
		t.Fatalf("%q %v", value, err)
	}
	if _, err := readTerminalAnswer(strings.NewReader("one\n/cancel\n")); !errors.Is(err, errAnswerCancelled) {
		t.Fatal(err)
	}
	for _, input := range []string{" \n.\n", "missing submit", strings.Repeat("a", 64*1024+1) + "\n.\n"} {
		if _, err := readTerminalAnswer(strings.NewReader(input)); err == nil {
			t.Fatal("unbounded or unsubmitted input accepted")
		}
	}
	for _, text := range []string{"", " \n\t", string([]byte{0xff}), strings.Repeat("a", 64*1024+1)} {
		if err := validInlineAnswer(text); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	j, _ := attentionFixture(t)
	var out bytes.Buffer
	if err := answerCommand(context.Background(), []string{"--run", j.ID}, strings.NewReader("/cancel\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(j.Workspace), "answers")); !os.IsNotExist(err) {
		t.Fatal("cancel saved an answer")
	}
	if !strings.Contains(out.String(), "Answer cancelled") {
		t.Fatal(out.String())
	}
}

func TestInspectRunActionLeavesMissingControllerLockUntouched(t *testing.T) {
	j, _ := attentionFixture(t)
	before := dashboardTree(t, filepath.Dir(j.Workspace))
	if _, err := inspectRunAction(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	after := dashboardTree(t, filepath.Dir(j.Workspace))
	if len(before) != len(after) {
		t.Fatal("inspection created a controller lock")
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatalf("inspection changed %s", path)
		}
	}
}

func TestAnswerDryRunWithoutInputShowsQuestionsWithoutWriting(t *testing.T) {
	j, _ := attentionFixture(t)
	before := dashboardTree(t, filepath.Dir(j.Workspace))
	var output bytes.Buffer
	if err := answerCommand(context.Background(), []string{"--run", j.ID, "--dry-run"}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), j.Outcome.Questions[0]) || strings.Contains(output.String(), "Enter answer") {
		t.Fatal(output.String())
	}
	after := dashboardTree(t, filepath.Dir(j.Workspace))
	if len(before) != len(after) {
		t.Fatal("dry run wrote private state")
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatal("dry run changed state")
		}
	}
}
