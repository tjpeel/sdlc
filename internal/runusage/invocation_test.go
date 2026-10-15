package runusage

import (
	"testing"
	"time"
)

func TestInvocationUsageSubtractsPriorSessionAndExcludesOtherInvocations(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "native-one", 100)
	b := attempt(2, "repair", "native-one", 150)
	b.Resumed = true
	c := attempt(3, "review", "native-two", 30)
	c.Provider = "claude"
	c.Usage.Aggregate.CacheCreationInputTokens = n(0)
	c.Usage.Models = map[string]TokenUsage{"model": *c.Usage.Aggregate}
	future := attempt(4, "repair", "native-one", 200)
	future.Resumed = true
	old := &AgentEvidence{Source: "codex.collab_tool_call", Status: "completed", ReportedEffort: "unknown", EffectiveSpeed: "unknown"}
	a.Usage.Agents = map[string]*AgentEvidence{agentKey("old-child"): old}
	b.Usage.Agents = map[string]*AgentEvidence{agentKey("old-child"): old, agentKey("new-child"): {Source: "codex.collab_tool_call", Status: "completed", ReportedEffort: "unknown", EffectiveSpeed: "unknown"}}
	quota := func(a Attempt, before, after float64) *SubscriptionUsage {
		o := NewObserver("codex", "model")
		reset := a.StartedAt.Add(time.Hour).Unix()
		read(o, quotaEvent("before", before, reset, a.StartedAt))
		read(o, quotaEvent("after", after, reset, a.StartedAt.Add(time.Second)))
		return o.Snapshot().Subscription
	}
	a.Usage.Subscription = quota(a, 89, 87)
	b.Usage.Subscription = quota(b, 87, 84)
	for _, r := range []Attempt{a, b, c, future} {
		if err := SaveAttempt(d, r); err != nil {
			t.Fatal(err)
		}
	}
	s, err := LoadInvocationSummary(d, "run-one", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if s.Attempts != 2 || s.Missing != 0 || s.ElapsedMS != 2000 || len(s.Sessions) != 2 {
		t.Fatalf("wrong interval: %+v", s)
	}
	for _, g := range s.Groups {
		want := int64(30)
		if g.Role == "repair" {
			want = 50
		}
		if g.Tokens.InputTokens == nil || *g.Tokens.InputTokens != want {
			t.Fatalf("old cumulative tokens charged: %+v", g)
		}
	}
	if len(s.Sessions[0].Agents) != 1 || len(s.Sessions[0].Invocations) != 1 || s.Sessions[0].Invocations[0].Attempt != 2 || s.Sessions[0].Tokens != nil {
		t.Fatal("historical child/session evidence retained", s.Sessions[0])
	}
	change := s.Subscriptions["codex"].Windows["codex/primary"]
	if change.UsedPercentagePoints == nil || *change.UsedPercentagePoints != 3 {
		t.Fatal("historical subscription interval included", change)
	}
	full, err := LoadSummary(d, "run-one", 4)
	if err != nil || full.Attempts != 4 || len(full.Sessions[0].Agents) != 2 {
		t.Fatal("full history changed", full, err)
	}
	zero, err := LoadInvocationSummary(d, "run-one", 4, 4)
	if err != nil || zero.Attempts != 0 || len(zero.Groups) != 0 || len(zero.Sessions) != 0 || len(zero.Subscriptions) != 0 {
		t.Fatal("zero-work resume replayed history", zero, err)
	}
}

func TestInvocationUsageKeepsMissingBaselineUnknown(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "native-one", 100)
	a.Outcome = "running"
	a.EndedAt = time.Time{}
	b := attempt(2, "repair", "native-one", 150)
	b.Resumed = true
	for _, r := range []Attempt{a, b} {
		if err := SaveAttempt(d, r); err != nil {
			t.Fatal(err)
		}
	}
	s, err := LoadInvocationSummary(d, "run-one", 1, 3)
	if err != nil || s.Missing != 1 || s.UnknownBaseline != 1 || len(s.Groups) != 1 || s.Groups[0].MeasuredAttempts != 0 || s.Groups[0].Tokens.InputTokens != nil {
		t.Fatal("missing baseline became zero or full cumulative charge", s, err)
	}
}

func TestInvocationChildCountersUseOnlyNewObservations(t *testing.T) {
	a := attempt(1, "implementation", "native-one", 100)
	a.Usage.Agents = map[string]*AgentEvidence{"child": {TotalTokens: n(100), DurationMS: n(1000), ToolUses: n(4)}}
	b := attempt(2, "repair", "native-one", 150)
	b.Usage.Agents = map[string]*AgentEvidence{"child": {TotalTokens: n(140), DurationMS: n(1500), ToolUses: n(6)}}
	s := invocationSessions([]Attempt{a, b}, 1, 2, 0)
	if len(s) != 1 || len(s[0].Agents) != 1 || *s[0].Agents[0].TotalTokens != 40 || *s[0].Agents[0].DurationMS != 500 || *s[0].Agents[0].ToolUses != 2 {
		t.Fatal("child counters charged previous work", s)
	}
	b.Attempt = 3
	b.Resumed = true
	missing := invocationSessions([]Attempt{a, b}, 2, 3, 2)
	if missing[0].Agents[0].TotalTokens != nil {
		t.Fatal("missing boundary charged historical child work")
	}

	if *b.Usage.Agents["child"].TotalTokens != 140 {
		t.Fatal("source metrics modified")
	}
}

func TestInvocationBaselineRequiresKnownBoundaryAndAttemptOrder(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing boundary", false: "clock skew"}[missing], func(t *testing.T) {
			d := dir(t)
			a := attempt(1, "implementation", "native-one", 100)
			b := attempt(2, "repair", "native-one", 150)
			b.Resumed = true
			c := attempt(3, "repair", "native-one", 180)
			c.Resumed = true
			a.StartedAt = a.StartedAt.Add(time.Hour)
			a.EndedAt = a.StartedAt.Add(time.Second)
			c.StartedAt = c.StartedAt.Add(2 * time.Hour)
			c.EndedAt = c.StartedAt.Add(time.Second)
			rows := []Attempt{a, c}
			if !missing {
				rows = append(rows, b)
			}
			for _, row := range rows {
				if err := SaveAttempt(d, row); err != nil {
					t.Fatal(err)
				}
			}
			s, err := LoadInvocationSummary(d, "run-one", 2, 3)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				if s.UnknownBaseline != 1 || s.Groups[0].Tokens.InputTokens != nil {
					t.Fatal("cost before missing boundary charged to invocation", s)
				}
			} else if s.Groups[0].Tokens.InputTokens == nil || *s.Groups[0].Tokens.InputTokens != 30 {
				t.Fatal("wall-clock ordering chose stale checkpoint", s)
			}
		})
	}
}
