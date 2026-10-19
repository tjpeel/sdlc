package runusage

import (
	"fmt"
	"testing"
)

func TestSyntheticInvocationTotalsRetainPartialCoverage(t *testing.T) {
	o := NewObserver("synthetic", "hf:moonshotai/Kimi-K3")
	line := []byte(`{"type":"sdlc.synthetic_metrics","provider":"synthetic","model":"hf:moonshotai/Kimi-K3","complete":false,"compactions":2,"usage":{"input_tokens":20,"cached_input_tokens":8,"cache_creation_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":2}}`)
	o.Consume(line)
	o.Consume(line)
	u := o.Snapshot()
	if u.Aggregate == nil || *u.Aggregate.InputTokens != 20 || *u.Aggregate.CachedInputTokens != 8 || u.CompletedTurns != 1 || u.Compactions != 2 || u.Completeness != "partial" {
		t.Fatalf("wrong native totals: %+v", u)
	}
	if u.Aggregate.Scope != "attempt_total" || u.Context != nil {
		t.Fatal("invocation accounting confused with context occupancy")
	}
	if err := validateUsage(u); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticResumedSessionSumsInvocationTotals(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("incomplete_first_%t", incomplete), func(t *testing.T) {
			d := dir(t)
			for i, input := range []int64{100, 50} {
				a := attempt(i+1, "implementation", "synthetic-session", input)
				a.Provider, a.Effort = "synthetic", "default"
				a.Resumed = i > 0
				if i > 0 {
					a.Role = "repair"
				}
				a.Usage.Aggregate.Source = "opencode.message.updated"
				a.Usage.Aggregate.Scope = "attempt_total"
				if incomplete && i == 0 {
					a.Usage.Completeness = "incomplete"
				}
				if err := SaveAttempt(d, a); err != nil {
					t.Fatal(err)
				}
			}
			s, err := LoadSummary(d, "run-one", 2)
			if err != nil {
				t.Fatal(err)
			}
			if s.UnknownBaseline != 0 || len(s.Sessions) != 1 || len(s.Sessions[0].Invocations) != 2 {
				t.Fatalf("unexpected session accounting: %+v", s)
			}
			session := s.Sessions[0]
			if incomplete {
				if session.Completeness != "incomplete" || session.Tokens != nil {
					t.Fatalf("earlier incomplete invocation lost: %+v", session)
				}
				return
			}
			if session.Completeness != "reported" || session.Tokens == nil || *session.Tokens.InputTokens != 150 {
				t.Fatalf("invocation totals not summed: %+v", session)
			}
			var total int64
			for _, group := range s.Groups {
				if group.Tokens.InputTokens != nil {
					total += *group.Tokens.InputTokens
				}
			}
			if total != 150 {
				t.Fatalf("group invocation totals = %d, want 150", total)
			}
		})
	}
}
