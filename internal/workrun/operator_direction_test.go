package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorDirectionRetainsSessionAndReachesNextTurn(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{fail: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || j.SessionID != takeoverSession {
		t.Fatalf("expected interrupted takeover: %v", err)
	}
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", "context canceled\nlive diagnostic worker could not complete; controller checkpoint retained\ncontext canceled"
	if err := Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(j.Workspace, "operator-direction-dirty.txt")
	if err := os.WriteFile(dirty, []byte("unfinished work"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "Nothing was rolled out; remove unnecessary legacy backfill while preserving unrelated work."
	if err := RecordOperatorDirection(dir, j.UpdatedAt, text); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SessionID != takeoverSession || len(saved.OperatorDirections) != 1 || saved.OperatorDirections[0].FirstAttempt != 0 {
		t.Fatal("direction lost the original session or audit")
	}
	if b, err := os.ReadFile(dirty); err != nil || string(b) != "unfinished work" {
		t.Fatal("direction changed dirty work")
	}
	p.calls = nil
	_ = r.Run(context.Background(), dir, &saved, "")
	if len(p.calls) != 1 || p.calls[0].ResumeID != takeoverSession || !strings.Contains(p.calls[0].Prompt, text) || !strings.Contains(p.calls[0].Prompt, "human-authorized requirements") {
		t.Fatalf("direction was not sent to original session: %#v", p.calls)
	}
	if saved.OperatorDirections[0].FirstAttempt != saved.Attempt {
		t.Fatal("dispatch attempt was not recorded")
	}
	if !strings.Contains(operatorDirectionContext(saved, "review"), text) || !strings.Contains(operatorDirectionContext(saved, "review"), "not review approval") {
		t.Fatal("independent reviewer did not receive decision context")
	}
}

func TestOperatorDirectionRejectsHoldsAndStaleCheckpoint(t *testing.T) {
	dir, j := takeoverFixture(t)
	j.StopReason = "context canceled\nprovider rate limit"
	if ValidateOperatorDirection(*j, "direction") == nil {
		t.Fatal("provider hold accepted")
	}
	j.StopReason = "context canceled"
	j.Outcome.Status = "blocked"
	if ValidateOperatorDirection(*j, "direction") == nil {
		t.Fatal("retained provider hold accepted")
	}
	j.Outcome.Status = "reviewed"
	j.Outcome.Questions = []string{"question"}
	if ValidateOperatorDirection(*j, "direction") == nil {
		t.Fatal("pending question accepted")
	}
	j.Outcome.Questions = nil
	if ValidateOperatorDirection(*j, strings.Repeat("a", 8193)) == nil {
		t.Fatal("oversized direction accepted")
	}
	if err := RecordOperatorDirection(dir, j.UpdatedAt.Add(-1), "direction"); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
}
