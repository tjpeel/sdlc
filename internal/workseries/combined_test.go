package workseries

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type combinedDriverFake struct {
	fakeDriver
	vectors     [][]Dependency
	invalidated []string
	combineErr  error
	barrier     chan struct{}
	admitted    int
}

func (d *combinedDriverFake) Combine(_ context.Context, _ Ticket, integration Target, parents []Dependency) (Target, error) {
	d.vectors = append(d.vectors, append([]Dependency{}, parents...))
	if d.combineErr != nil {
		return Target{}, d.combineErr
	}
	return Target{Base: "derived-base", SHA: sha(fmt.Sprintf("%s/%v", integration.SHA, parents)), Dependencies: parents, Ancestors: integration.Ancestors}, nil
}
func (d *combinedDriverFake) Invalidate(_ context.Context, ticket Ticket, _ Result) error {
	d.invalidated = append(d.invalidated, ticket.File)
	return nil
}
func TestCombinedChildStartsAfterIndependentConcurrentParents(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-a.md"}, Ticket{File: "02-b.md"}, Ticket{File: "03-c.md", DependsOn: []string{"01-a.md", "02-b.md"}})
	d := &combinedDriverFake{barrier: make(chan struct{})}
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if d.max != 2 {
		t.Fatalf("independent parents lost concurrency: %d", d.max)
	}
	if !reflect.DeepEqual(d.executed, []string{"01-a.md", "02-b.md", "03-c.md"}) && !reflect.DeepEqual(d.executed, []string{"02-b.md", "01-a.md", "03-c.md"}) {
		t.Fatalf("child launched before both parent snapshots: %v", d.executed)
	}
	child := s.Results["03-c.md"]
	if child.Base != "derived-base" || len(child.Dependencies) != 2 || child.Dependencies[0].Ticket != "01-a.md" || child.Dependencies[1].Ticket != "02-b.md" {
		t.Fatalf("ordered vector lost: %+v", child)
	}
}
func TestFailedCombinationStopsBeforeChildLaunch(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-a.md"}, Ticket{File: "02-b.md"}, Ticket{File: "03-c.md", DependsOn: []string{"01-a.md", "02-b.md"}})
	d := &combinedDriverFake{combineErr: errors.New("combined checks failed")}
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err == nil {
		t.Fatal("failed aggregate accepted")
	}
	for _, executed := range d.executed {
		if executed == "03-c.md" {
			t.Fatal("child native job started on failing base")
		}
	}
	if _, ok := s.Results["03-c.md"]; ok {
		t.Fatal("child identity launched before aggregate passed")
	}
}
func TestWaitingDependencyNeverReconcilesWithEmptyTargetOrBlocksParentRepair(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-a.md"}, Ticket{File: "02-b.md"}, Ticket{File: "03-c.md", DependsOn: []string{"01-a.md", "02-b.md"}})
	s.Results["01-a.md"] = Result{RunID: "111111111111111111111111", State: "ready", Base: "main", BaseSHA: sha("main"), HeadSHA: sha("a"), Branch: "a"}
	s.Results["02-b.md"] = Result{RunID: "222222222222222222222222", State: "prepared", Base: "main", BaseSHA: sha("main")}
	s.Results["03-c.md"] = Result{RunID: "333333333333333333333333", State: "ready", Base: "derived-old", BaseSHA: sha("old"), HeadSHA: sha("c"), Branch: "c"}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	d := &combinedDriverFake{}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if len(d.invalidated) == 0 || d.invalidated[0] != "03-c.md" {
		t.Fatalf("child was not invalidated while parent unavailable: %v", d.invalidated)
	}
	if len(d.executed) == 0 || d.executed[0] != "02-b.md" {
		t.Fatalf("waiting child blocked parent: %v", d.executed)
	}
	for _, name := range d.reconciled {
		if name == "03-c.md" && len(d.vectors) == 0 {
			t.Fatal("child reconciled before combined target was available")
		}
	}
}

func (d *combinedDriverFake) Execute(ctx context.Context, ticket Ticket, target Target, previous *Result) (Result, error) {
	if d.barrier != nil && len(ticket.DependsOn) == 0 {
		d.mu.Lock()
		d.admitted++
		if d.admitted == 2 {
			close(d.barrier)
		}
		d.mu.Unlock()
		select {
		case <-d.barrier:
		case <-ctx.Done():
			return *previous, ctx.Err()
		}
	}
	return d.fakeDriver.Execute(ctx, ticket, target, previous)
}

type lifecycleCombinedDriver struct {
	combinedDriverFake
	steps   []string
	repairB bool
}

func (d *lifecycleCombinedDriver) Execute(ctx context.Context, t Ticket, target Target, previous *Result) (Result, error) {
	d.mu.Lock()
	d.steps = append(d.steps, "execute:"+t.File)
	d.mu.Unlock()
	result, err := d.combinedDriverFake.Execute(ctx, t, target, previous)
	if t.File == "02-b.md" && d.repairB {
		result.HeadSHA = sha("b repaired")
	}
	return result, err
}
func (d *lifecycleCombinedDriver) Reconcile(ctx context.Context, t Ticket, target Target, previous Result) (Result, error) {
	d.mu.Lock()
	d.steps = append(d.steps, "reconcile:"+t.File+":"+target.Base)
	d.mu.Unlock()
	result, err := d.fakeDriver.Reconcile(ctx, t, target, previous)
	if t.File == "02-b.md" {
		d.repairB = true
		result.State = "ci"
	}
	if t.File == "03-c.md" && !attentionState(previous.State) {
		result.State = "ready"
		result.HeadSHA = sha("c " + target.SHA)
	}
	return result, err
}
func TestIndependentParentSquashLifecycleRepairsBThenRestacksOriginalCOnce(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-a.md"}, Ticket{File: "02-b.md"}, Ticket{File: "03-c.md", DependsOn: []string{"01-a.md", "02-b.md"}})
	d := &lifecycleCombinedDriver{}
	d.bases = map[string]Target{"main": {Base: "main", SHA: sha("initial main")}}
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	originalB, originalC := s.Results["02-b.md"].RunID, s.Results["03-c.md"].RunID
	a := s.Results["01-a.md"]
	d.observe = map[string]Observation{a.RunID: {State: "MERGED", HeadSHA: a.HeadSHA, MergeSHA: sha("a squash"), Base: "main"}}
	d.bases["main"] = Target{Base: "main", SHA: sha("a squash")}
	d.steps = nil
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if want := []string{"reconcile:02-b.md:main", "execute:02-b.md", "reconcile:03-c.md:derived-base"}; !reflect.DeepEqual(d.steps, want) {
		t.Fatalf("parent repair and child batching = %v, want %v", d.steps, want)
	}
	if s.Results["02-b.md"].RunID != originalB || s.Results["03-c.md"].RunID != originalC {
		t.Fatal("restack replaced original jobs")
	}
	vector := s.Results["03-c.md"].Dependencies
	if len(vector) != 2 || vector[0].State != "merged" || vector[0].MergeSHA != sha("a squash") || vector[1].State != "ready" || vector[1].HeadSHA != sha("b repaired") {
		t.Fatalf("restack did not use completed parent vector: %+v", vector)
	}
	b := s.Results["02-b.md"]
	d.observe[b.RunID] = Observation{State: "MERGED", HeadSHA: b.HeadSHA, MergeSHA: sha("b squash"), Base: "main"}
	d.bases["main"] = Target{Base: "main", SHA: sha("b squash")}
	d.steps = nil
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.steps, []string{"reconcile:03-c.md:main"}) {
		t.Fatalf("final own run did not restack once to main: %v", d.steps)
	}
	if c := s.Results["03-c.md"]; c.RunID != originalC || c.Base != "main" || c.BaseSHA != sha("b squash") {
		t.Fatalf("final child identity/base lost: %+v", c)
	}
}
func TestInvalidHeldChildDoesNotPreventParentRepairAndPreservesQuestionHold(t *testing.T) {
	dir, s := testState(t, Ticket{File: "01-a.md"}, Ticket{File: "02-b.md"}, Ticket{File: "03-c.md", DependsOn: []string{"01-a.md", "02-b.md"}})
	d := &lifecycleCombinedDriver{}
	d.bases = map[string]Target{"main": {Base: "main", SHA: sha("initial main")}}
	if err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	c := s.Results["03-c.md"]
	c.State, c.StopReason = "waiting_for_human", "original question"
	s.Results["03-c.md"] = c
	a := s.Results["01-a.md"]
	d.observe = map[string]Observation{a.RunID: {State: "MERGED", HeadSHA: a.HeadSHA, MergeSHA: sha("a squash"), Base: "main"}}
	d.bases["main"] = Target{Base: "main", SHA: sha("a squash")}
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	err := (Runner{Driver: d, Parallel: 2}).Run(context.Background(), dir, s)
	if !d.repairB {
		t.Fatalf("held descendant prevented parent repair: %v", err)
	}
	// The production driver retains the journal hold; this fake returns it too.
	if got := s.Results["03-c.md"]; got.RunID != c.RunID || got.State != "waiting_for_human" || got.StopReason != "original question" || got.DependencyInvalid {
		t.Fatalf("held child was not recovered without losing the original question: %+v", got)
	}
}
