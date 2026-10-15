package runusage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionsRetainSettingsAndDoNotAddResumeCheckpoints(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "private-native-session", 100)
	a.Effort = "high"
	a.RequestedSpeed = "normal"
	a.EffectiveSpeed = "unknown"
	a.Usage.Agents = map[string]*AgentEvidence{agentKey("private-child"): {Source: "codex.collab_tool_call", Status: "running"}}
	if err := SaveAttempt(d, a); err != nil {
		t.Fatal(err)
	}
	b := attempt(2, "repair", "private-native-session", 150)
	b.Resumed = true
	b.Effort = "low"
	b.RequestedSpeed = "fast"
	b.EffectiveSpeed = "unknown"
	b.Usage.Agents = map[string]*AgentEvidence{agentKey("private-child"): {Source: "codex.collab_tool_call", Status: "completed"}}
	if err := SaveAttempt(d, b); err != nil {
		t.Fatal(err)
	}
	c := attempt(3, "repair", "private-native-session", 180)
	c.Resumed = true
	c.Effort = "high"
	c.RequestedSpeed = "normal"
	c.EffectiveSpeed = "unknown"
	if err := SaveAttempt(d, c); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSummary(d, "run-one", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sessions) != 1 || len(s.Groups) != 3 || len(s.Sessions[0].Invocations) != 3 || len(s.Sessions[0].Agents) != 1 || s.Sessions[0].Agents[0].Status != "completed" || *s.Sessions[0].Tokens.InputTokens != 180 {
		t.Fatalf("resume checkpoints or settings merged incorrectly: %+v", s)
	}
	for _, g := range s.Groups {
		if g.Role == "repair" && g.Effort == "low" && (*g.Tokens.InputTokens != 50 || g.RequestedSpeed != "fast") {
			t.Fatalf("wrong attempt delta %+v", g)
		}
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), agentKey("private-child")) {
		t.Fatal("native identity leaked in session summary")
	}
	// An unfinished resume cannot make the earlier checkpoint look final.
	c = attempt(4, "repair", "private-native-session", 200)
	c.Resumed = true
	c.Usage.Completeness = "incomplete"
	if err := SaveAttempt(d, c); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSummary(d, "run-one", 4)
	if err != nil || s.Sessions[0].Tokens != nil || s.Sessions[0].Completeness != "incomplete" {
		t.Fatal("stale checkpoint represented as latest complete usage", err)
	}
}

func TestGroupsSplitReasoningAndSpeedWithIdenticalRoles(t *testing.T) {
	d := dir(t)
	for i, setting := range []struct{ effort, speed string }{{"high", "normal"}, {"low", "normal"}, {"high", "fast"}} {
		a := attempt(i+1, "implementation", "", 100)
		a.Effort = setting.effort
		a.RequestedSpeed = setting.speed
		if err := SaveAttempt(d, a); err != nil {
			t.Fatal(err)
		}
	}
	s, err := LoadSummary(d, "run-one", 3)
	if err != nil || len(s.Groups) != 3 || len(s.Sessions) != 3 {
		t.Fatal("settings or unidentified sessions conflated", s, err)
	}
	for _, session := range s.Sessions {
		if session.IdentityObserved {
			t.Fatal("invented session identity")
		}
	}
}
