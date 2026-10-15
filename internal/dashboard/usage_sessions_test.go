package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestSessionDetailsDistinguishSettingsModelsAndChildCounters(t *testing.T) {
	n := int64(120)
	summary := runusage.Summary{Sessions: []runusage.SessionSummary{{Number: 1, Provider: "claude", IdentityObserved: true, Completeness: "reported", Invocations: []runusage.SessionInvocation{{Attempt: 1, Role: "implementation", Outcome: "completed", RequestedModel: "main-model", ReportedModel: "main-model", RequestedEffort: "high", RequestedSpeed: "normal", ReportedEffort: "unknown", EffectiveSpeed: "unknown"}}, Agents: []runusage.AgentEvidence{{Type: "read_low", RequestedModel: "sonnet", ReportedModels: []string{"child-model"}, Status: "completed", TotalTokens: &n, ReportedEffort: "unknown", EffectiveSpeed: "unknown"}}}}}
	var out bytes.Buffer
	WriteSessions(&out, summary)
	for _, want := range []string{"requested model main-model, reasoning high, speed normal", "reported model main-model, reasoning unknown, speed unknown", "requested model sonnet; reported models child-model", "task tokens 120", "never add them to native session totals", "partial visibility"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	WriteUsageOverview(&out, summary)
	for _, want := range []string{"requested model main-model, reasoning high, speed normal", "reported model main-model", "requested model sonnet; reported models child-model", "reasoning unknown; speed unknown", "task tokens 120"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("completion summary missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	WriteUsageSettings(&out, runusage.Group{Effort: "low", RequestedSpeed: "fast", EffectiveSpeed: "unknown", Models: map[string]runusage.TokenUsage{"child-model": {InputTokens: &n}}})
	if !strings.Contains(out.String(), "reasoning low; speed fast") || !strings.Contains(out.String(), "Observed model child-model: input 120") {
		t.Fatal(out.String())
	}
}
