package runusage

import (
	"fmt"
	"reflect"
)

// Keep replayed child observations and cumulative session checkpoints out of the
// completion report. Native attempt deltas are calculated separately by loadSummary.
func invocationSessions(records []Attempt, after, through, lastMissingPrior int) []SessionSummary {
	identity := func(a Attempt) string {
		key := a.Provider + "\x00" + a.ImageID + "\x00" + a.SessionID
		if a.SessionID == "" {
			key += fmt.Sprintf("\x00%d-%s", a.Attempt, a.Role)
		}
		return key
	}
	prior := map[string]map[string]*AgentEvidence{}
	priorAttempt := map[string]map[string]int{}
	for _, a := range records {
		if a.Attempt > after {
			continue
		}
		key := identity(a)
		if prior[key] == nil {
			prior[key] = map[string]*AgentEvidence{}
			priorAttempt[key] = map[string]int{}
		}
		for task, agent := range a.Usage.Agents {
			if agent != nil {
				prior[key][task] = agent
				priorAttempt[key][task] = a.Attempt
			}
		}
	}
	selected := []Attempt{}
	for _, a := range records {
		if a.Attempt <= after || a.Attempt > through {
			continue
		}
		copy := a
		copy.Usage.Agents = map[string]*AgentEvidence{}
		for task, agent := range a.Usage.Agents {
			if agent == nil {
				continue
			}
			old := prior[identity(a)][task]
			if old != nil && reflect.DeepEqual(old, agent) {
				continue
			}
			child := *agent
			if old != nil {
				child.TotalTokens = invocationCounter(agent.TotalTokens, old.TotalTokens)
				child.ToolUses = invocationCounter(agent.ToolUses, old.ToolUses)
				child.DurationMS = invocationCounter(agent.DurationMS, old.DurationMS)
				child.FinalRequestTokens, child.FinalRequestUsage = nil, nil
			}
			if lastMissingPrior > 0 && (old != nil && priorAttempt[identity(a)][task] <= lastMissingPrior || old == nil && a.Resumed) {
				child.TotalTokens, child.ToolUses, child.DurationMS = nil, nil, nil
				child.FinalRequestTokens, child.FinalRequestUsage = nil, nil
			}
			copy.Usage.Agents[task] = &child
		}
		selected = append(selected, copy)
	}
	sessions := sessionSummaries(selected)
	for i := range sessions {
		// These are whole native session checkpoints, not invocation usage.
		sessions[i].Tokens, sessions[i].Models = nil, nil
	}
	return sessions
}

func invocationCounter(current, baseline *int64) *int64 {
	if current == nil || baseline == nil || *current < *baseline {
		return nil
	}
	value := *current - *baseline
	return &value
}
