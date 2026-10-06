package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestMissingFileRecoveryIsVisibleInRunAndShellViews(t *testing.T) {
	j, marker := attentionFixture(t)
	j.ResumeState = "implementing"
	j.Outcome.Questions = []string{"Please supply .sdlc/work/TASK-1/specification.md."}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	adapter := &shellAdapter{}
	selected, err := adapter.resolveRun(context.Background(), "", j.ID)
	if err != nil || selected.InputAction != "/inputs --run "+j.ID {
		t.Fatalf("shell file repair: %+v %v", selected, err)
	}
	action, err := inspectRunAction(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var detail bytes.Buffer
	if err := dashboard.Detail(&detail, action.View, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{detail.String(), progressStateText(action.View)} {
		if !strings.Contains(text, "Missing file? sdlc inputs --run "+j.ID) {
			t.Fatalf("missing file repair route: %s", text)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inspection started connected work")
	}
}

func TestShellResponseUsesRecordedProjectAndBindsAnswerToQuestion(t *testing.T) {
	j, marker := attentionFixture(t)
	t.Chdir(t.TempDir())
	a := &shellAdapter{plans: map[string]string{}}
	selected, err := a.resolveRun(context.Background(), "different-project", j.ID[:3])
	if err != nil || selected.Root != j.Plan.Root || selected.ID != j.ID || len(selected.Questions) != 2 {
		t.Fatalf("selected: %+v %v", selected, err)
	}
	text := "Use 404.\n/exit is literal text; `$(example)` stays literal."
	args, err := a.prepareRunResponse(context.Background(), selected, &text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), text) {
		t.Fatal("answer entered controller arguments")
	}
	options, err := parseRunOptions(args[1:])
	if err != nil || options.resume != j.ID || options.root != j.Plan.Root {
		t.Fatalf("resume target: %+v %v", options, err)
	}
	data, err := readHumanAnswer(options.answerFile)
	if err != nil || string(data) != text {
		t.Fatalf("saved answer: %q %v", data, err)
	}
	if a.plans[planKey(j.Plan.Root, args)] == "" {
		t.Fatal("answer was not bound to the launch preview")
	}
	j.Outcome.Questions = []string{"Changed question"}
	if err := workrun.Save(filepath.Dir(j.Workspace), &j); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareRunResponse(context.Background(), selected, &text); err == nil {
		t.Fatal("answer to an outdated question accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("answer preparation contacted a provider")
	}
}

func TestInspectRunSelectorUsesDashboardDetail(t *testing.T) {
	j, _ := attentionFixture(t)
	var output bytes.Buffer
	if err := inspectCommand(context.Background(), []string{"@run:" + j.ID[:3], "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), j.ID) || !strings.Contains(output.String(), "waiting_for_human") {
		t.Fatalf("run detail: %s", output.String())
	}
}
