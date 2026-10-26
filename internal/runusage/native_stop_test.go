package runusage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexCapacityRequiresUnmixedCanonicalFatalEvents(t *testing.T) {
	capacity := `{"type":"error","message":"Selected model is at capacity. Please try a different model."}`
	failed := `{"type":"turn.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}`
	usage := `{"type":"error","message":"You’ve hit your usage limit. Try later."}`
	for _, test := range []struct{ name, events, category string }{
		{"capacity", capacity + "\n" + failed, "model_capacity"},
		{"failed only", failed, "model_capacity"},
		{"usage then capacity", usage + "\n" + capacity, "native_failure"},
		{"capacity then usage", capacity + "\n" + usage, "native_failure"},
		{"unknown then capacity", `{"type":"error","message":"Login required"}` + "\n" + capacity, "native_failure"},
		{"unknown format", `{"type":"error","message":"model at capacity"}`, "native_failure"},
		{"quoted tool output", `{"type":"item.completed","item":{"type":"command_execution","output":"Selected model is at capacity. Please try a different model."}}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := NewObserver("codex", "selected-model")
			_, _ = o.Write([]byte(test.events + "\n"))
			o.Finish()
			stop := o.Snapshot().NativeStop
			if test.category == "" {
				if stop != nil {
					t.Fatal(stop)
				}
			} else if stop == nil || stop.Category != test.category {
				t.Fatalf("category=%+v want=%s", stop, test.category)
			}
		})
	}
}

func TestCodexFatalUsageCategoryComesOnlyFromOfficialFatalEvents(t *testing.T) {
	for _, test := range []struct {
		name, event string
		exhausted   bool
	}{
		{"account cap", `{"type":"error","message":"You’ve hit your usage limit. Upgrade or wait."}`, true},
		{"model cap", `{"type":"turn.failed","error":{"message":"You’ve hit your usage limit for this model. Try later."}}`, true},
		{"tool output", `{"type":"item.completed","item":{"type":"command_execution","output":"You’ve hit your usage limit. Upgrade or wait."}}`, false},
		{"assistant text", `{"type":"item.completed","item":{"type":"agent_message","text":"You’ve hit your usage limit. Upgrade or wait."}}`, false},
		{"request throttle", `{"type":"error","message":"Rate limit reached for requests"}`, false},
		{"authentication", `{"type":"turn.failed","error":{"message":"Login required"}}`, false},
		{"unknown format", `{"type":"error","message":"Usage limit reached"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := NewObserver("codex", "selected-model")
			_, _ = o.Write([]byte(test.event + "\n"))
			o.Finish()
			usage := o.Snapshot()
			if (usage.NativeStop != nil && usage.NativeStop.Category == "usage_exhausted") != test.exhausted {
				t.Fatalf("unexpected fatal category: %+v", usage.NativeStop)
			}
			if test.exhausted {
				if usage.NativeStop.Category != "usage_exhausted" || usage.NativeStop.Source != "codex.exec.fatal_error" || usage.NativeStop.ObservedAt.IsZero() {
					t.Fatal("canonical native stop missing")
				}
				data, _ := json.Marshal(usage)
				if strings.Contains(string(data), "Upgrade") || strings.Contains(string(data), "Try later") {
					t.Fatal("private native error payload retained")
				}
			}
		})
	}
}
