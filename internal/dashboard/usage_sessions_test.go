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
	for _, want := range []string{"Subagents: 1 observed", "claude/child-model | read_low: 1 agent", "task tokens 120"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("completion summary missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "unknown") || strings.Contains(out.String(), "Attempt 1") {
		t.Fatal("verbose missing fields returned", out.String())
	}

	out.Reset()
	WriteUsageSettings(&out, runusage.Group{Effort: "low", RequestedSpeed: "fast", EffectiveSpeed: "unknown", Models: map[string]runusage.TokenUsage{"child-model": {InputTokens: &n}}})
	if !strings.Contains(out.String(), "reasoning low; speed fast") || !strings.Contains(out.String(), "Observed model child-model: input 120") {
		t.Fatal(out.String())
	}
}

func TestUsageOverviewKeepsKeyEvidenceWithoutRepeatingMissingFields(t *testing.T) {
	input, cached, output, peak := int64(120237), int64(74112), int64(540), int64(48000)
	summary := runusage.Summary{Attempts: 4, ElapsedMS: 123000, Failed: 1, Groups: []runusage.Group{{Provider: "codex", Model: "example-model", Role: "repair", Effort: "medium", RequestedSpeed: "normal", EffectiveSpeed: "unknown", Attempts: 2, MeasuredAttempts: 2, ElapsedMS: 61000, PeakContextTokens: &peak, Tokens: runusage.TokenUsage{InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output}}}, Sessions: []runusage.SessionSummary{{Provider: "codex", Number: 1, Tokens: &runusage.TokenUsage{InputTokens: &input}}}, Subscriptions: map[string]*runusage.SubscriptionUsage{"claude": {}}}
	var out bytes.Buffer
	WriteUsageOverview(&out, summary)
	for _, want := range []string{"4 attempts across 1 session", "1 failed attempt", "repair: codex/example-model | medium effort | normal speed", "2 attempts, 1m1s", "in 120,237, cached 74,112, out 540", "peak context 48,000", "none observed", "Subscription measurements unavailable: claude"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	for _, unwanted := range []string{"unknown", "Session 1", "Native fast-mode", "incomplete 0", "Attempt 1"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("noise %q in %s", unwanted, out.String())
		}
	}
	if strings.Count(out.String(), "120,237") != 1 || strings.Count(out.String(), "\n") > 8 {
		t.Fatal("summary repeated evidence", out.String())
	}
}
