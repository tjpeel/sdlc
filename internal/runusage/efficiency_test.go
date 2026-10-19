package runusage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPromptBytesAreNotTokensAndDoNotRetainText(t *testing.T) {
	m := MeasurePrompt("policy\nfeedback é", "feedback é")
	if m.Bytes != 18 || m.FeedbackBytes != 11 {
		t.Fatalf("UTF-8 bytes: %+v", m)
	}
	encoded, _ := json.Marshal(m)
	if strings.Contains(string(encoded), "policy") {
		t.Fatal("prompt contents retained")
	}
	if MeasurePrompt("sanitized review", "private feedback").FeedbackBytes != 0 {
		t.Fatal("feedback not supplied was counted")
	}
}

func TestStreamCountsCompletedRootResultsOnceAndReportsGaps(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			o := NewObserver(provider, "model")
			var event string
			if provider == "codex" {
				read(o, `{"type":"item.updated","item":{"id":"tool-one","type":"command_execution","aggregated_output":"intermediate"}}`)
				event = `{"type":"item.completed","item":{"id":"tool-one","type":"command_execution","command":"cat SKILL.md","aggregated_output":"é"}}`
			} else {
				read(o, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-one","name":"Read"}]}}`)
				read(o, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-one","name":"Read"}]}}`)
				event = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-one","content":"é"}]}}`
				read(o, `{"type":"user","parent_tool_use_id":"child","message":{"content":[{"type":"tool_result","tool_use_id":"child-tool","content":"private child"}]}}`)
			}
			read(o, event)
			read(o, event)
			read(o, "not-json")
			_, _ = o.Write([]byte(strings.Repeat("x", maxEventLine+1) + "\n"))
			m := o.Snapshot().Stream
			wantBytes := int64(2)
			if provider == "claude" {
				wantBytes = 4
			} // JSON string includes its quotes.
			if m.ToolCalls != 1 || m.ToolResultBytes != wantBytes || m.MalformedLines != 1 || m.OversizedLines != 1 {
				t.Fatalf("incorrect coverage/counts: %+v", m)
			}
			encoded, _ := json.Marshal(m)
			if strings.Contains(string(encoded), "SKILL") || strings.Contains(string(encoded), "private") {
				t.Fatal("raw tool data retained")
			}
		})
	}
}

func TestSupplementaryContextDoesNotChangeAccountingOrNativeTiming(t *testing.T) {
	o := NewObserver("codex", "model")
	read(o, `{"type":"thread.started","thread_id":"example-session"}`)
	read(o, `{"type":"sdlc.native_session_recovery","provider":"codex","phase":"completed"}`)
	read(o, `{"type":"sdlc.codex_context","provider":"codex","coverage":"complete","compactions":1,"observations":2,"peak_input_tokens":300,"context":{"source":"codex.rollout.last_token_usage","scope":"request_context","input_tokens":200,"tokens":200,"window":1000}}`)
	read(o, `{"type":"turn.completed","usage":{"input_tokens":9000,"cached_input_tokens":8000,"output_tokens":100}}`)
	u := o.Snapshot()
	if u.Context == nil || *u.Context.Tokens != 200 || *u.PeakContextTokens != 300 || u.Compactions != 1 || u.CompactionCoverage != "complete" || *u.Aggregate.InputTokens != 9000 {
		t.Fatalf("context/cumulative accounting conflated: %+v", u)
	}
	if o.Timing().Events != 2 {
		t.Fatalf("numeric SDLC events changed native timing: %+v", o.Timing())
	}
	if err := validateUsage(u); err != nil {
		t.Fatal(err)
	}
	read(o, `{"type":"sdlc.codex_context","provider":"codex","coverage":"complete","context":{"source":"codex.rollout.last_token_usage","scope":"request_context","input_tokens":-1,"tokens":-1}}`)
	if *o.Snapshot().Context.Tokens != 200 {
		t.Fatal("invalid sample replaced valid observation")
	}
}

func TestEfficiencyRoundTripAndLegacyCoverage(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	a.OptimizerMode = "off"
	a.Prompt = MeasurePrompt("policy feedback", "feedback")
	a.Usage.Stream = &StreamMetrics{Bytes: 42, ToolCalls: 1, ToolResultBytes: 3}
	a.Usage.Context = &ContextUsage{Source: "codex.rollout.last_token_usage", Scope: "request_context", Tokens: n(50), InputTokens: n(50), Window: n(1000), ObservedAt: time.Now().UTC()}
	a.Usage.CompactionCoverage = "complete"
	if err := SaveAttempt(d, a); err != nil {
		t.Fatal(err)
	}
	b := attempt(2, "implementation", "session", 150)
	b.OptimizerMode = "off"
	b.Resumed = true
	if err := SaveAttempt(d, b); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSummary(d, "run-one", 2)
	if err != nil {
		t.Fatal(err)
	}
	e := s.Groups[0].Efficiency
	if e.PromptAttempts != 1 || e.MissingPrompts != 1 || e.PromptBytes != 15 || e.StreamAttempts != 1 || e.MissingStreams != 1 || e.ContextAttempts != 1 || e.MissingContext != 1 || e.CompleteCompactions != 1 || e.UnknownCompactions != 1 {
		t.Fatalf("legacy coverage hidden: %+v", e)
	}
	if *s.Groups[0].Tokens.InputTokens != 150 {
		t.Fatal("observations changed resumed token deltas")
	}
	if s.Sessions[0].Invocations[0].Prompt == nil || s.Sessions[0].Invocations[0].Context == nil {
		t.Fatal("per-invocation evidence missing from JSON boundary")
	}
}
