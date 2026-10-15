package runusage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestClaudeDelegationEvidenceIsSeparateAndPrivate(t *testing.T) {
	o := NewObserver("claude", "main-model")
	read(o, `{"type":"system","subtype":"init","session_id":"private-session","model":"main-model"}`)
	read(o, `{"type":"assistant","session_id":"private-session","message":{"model":"main-model","content":[{"type":"tool_use","name":"Agent","id":"private-tool","input":{"subagent_type":"read_low","model":"sonnet","prompt":"private-prompt","description":"private-description"}}]}}`)
	read(o, `{"type":"assistant","parent_tool_use_id":"private-tool","session_id":"child-session","message":{"model":"child-model","content":[{"type":"tool_use","name":"Agent","id":"private-nested","input":{"subagent_type":"read_high"}}],"usage":{"input_tokens":999}}}`)
	read(o, `{"type":"system","subtype":"task_started","session_id":"private-session","task_id":"private-task","tool_use_id":"private-tool","description":"private-description"}`)
	event := `{"type":"system","subtype":"task_notification","session_id":"private-session","task_id":"private-task","tool_use_id":"private-tool","status":"completed","usage":{"total_tokens":120,"tool_uses":3,"duration_ms":90},"summary":"private-summary","output_file":"private-path"}`
	read(o, event)
	read(o, event)
	read(o, `{"type":"system","subtype":"task_updated","session_id":"private-session","task_id":"private-task","patch":{"status":"killed","other":"private-patch"}}`)
	read(o, `{"type":"system","subtype":"task_notification","task_id":"shell-task","usage":{"total_tokens":900}}`)
	read(o, `{"type":"assistant","parent_tool_use_id":"unrelated","message":{"model":"foreign-model","content":[{"type":"tool_use","name":"Agent","id":"foreign-tool"}]}}`)
	u := o.Snapshot()
	a := u.Agents[agentKey("private-tool")]
	if len(u.Agents) != 2 || a == nil || a.Status != "killed" || a.RequestedModel != "sonnet" || len(a.ReportedModels) != 1 || a.ReportedModels[0] != "child-model" || a.TotalTokens == nil || *a.TotalTokens != 120 || *a.ToolUses != 3 || u.ReportedModel != "main-model" || !u.ModelMatches || u.Aggregate != nil || o.SessionID() != "private-session" {
		t.Fatalf("delegation scope lost: %+v %+v", u, a)
	}
	if u.Agents[agentKey("private-nested")].ParentKey != agentKey("private-tool") {
		t.Fatal("nested delegate not associated")
	}
	a.TotalTokens = n(999)
	if *o.Snapshot().Agents[agentKey("private-tool")].TotalTokens != 120 {
		t.Fatal("snapshot aliases delegate counters")
	}
	data, _ := json.Marshal(o.Snapshot())
	for _, secret := range []string{"private-tool", "private-session", "private-task", "private-prompt", "private-description", "private-summary", "private-path", "private-patch", "foreign-model"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("unprojected event data retained", secret)
		}
	}
}

func TestCodexDelegationLifecycleWithoutInventedUsage(t *testing.T) {
	o := NewObserver("codex", "main-model")
	read(o, `{"type":"thread.started","thread_id":"private-root"}`)
	for _, status := range []string{"running", "completed"} {
		event := fmt.Sprintf(`{"type":"item.completed","item":{"id":"call","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"private-root","receiver_thread_ids":["private-child"],"agents_states":{"private-child":{"status":%q,"message":"private-message"}},"prompt":"private-prompt","status":"completed"}}`, status)
		read(o, event)
		read(o, event)
	}
	read(o, `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":30,"output_tokens":10}}`)
	u := o.Snapshot()
	a := u.Agents[agentKey("private-child")]
	if len(u.Agents) != 1 || a == nil || a.Status != "completed" || a.TotalTokens != nil || len(a.ReportedModels) != 0 || a.RequestedModel != "" || a.ReportedEffort != "unknown" || a.EffectiveSpeed != "unknown" || *u.Aggregate.InputTokens != 100 {
		t.Fatalf("invented or lost child evidence %+v", a)
	}
	data, _ := json.Marshal(u)
	if strings.Contains(string(data), "private-") {
		t.Fatal("private collab payload retained", string(data))
	}
}

func TestAgentCollectionBoundsAndSessionReset(t *testing.T) {
	o := NewObserver("codex", "main")
	read(o, `{"type":"thread.started","thread_id":"one"}`)
	for i := 0; i < maxAgents+2; i++ {
		read(o, fmt.Sprintf(`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"spawn_agent","receiver_thread_ids":["child-%d"]}}`, i))
	}
	if u := o.Snapshot(); len(u.Agents) != maxAgents || !u.AgentsTruncated {
		t.Fatal("collection bound not visible")
	}
	read(o, `{"type":"thread.started","thread_id":"two"}`)
	if len(o.Snapshot().Agents) != 0 || o.Snapshot().AgentsTruncated {
		t.Fatal("child evidence crossed session boundary")
	}
}

func TestClaudeToolResultsExposeResolvedModelsAndFinalRequestScope(t *testing.T) {
	o := NewObserver("claude", "main")
	read(o, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"private-call","input":{"subagent_type":"reader","model":"sonnet"}}]}}`)
	event := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"private-call","content":"private-answer"}]},"tool_use_result":{"status":"completed","resolvedModel":"resolved-model","modelsUsed":["resolved-model","fallback-model"],"totalTokens":45,"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":5,"output_tokens":10},"content":"private-result"}}`
	read(o, event)
	read(o, event)
	u := o.Snapshot()
	a := u.Agents[agentKey("private-call")]
	if a == nil || a.Status != "completed" || len(a.ReportedModels) != 2 || a.TotalTokens != nil || a.FinalRequestTokens == nil || *a.FinalRequestTokens != 45 || a.FinalRequestUsage == nil || *a.FinalRequestUsage.InputTokens != 10 || a.FinalRequestUsage.Scope != "final_request" || u.Aggregate != nil {
		t.Fatalf("final request confused with whole-agent usage: %+v", a)
	}
	if err := validateUsage(u); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(u)
	if strings.Contains(string(data), "private-") {
		t.Fatal("private result retained", string(data))
	}
}
