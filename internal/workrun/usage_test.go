package workrun

import (
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestCoalescedQuotaBaselineIsCheckpointedBeforeNativeUsage(t *testing.T) {
	observer := runusage.NewObserver("codex", "example-model")
	checkpoints := 0
	recorder := &subscriptionRecorder{observer: observer, save: func(usage runusage.Usage) error {
		checkpoints++
		if usage.Subscription == nil || usage.Subscription.Before == nil || usage.Aggregate != nil {
			t.Fatalf("model usage was processed before quota baseline: %+v", usage)
		}
		return nil
	}}
	chunk := quotaBoundary("before", 80) + "\n" + `{"type":"thread.started","thread_id":"one"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":123}}` + "\n"
	if _, err := recorder.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 || observer.Snapshot().Aggregate == nil || *observer.Snapshot().Aggregate.InputTokens != 123 {
		t.Fatal("coalesced native events lost")
	}
}
