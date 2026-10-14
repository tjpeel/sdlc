package runusage

import (
	"encoding/json"
	"strings"
	"testing"
)

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
