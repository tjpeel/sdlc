package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type dependencyRemoteFake struct {
	branches map[string]string
	parents  map[int]RemotePR
	ci       string
	calls    int
}

func (f *dependencyRemoteFake) Branch(_ context.Context, _ Plan, branch string) (BranchSnapshot, error) {
	f.calls++
	return BranchSnapshot{Branch: branch, SHA: f.branches[branch]}, nil
}
func (f *dependencyRemoteFake) Observe(_ context.Context, _ Plan, p Publication) (RemotePR, error) {
	f.calls++
	return f.parents[p.Number], nil
}
func (f *dependencyRemoteFake) Checks(_ context.Context, _ Plan, _ Publication) (CIResult, error) {
	f.calls++
	return CIResult{Status: f.ci}, nil
}
func dependencyFixture(t *testing.T) (string, *Journal, *dependencyRemoteFake) {
	t.Helper()
	directory, journal := testRun(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(root, ".sdlc", "work", "TASK-1", "series")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	parent := DerivedParent{Ticket: "01-parent.md", RunID: strings.Repeat("a", 24), Branch: "parent", Base: "main", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40), State: "ready", Number: 1}
	data, _ := json.Marshal(struct{ Results map[string]DerivedParent }{map[string]DerivedParent{parent.Ticket: parent}})
	if err := os.WriteFile(filepath.Join(owner, "journal.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	journal.Plan.Root = root
	journal.Plan.Base = "derived"
	journal.Plan.DependencyContext = &DependencyContext{Owner: owner, Ticket: "02-child.md", IntegrationBranch: "main", IntegrationSHA: parent.BaseSHA, SourceSHA: strings.Repeat("3", 40), SignedSHA: journal.Plan.SourceSHA, Tree: testTree, Parents: []DerivedParent{parent}}
	remote := &dependencyRemoteFake{branches: map[string]string{"main": parent.BaseSHA, "derived": journal.Plan.SourceSHA, "parent": parent.HeadSHA}, parents: map[int]RemotePR{1: {State: "OPEN", Base: parent.Base, BaseSHA: parent.BaseSHA, HeadSHA: parent.HeadSHA}}, ci: "passed"}
	return directory, journal, remote
}
func TestDependencyContextAllowsCurrentHumanActionsButRejectsStaleRefsAndCI(t *testing.T) {
	for _, name := range []string{"current", "integration", "parent head", "parent base", "parent CI", "owner readiness"} {
		t.Run(name, func(t *testing.T) {
			directory, journal, remote := dependencyFixture(t)
			switch name {
			case "integration":
				remote.branches["main"] = strings.Repeat("3", 40)
			case "parent head":
				remote.branches["parent"] = strings.Repeat("3", 40)
			case "parent base":
				p := remote.parents[1]
				p.Base = "other"
				remote.parents[1] = p
			case "parent CI":
				remote.ci = "failed"
			case "owner readiness":
				if err := os.WriteFile(filepath.Join(journal.Plan.DependencyContext.Owner, "journal.json"), []byte(`{"Results":{}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateDependencyContext(context.Background(), directory, journal.Plan, remote)
			if name == "current" && err != nil {
				t.Fatal(err)
			}
			if name != "current" && !errors.Is(err, ErrDependencyInvalid) {
				t.Fatalf("stale context accepted: %v", err)
			}
		})
	}
}
func TestDeferredReviewAnswerSurvivesHoldAndStartsOriginalImplementationAfterRestack(t *testing.T) {
	directory, journal := testRun(t)
	journal.State, journal.PendingRole, journal.SessionID = "waiting_for_human", "review", testNative
	journal.Outcome.Questions = []string{"Which observed result should the ticket preserve?"}
	if err := Save(directory, journal); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateDependency(directory); err != nil {
		t.Fatal(err)
	}
	if err := StoreDeferredAnswer(directory, journal, "Preserve the documented result."); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "waiting_for_human" || saved.DeferredAnswer == "" || saved.SessionID != testNative || len(saved.Outcome.Questions) != 1 {
		t.Fatal("deferred answer changed the hold or original session")
	}
	saved.RestackNeedsChecks = true
	if err := ClearDependencyInvalidation(directory); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{outcomes: []Outcome{{Status: "blocked", Summary: "fixture stop"}}}
	runner := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := runner.Run(context.Background(), directory, &saved, ""); err == nil {
		t.Fatal("fixture native hold not returned")
	}
	if len(p.calls) != 1 || p.calls[0].Role != "implementation" || p.calls[0].ResumeID != testNative || !strings.Contains(p.calls[0].Prompt, "Preserve the documented result.") {
		t.Fatalf("restacked review answer bypassed current implementation: %+v", p.calls)
	}
	if saved.DeferredAnswer != "" || saved.ReviewHumanInput == nil {
		t.Fatal("answer was not consumed and retained as review context")
	}
}
