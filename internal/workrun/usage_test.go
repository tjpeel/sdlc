package workrun

import (
	"bytes"
	"errors"
	"io"
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

func TestOptionalContextCheckpointFailureStillForwardsNativeEvents(t *testing.T) {
	observer := runusage.NewObserver("codex", "example-model")
	recorder := &subscriptionRecorder{observer: observer, save: func(runusage.Usage) error { return errors.New("unavailable progress storage") }}
	var forwarded bytes.Buffer
	event := []byte(`{"type":"sdlc.codex_context","provider":"codex","coverage":"partial","observations":1,"compactions":0,"context":{"source":"codex.rollout.last_token_usage","scope":"request_context","input_tokens":100,"tokens":100}}` + "\n")
	if n, err := io.MultiWriter(recorder, &forwarded).Write(event); err != nil || n != len(event) {
		t.Fatalf("optional checkpoint interrupted execution: %d %v", n, err)
	}
	if !bytes.Equal(forwarded.Bytes(), event) || observer.Snapshot().ContextCheckpointErrors != 1 {
		t.Fatal("event forwarding or failure observation lost")
	}
	if observer.Snapshot().Context == nil {
		t.Fatal("numeric context observation lost")
	}
}
