package runusage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeReceptionTimingSeparatesInitialGapAndFinalSilence(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	at := start
	o := NewObserver("codex", "model")
	o.startedAt = start
	o.now = func() time.Time { return at }
	at = start.Add(2 * time.Second)
	read(o, `not-json`)
	read(o, `{"type":"thread.started","thread_id":"private-session"}`)
	at = start.Add(5 * time.Second)
	read(o, `{"type":"item.completed","item":{"type":"agent_message","text":"private-content"}}`)
	at = start.Add(9*time.Minute + 5*time.Second)
	_, _ = o.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":10}}`))
	at = start.Add(9*time.Minute + 7*time.Second)
	o.Finish()
	value := o.Timing()
	if value.Events != 3 || value.FirstEventMS == nil || *value.FirstEventMS != 2000 || value.LongestGapMS != 540000 || value.FinalSilenceMS == nil || *value.FinalSilenceMS != 2000 {
		t.Fatalf("reception boundaries lost: %+v", value)
	}
	*value.FirstEventMS = 99
	if *o.Timing().FirstEventMS != 2000 {
		t.Fatal("timing snapshot aliases observer")
	}
	encoded, _ := json.Marshal(o.Timing())
	if strings.Contains(string(encoded), "private") {
		t.Fatal("timing retained event contents")
	}
}

func TestReceptionTimingWithoutEventsRemainsUnknown(t *testing.T) {
	o := NewObserver("codex", "model")
	read(o, "invalid")
	read(o, `{}`)
	o.Finish()
	value := o.Timing()
	if value.Events != 0 || value.FirstEventMS != nil || value.FinalSilenceMS != nil || value.LongestGapMS != 0 {
		t.Fatalf("invented reception evidence: %+v", value)
	}
}

func TestTimingSummariesPreserveLegacyCoverageAndPrivateBoundaries(t *testing.T) {
	d := dir(t)
	old := attempt(1, "implementation", "private-session", 10)
	if err := SaveAttempt(d, old); err != nil {
		t.Fatal(err)
	}
	newer := attempt(2, "implementation", "private-session", 20)
	newer.EventTiming = &EventTiming{Events: 3, FirstEventMS: n(100), LongestGapMS: 200, FinalSilenceMS: n(50)}
	if err := SaveAttempt(d, newer); err != nil {
		t.Fatal(err)
	}
	result, err := LoadSummary(d, "run-one", 2)
	if err != nil {
		t.Fatal(err)
	}
	value := result.Groups[0].EventTiming
	if value == nil || value.RecordedAttempts != 1 || value.MissingAttempts != 1 || value.Events != 3 || *value.LongestFirstEventMS != 100 || value.LongestGapMS != 200 || *value.LongestFinalMS != 50 {
		t.Fatalf("legacy coverage or measured intervals lost: %+v", value)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-session") {
		t.Fatal("summary exposed native identity")
	}
	newer.EventTiming.Events = 0
	if err := SaveAttempt(d, newer); err == nil {
		t.Fatal("accepted timing with no matching events")
	}
}
