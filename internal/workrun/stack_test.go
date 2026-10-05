package workrun

import (
	"context"
	"testing"
)

type replayRebaser struct{ calls int }

func (rebaser *replayRebaser) Rebase(context.Context, string, string, string, string) (RebaseResult, error) {
	rebaser.calls++
	return RebaseResult{}, nil
}

func TestReplayReconciliationUsesCheckpointResult(t *testing.T) {
	_, journal := testRun(t)
	snapshot := BranchSnapshot{Branch: "main", SHA: testBase, Bundle: "/private/run/publisher/snapshot.bundle"}
	if err := BeginReconciliation(journal, snapshot); err != nil {
		t.Fatal(err)
	}
	rebaser := &replayRebaser{}
	if _, err := ReplayReconciliation(context.Background(), rebaser, journal); err != nil {
		t.Fatal(err)
	}
	if journal.Reconciliation.State != "rebased" || rebaser.calls != 1 {
		t.Fatalf("reconciliation was not checkpointed: %+v", journal.Reconciliation)
	}
	if _, err := ReplayReconciliation(context.Background(), rebaser, journal); err != nil || rebaser.calls != 1 {
		t.Fatalf("checkpoint replay reran rebase: %v, %d", err, rebaser.calls)
	}
}
