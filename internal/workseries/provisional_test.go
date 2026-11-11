package workseries

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func provisionalSeriesFixture(t *testing.T) (State, Ticket, *fakeDriver) {
	t.Helper()
	_, s := testState(t, Ticket{File: "01-parent.md"}, Ticket{File: "02-child.md", DependsOn: []string{"01-parent.md"}})
	id, head, base := strings.Repeat("1", 24), sha("parent-head"), sha("parent-base")
	pub := workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: head, BaseSHA: base}
	proof := workrun.ProvisionalParentProof{RunID: id, Ticket: "01-parent.md", Branch: "parent-branch", Base: "main", Publication: pub, CandidateHead: sha("candidate"), CandidateTree: sha("tree"), CheckKey: "checked", CIHead: head, CIStatus: "failed", Attempt: 2, ScannedAt: time.Now().UTC()}
	s.Results["01-parent.md"] = Result{RunID: id, State: "blocked", HoldStage: "ci", Branch: proof.Branch, Base: proof.Base, BaseSHA: base, HeadSHA: head, PRNumber: 1, URL: pub.URL, Provisional: &ProvisionalMark{Proof: proof, GrantedAt: time.Now().UTC()}}
	child := s.Plan.Tickets[1]
	d := &fakeDriver{bases: map[string]Target{"parent-branch": {Base: "parent-branch", SHA: head}}, observe: map[string]Observation{id: {State: "OPEN", Base: "main", BaseSHA: base, HeadSHA: head}}}
	return *s, child, d
}

func TestProvisionalParentTargetsExactPublishedHeadWithoutPassingCI(t *testing.T) {
	s, child, d := provisionalSeriesFixture(t)
	r := Runner{Driver: d}
	if err := r.attention(s); err != nil {
		t.Fatal(err)
	}
	target, wait, err := r.target(context.Background(), s, child)
	if err != nil || wait || target.Provisional == nil || target.SHA != s.Results["01-parent.md"].HeadSHA || target.Base != "parent-branch" || len(target.Dependencies) != 1 || target.Dependencies[0].State != "provisional" {
		t.Fatalf("wrong provisional target: %+v %v", target, err)
	}
	d.bases["parent-branch"] = Target{Base: "parent-branch", SHA: sha("moved")}
	if _, _, err := r.target(context.Background(), s, child); err == nil {
		t.Fatal("moved parent admitted")
	}
}

func TestProvisionalParentMovementInvalidatesDescendant(t *testing.T) {
	s, _, d := provisionalSeriesFixture(t)
	s.Results["02-child.md"] = Result{RunID: strings.Repeat("2", 24), State: "ready", Base: "parent-branch", BaseSHA: s.Results["01-parent.md"].HeadSHA, HeadSHA: sha("child")}
	d.observe[s.Results["01-parent.md"].RunID] = Observation{State: "OPEN", Base: "main", BaseSHA: s.Results["01-parent.md"].BaseSHA, HeadSHA: sha("moved")}
	_, err := (Runner{Driver: d}).observeReady(context.Background(), &s, func() error { return nil }, func(string, ...any) {}, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Results["01-parent.md"].Provisional != nil || !s.Results["02-child.md"].DependencyInvalid || len(s.ProvisionalAudit) != 1 || s.ProvisionalAudit[0].Action != "invalidate" {
		t.Fatal("parent movement did not invalidate its dependent")
	}
}

func TestFeatureStartsDependentFromProvisionalParentAfterRestart(t *testing.T) {
	s, _, d := provisionalSeriesFixture(t)
	dir, err := Directory(s.Plan.Root, s.Plan.Reference, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, &s); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Runner{Driver: d}).Run(context.Background(), dir, &reloaded); err != nil {
		t.Fatal(err)
	}
	parent, child := reloaded.Results["01-parent.md"], reloaded.Results["02-child.md"]
	if len(d.executed) != 1 || d.executed[0] != "02-child.md" || parent.State != "blocked" || parent.Provisional == nil || child.Base != parent.Branch || child.BaseSHA != parent.HeadSHA || len(child.Dependencies) != 1 || child.Dependencies[0].State != "provisional" {
		t.Fatalf("incorrect restarted scheduler state: parent=%+v child=%+v", parent, child)
	}
}
