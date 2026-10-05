package workseries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testState(t *testing.T, tickets ...Ticket) (string, *State) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sdlc", "work", "Example"), 0700); err != nil {
		t.Fatal(err)
	}
	p := Plan{Version: 1, Root: root, Reference: "Example", Base: "main", Tickets: tickets}
	if err := validatePlan(&p); err != nil {
		t.Fatal(err)
	}
	p.DefinitionSHA = definitionSHA(p)
	dir, err := Directory(root, "Example", true)
	if err != nil {
		t.Fatal(err)
	}
	return dir, &State{Version: 1, Plan: p, Results: map[string]Result{}}
}

type fakeDriver struct {
	mu              sync.Mutex
	executed        []string
	active, max     int
	bases           map[string]Target
	observe         map[string]Observation
	reconciled      []string
	executeResult   Result
	executeErr      error
	reconcileResult Result
	reconcileErr    error
}

func sha(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:20])
}

func (d *fakeDriver) Base(_ context.Context, name string) (Target, error) {
	if x, ok := d.bases[name]; ok {
		return x, nil
	}
	return Target{Base: name, SHA: sha(name)}, nil
}
func (d *fakeDriver) Execute(_ context.Context, t Ticket, target Target, r *Result) (Result, error) {
	d.mu.Lock()
	d.executed = append(d.executed, filepath.Base(t.File))
	d.active++
	if d.active > d.max {
		d.max = d.active
	}
	d.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
	if d.executeErr != nil {
		return d.executeResult, d.executeErr
	}
	return Result{RunID: r.RunID, State: "ready", Branch: "branch-" + filepath.Base(t.File), Base: target.Base, BaseSHA: target.SHA, HeadSHA: sha("head-" + filepath.Base(t.File))}, nil
}

func TestRunnerPersistsStoppedExecutionMetadata(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	d := &fakeDriver{executeResult: Result{State: "stopped", Branch: "stopped-branch", URL: "https://example.invalid/pr/1"}, executeErr: errors.New("worker stopped")}
	err := (Runner{Driver: d}).Run(context.Background(), dir, s)
	if err == nil {
		t.Fatal("stopped worker did not require attention")
	}
	got := s.Results["01-one.md"]
	if got.State != "stopped" || got.Branch != "stopped-branch" || got.URL == "" || got.RunID == "" {
		t.Fatalf("returned stopped metadata was lost: %+v", got)
	}
}

func TestRunnerBlocksFailedActiveExecution(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	d := &fakeDriver{executeResult: Result{State: "running", Branch: "retained-branch"}, executeErr: errors.New("source preparation failed")}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("preparation failure did not require attention")
	}
	got := s.Results["01-one.md"]
	if got.State != "blocked" || got.StopReason != "source preparation failed" || got.RunID == "" || got.Branch != "retained-branch" || len(d.executed) != 1 {
		t.Fatalf("failed execution state lost or retried: %+v, %v", got, d.executed)
	}
}

func TestRunnerRetargetsPreparedCheckpointWithoutReconcile(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "prepared", Base: "main", BaseSHA: sha("old-base")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("new-base")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if got := s.Results["01-one.md"].BaseSHA; got != sha("new-base") {
		t.Fatalf("prepared target was not refreshed: %q", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reconciled) != 0 {
		t.Fatalf("prepared placeholder was reconciled: %v", d.reconciled)
	}
}

func TestRunnerBlocksExternalBaseNameEdit(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Base: "main", BaseSHA: sha("base"), HeadSHA: sha("head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{observe: map[string]Observation{"111111111111111111111111": {State: "OPEN", Base: "other", BaseSHA: sha("base"), HeadSHA: sha("head")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("external base-name edit did not require attention")
	}
}
func (d *fakeDriver) Observe(_ context.Context, r Result) (Observation, error) {
	if x, ok := d.observe[r.RunID]; ok {
		return x, nil
	}
	return Observation{State: "OPEN", Base: r.Base, BaseSHA: r.BaseSHA, HeadSHA: r.HeadSHA}, nil
}
func (d *fakeDriver) Reconcile(_ context.Context, _ Ticket, target Target, r Result) (Result, error) {
	d.mu.Lock()
	d.reconciled = append(d.reconciled, r.RunID)
	d.mu.Unlock()
	r.Base = target.Base
	r.BaseSHA = target.SHA
	if d.reconcileErr != nil {
		return d.reconcileResult, d.reconcileErr
	}
	if d.reconcileResult.State != "" {
		return d.reconcileResult, nil
	}
	return r, nil
}

func TestPlanOrdersDependencyBeforeHigherPriorityChild(t *testing.T) {
	p := Plan{Version: 1, Reference: "Example", Base: "main", Tickets: []Ticket{{File: "01-child.md", Priority: -1, DependsOn: []string{"02-parent.md"}}, {File: "02-parent.md", Priority: 10}}}
	if err := validatePlan(&p); err != nil {
		t.Fatal(err)
	}
	if p.Tickets[0].File != "02-parent.md" {
		t.Fatalf("dependency order = %+v", p.Tickets)
	}
}

func TestCIStatusMatrix(t *testing.T) {
	for _, test := range []struct {
		status string
		action bool
	}{{"", false}, {"passed", false}, {"pending", true}, {"failed", true}, {"missing", true}, {"unverified", true}} {
		if got := ciNeedsAction(test.status); got != test.action {
			t.Fatalf("CI %q action=%v", test.status, got)
		}
	}
}

func TestRunnerBlocksMergedPRWithChangedBase(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Base: "main", BaseSHA: sha("base"), HeadSHA: sha("head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{observe: map[string]Observation{"111111111111111111111111": {State: "MERGED", Base: "other", HeadSHA: sha("head"), MergeSHA: sha("merge")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("changed-base merge did not require attention")
	}
}

func TestRunnerReconcilesReadyPRForNonPassingCI(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Branch: "branch", Base: "main", BaseSHA: sha("base"), HeadSHA: sha("head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("base")}}, observe: map[string]Observation{"111111111111111111111111": {State: "OPEN", Base: "main", BaseSHA: sha("base"), HeadSHA: sha("head"), CI: "pending", Details: "checks pending"}}, reconcileResult: Result{State: "waiting_human"}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("pending CI did not require attention")
	}
	got := s.Results["01-one.md"]
	if got.State != "waiting_human" || got.StopReason != "checks pending" {
		t.Fatalf("CI reconcile did not checkpoint state: %+v", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reconciled) != 1 {
		t.Fatalf("CI did not reconcile: %v", d.reconciled)
	}
}

func TestRunnerResumesActiveStageWithoutSelfResourceConflict(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md", Touches: []string{"src"}})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "implementing", Base: "main", BaseSHA: sha("base")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("base")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.executed) != 1 {
		t.Fatalf("active stage did not resume: %v", d.executed)
	}
}

func TestRunnerPersistsReconcileFailureMetadata(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Branch: "branch", Base: "main", BaseSHA: sha("old-base"), HeadSHA: sha("head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("new-target")}}, reconcileResult: Result{State: "waiting_human", Branch: "repair-branch", URL: "https://example.invalid/repair"}, reconcileErr: errors.New("needs approval")}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("reconcile attention was not returned")
	}
	got := s.Results["01-one.md"]
	if got.State != "waiting_human" || got.Branch != "repair-branch" || got.URL == "" {
		t.Fatalf("reconcile metadata lost: %+v", got)
	}
}

func TestRunnerRejectsInvalidExistingJournalWithoutReplacingIt(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	path := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{Driver: &fakeDriver{}}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("invalid journal accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "not json" {
		t.Fatalf("journal was replaced: %q %v", data, err)
	}
}

func TestRunnerReconcilesReadyRootWhenMainAdvances(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Base: "main", BaseSHA: sha("old-base"), HeadSHA: sha("head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("new")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if got := s.Results["01-one.md"].BaseSHA; got != sha("new") {
		t.Fatalf("ready root was not reconciled: %q", got)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reconciled) != 1 {
		t.Fatalf("reconcile calls = %v", d.reconciled)
	}
}

func TestRunnerReconcilesReadyChildAfterParentSquashMerge(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-parent.md"}, Ticket{File: "02-child.md", DependsOn: []string{"01-parent.md"}})
	s.Results["01-parent.md"] = Result{RunID: "111111111111111111111111", State: "ready", Branch: "branch-parent", Base: "main", BaseSHA: sha("old"), HeadSHA: sha("parent-head")}
	s.Results["02-child.md"] = Result{RunID: "222222222222222222222222", State: "ready", Branch: "branch-child", Base: "branch-parent", BaseSHA: sha("parent-head"), HeadSHA: sha("child-head")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("squash")}}, observe: map[string]Observation{"111111111111111111111111": {State: "MERGED", HeadSHA: sha("parent-head"), MergeSHA: sha("squash")}, "222222222222222222222222": {State: "OPEN", Base: "main", BaseSHA: sha("squash"), HeadSHA: sha("child-head")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	child := s.Results["02-child.md"]
	if child.State != "ready" || child.BaseSHA != sha("squash") {
		t.Fatalf("child was not reconciled: %+v", child)
	}
}

func TestRunnerUsesPriorityAndSerializesOverlappingResources(t *testing.T) {
	dir, s := testState(t,
		Ticket{File: "01-one.md", Priority: 1, Touches: []string{"src"}},
		Ticket{File: "02-two.md", Priority: 0, Touches: []string{"src/api"}},
		Ticket{File: "03-three.md", Priority: 2, Touches: []string{"docs"}},
	)
	d := &fakeDriver{}
	if err := (Runner{Driver: d, Parallel: 3}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.max > 2 {
		t.Fatalf("overlapping writers ran together: %d", d.max)
	}
	if len(d.executed) != 3 || d.executed[2] != "01-one.md" {
		t.Fatalf("overlapping lower-priority ticket was not deferred: %v", d.executed)
	}
}

func TestRunnerWaitsForSingleReadyParentAtItsHead(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-parent.md"}, Ticket{File: "02-child.md", DependsOn: []string{"01-parent.md"}})
	d := &fakeDriver{bases: map[string]Target{"branch-01-parent.md": {Base: "branch-01-parent.md", SHA: sha("head-01-parent.md")}}}
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if fmt.Sprint(d.executed) != "[01-parent.md 02-child.md]" {
		t.Fatalf("dependency order = %v", d.executed)
	}
}

func TestRunnerDoesNotRetryStoppedWorkAfterResume(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "blocked", StopReason: "stopped"}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("stopped result did not require attention")
	}
	if len(d.executed) != 0 {
		t.Fatalf("blocked result retried: %v", d.executed)
	}
}

func TestRunnerBlocksUnexpectedEditedPR(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"})
	s.Results["01-one.md"] = Result{RunID: "111111111111111111111111", State: "ready", Base: "main", BaseSHA: sha("main"), HeadSHA: sha("expected")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{observe: map[string]Observation{"111111111111111111111111": {State: "OPEN", Base: "main", BaseSHA: sha("main"), HeadSHA: sha("edited")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("edited PR did not require attention")
	}
	if s.Results["01-one.md"].State != "blocked" {
		t.Fatalf("edited PR was accepted: %+v", s.Results["01-one.md"])
	}
}

func TestRunnerMergedParentUsesIntegrationBase(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-parent.md"}, Ticket{File: "02-child.md", DependsOn: []string{"01-parent.md"}})
	s.Results["01-parent.md"] = Result{RunID: "111111111111111111111111", State: "merged", HeadSHA: sha("head-parent"), MergeSHA: sha("squash")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{bases: map[string]Target{"main": {Base: "main", SHA: sha("squash-merge")}}}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if got := s.Results["02-child.md"].BaseSHA; got != sha("squash-merge") {
		t.Fatalf("child base = %q", got)
	}
}

func TestValidatePlanRejectsCyclesAndMissingDependencies(t *testing.T) {
	for _, tickets := range [][]Ticket{{{File: "01-a.md", DependsOn: []string{"02-b.md"}}, {File: "02-b.md", DependsOn: []string{"01-a.md"}}}, {{File: "01-a.md", DependsOn: []string{"missing.md"}}}} {
		p := Plan{Version: 1, Reference: "Example", Base: "main", Tickets: tickets}
		if validatePlan(&p) == nil {
			t.Fatal("invalid dependency graph accepted")
		}
	}
}

func TestRunnerMultipleParentsWaitUntilBothMerge(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-api.md"}, Ticket{File: "02-consumer.md"}, Ticket{File: "03-join.md", DependsOn: []string{"01-api.md", "02-consumer.md"}})
	for i, file := range []string{"01-api.md", "02-consumer.md"} {
		s.Results[file] = Result{RunID: fmt.Sprintf("%024x", i+1), State: "ready", Branch: "branch-" + file, Base: "main", BaseSHA: sha("main"), HeadSHA: sha("head-" + file)}
	}
	driver := &fakeDriver{}
	if err := (Runner{Driver: driver, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if len(driver.executed) != 0 {
		t.Fatal("multi-parent child started before merge")
	}
	for _, file := range []string{"01-api.md", "02-consumer.md"} {
		result := s.Results[file]
		result.State = "merged"
		result.MergeSHA = sha("merged-" + file)
		s.Results[file] = result
	}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{Driver: driver, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(driver.executed) != "[03-join.md]" || s.Results["03-join.md"].Base != "main" {
		t.Fatalf("incorrect join launch: %+v %v", s.Results, driver.executed)
	}
}

func TestRunnerIndependentTicketsRunConcurrentlyAndDoNotDuplicate(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-one.md"}, Ticket{File: "02-two.md"})
	driver := &fakeDriver{}
	runner := Runner{Driver: driver, Parallel: 2}
	if err := runner.Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if driver.max != 2 || len(driver.executed) != 2 {
		t.Fatalf("parallel capacity unused: active=%d jobs=%v", driver.max, driver.executed)
	}
	if s.Results["01-one.md"].RunID == s.Results["02-two.md"].RunID {
		t.Fatal("concurrent jobs shared an identity")
	}
	if err := runner.Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if len(driver.executed) != 2 {
		t.Fatalf("repeated feature duplicated ready jobs: %v", driver.executed)
	}
}
