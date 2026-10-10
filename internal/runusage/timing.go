package runusage

import "time"

// EventTiming measures reception of native JSONL, not provider queue or compute
// time. Silence can include tool execution, agent waits and buffering.
type EventTiming struct {
	Events         int64  `json:"events"`
	FirstEventMS   *int64 `json:"first_event_ms,omitempty"`
	LongestGapMS   int64  `json:"longest_event_gap_ms"`
	FinalSilenceMS *int64 `json:"final_silence_ms,omitempty"`
}

type EventTimingSummary struct {
	RecordedAttempts    int    `json:"recorded_attempts"`
	MissingAttempts     int    `json:"missing_attempts"`
	Events              int64  `json:"events"`
	LongestFirstEventMS *int64 `json:"longest_first_event_ms,omitempty"`
	LongestGapMS        int64  `json:"longest_event_gap_ms"`
	LongestFinalMS      *int64 `json:"longest_final_silence_ms,omitempty"`
}

func elapsedMS(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func (o *Observer) observeEvent() {
	at := o.receivedAt
	if at.IsZero() {
		at = o.now()
	}
	if o.timing.Events == 0 {
		value := elapsedMS(o.startedAt, at)
		o.timing.FirstEventMS = &value
	} else if gap := elapsedMS(o.lastEventAt, at); gap > o.timing.LongestGapMS {
		o.timing.LongestGapMS = gap
	}
	o.timing.Events++
	o.lastEventAt = at
}

// Timing returns numeric reception evidence without retaining native contents.
func (o *Observer) Timing() EventTiming {
	o.mu.Lock()
	defer o.mu.Unlock()
	value := o.timing
	if value.FirstEventMS != nil {
		first := *value.FirstEventMS
		value.FirstEventMS = &first
		end := o.finishedAt
		if end.IsZero() {
			end = o.now()
		}
		final := elapsedMS(o.lastEventAt, end)
		value.FinalSilenceMS = &final
	}
	return value
}

func (summary *EventTimingSummary) add(value *EventTiming) {
	if value == nil {
		summary.MissingAttempts++
		return
	}
	summary.RecordedAttempts++
	summary.Events += value.Events
	if value.LongestGapMS > summary.LongestGapMS {
		summary.LongestGapMS = value.LongestGapMS
	}
	for _, pair := range []struct {
		target **int64
		source *int64
	}{
		{&summary.LongestFirstEventMS, value.FirstEventMS},
		{&summary.LongestFinalMS, value.FinalSilenceMS},
	} {
		if pair.source != nil && (*pair.target == nil || *pair.source > **pair.target) {
			copy := *pair.source
			*pair.target = &copy
		}
	}
}
