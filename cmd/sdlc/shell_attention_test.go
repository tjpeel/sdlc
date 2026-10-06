package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestShellResponseUsesRecordedProjectAndBindsAnswerToQuestion(t *testing.T) {
	j, marker := attentionFixture(t)
	t.Chdir(t.TempDir())
	a := &shellAdapter{plans: map[string]string{}}
	selected, err := a.resolveRun(context.Background(), "different-project", j.ID[:6])
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
