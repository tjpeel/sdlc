package runusage

import (
	"encoding/json"
	"strings"
	"testing"
)

func n(v int64) *int64           { return &v }
func read(o *Observer, s string) { _, _ = o.Write([]byte(s + "\n")) }
func TestNativeTotalsAndPrimaryContext(t *testing.T) {
	o := NewObserver("claude", "main-model")
	read(o, `{"type":"system","subtype":"init","session_id":"native-private","model":"main-model","claude_code_version":"2.1.287"}`)
	read(o, `{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m1","model":"main-model","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":5}}}`)
	read(o, `{"type":"assistant","parent_tool_use_id":"child","message":{"id":"child","model":"child-model","usage":{"input_tokens":999}}}`)
	read(o, `{"type":"result","usage":{"input_tokens":10,"output_tokens":3},"modelUsage":{"main-model":{"inputTokens":10,"cacheReadInputTokens":20,"cacheCreationInputTokens":5,"outputTokens":3},"child-model":{"inputTokens":50,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":7}},"total_cost_usd":0.4}`)
	o.Finish()
	u := o.Snapshot()
	if u.ReportedModel != "main-model" || u.Context == nil || *u.Context.Tokens != 35 || len(u.Models) != 2 || *u.Models["child-model"].InputTokens != 50 || *u.Aggregate.InputTokens != 10 || u.ClientVersion != "2.1.287" {
		t.Fatalf("wrong native scope: %+v", u)
	}
	u.Models["child-model"] = TokenUsage{}
	if o.Snapshot().Models["child-model"].InputTokens == nil {
		t.Fatal("snapshot aliases parser map")
	}
}
func TestCodexSubsetsAndNoInventedContext(t *testing.T) {
	o := NewObserver("codex", "m")
	s := `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":4,"output_tokens":20,"reasoning_output_tokens":10}}`
	read(o, s)
	read(o, s)
	u := o.Snapshot()
	if u.Context != nil || u.CompletedTurns != 1 || *u.Aggregate.ReasoningOutputTokens != 10 || *u.Aggregate.CacheWriteInputTokens != 4 {
		t.Fatalf("wrong subsets %+v", u)
	}
	read(o, `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":101}}`)
	if *o.Snapshot().Aggregate.CachedInputTokens != 80 {
		t.Fatal("invalid subset accepted")
	}
}
func TestQuotaOptionalFieldsAndCrash(t *testing.T) {
	o := NewObserver("claude", "m")
	read(o, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","resetsAt":1900000000,"utilization":0.7,"extra":"secret"}}`)
	read(o, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":1.2}}`)
	read(o, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"seven_day"}}`)
	read(o, `{"type":"result","subtype":"error_during_execution","usage":{"input_tokens":0},"modelUsage":{},"total_cost_usd":0}`)
	o.Finish()
	u := o.Snapshot()
	if u.Completeness != "incomplete" || u.Aggregate != nil || u.RateLimits["five_hour"].Status != "allowed_warning" || u.RateLimits["seven_day"].Utilization != nil {
		t.Fatalf("invalid quota/crash %+v", u)
	}
	b, _ := json.Marshal(u)
	if strings.Contains(string(b), "secret") {
		t.Fatal("raw quota payload persisted")
	}
}
func TestObserverFinishAndSessionChange(t *testing.T) {
	o := NewObserver("codex", "m")
	read(o, `{"type":"thread.started","thread_id":"one"}`)
	_, _ = o.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":5}}`))
	o.Finish()
	if *o.Snapshot().Aggregate.InputTokens != 5 {
		t.Fatal("final line lost")
	}
	read(o, `{"type":"thread.started","thread_id":"two"}`)
	if o.SessionID() != "two" || o.Snapshot().Aggregate != nil {
		t.Fatal("session boundary retained totals")
	}
}
