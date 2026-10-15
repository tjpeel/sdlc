package runusage

import (
	"fmt"
	"regexp"
	"sort"
)

// SessionSummary uses a report-local ordinal, never the native session ID.
// Tokens are the latest complete native cumulative checkpoint, not a sum of attempts.
type SessionSummary struct {
	Number           int                   `json:"number"`
	Provider         string                `json:"provider"`
	IdentityObserved bool                  `json:"identity_observed"`
	Completeness     string                `json:"completeness"`
	Tokens           *TokenUsage           `json:"tokens,omitempty"`
	Models           map[string]TokenUsage `json:"models,omitempty"`
	Invocations      []SessionInvocation   `json:"invocations"`
	Agents           []AgentEvidence       `json:"subagents"`
	AgentsTruncated  bool                  `json:"subagents_truncated,omitempty"`
}
type SessionInvocation struct {
	ReviewEffortSelection *ReviewEffortSelection `json:"review_effort_selection,omitempty"`

	FastModeState   string `json:"native_fast_mode_state,omitempty"`
	Attempt         int    `json:"attempt"`
	Role            string `json:"role"`
	Outcome         string `json:"outcome"`
	RequestedModel  string `json:"requested_model"`
	ReportedModel   string `json:"reported_model,omitempty"`
	RequestedEffort string `json:"requested_effort,omitempty"`
	ReportedEffort  string `json:"reported_effort"`
	RequestedSpeed  string `json:"requested_speed,omitempty"`
	EffectiveSpeed  string `json:"effective_speed"`
}

func sessionSummaries(records []Attempt) []SessionSummary {
	sessions := []SessionSummary{}
	indexes := map[string]int{}
	agents := []map[string]*AgentEvidence{}
	for _, a := range records {
		key := a.Provider + "\x00" + a.ImageID + "\x00" + a.SessionID
		if a.SessionID == "" {
			key += fmt.Sprintf("\x00%d-%s", a.Attempt, a.Role)
		}
		index, exists := indexes[key]
		if !exists {
			index = len(sessions)
			indexes[key] = index
			sessions = append(sessions, SessionSummary{Number: index + 1, Provider: a.Provider, IdentityObserved: a.SessionID != "", Agents: []AgentEvidence{}})
			agents = append(agents, map[string]*AgentEvidence{})
		}
		s := &sessions[index]
		speed := a.EffectiveSpeed
		if speed == "" {
			speed = "unknown"
		}
		s.Invocations = append(s.Invocations, SessionInvocation{ReviewEffortSelection: a.ReviewEffortSelection, FastModeState: a.Usage.FastModeState, Attempt: a.Attempt, Role: a.Role, Outcome: a.Outcome, RequestedModel: a.Model, ReportedModel: a.Usage.ReportedModel, RequestedEffort: a.Effort, ReportedEffort: "unknown", RequestedSpeed: a.RequestedSpeed, EffectiveSpeed: speed})
		s.Tokens, s.Models = nil, nil
		s.Completeness = "incomplete"
		if totals, known := wholeTree(a.Usage, a.Provider); known && a.Usage.Completeness == "observed" && a.Outcome != "running" {
			s.Tokens = &totals
			s.Models = a.Usage.Models
			s.Completeness = "reported"
		}
		s.AgentsTruncated = s.AgentsTruncated || a.Usage.AgentsTruncated
		for key, value := range a.Usage.Agents {
			if value == nil {
				continue
			}
			old := agents[index][key]
			if old == nil {
				if len(agents[index]) >= maxAgents {
					s.AgentsTruncated = true
					continue
				}
				copy := *value
				copy.TaskKey = ""
				copy.ReportedModels = append([]string{}, value.ReportedModels...)
				agents[index][key] = &copy
			} else {
				if value.Type != "" {
					old.Type = value.Type
				}
				if value.RequestedModel != "" {
					old.RequestedModel = value.RequestedModel
				}
				if value.Status != "" {
					old.Status = value.Status
				}
				if value.FinalRequestTokens != nil {
					old.FinalRequestTokens = value.FinalRequestTokens
				}
				if value.FinalRequestUsage != nil {
					old.FinalRequestUsage = value.FinalRequestUsage
				}
				if value.TotalTokens != nil {
					old.TotalTokens = value.TotalTokens
				}
				if value.ToolUses != nil {
					old.ToolUses = value.ToolUses
				}
				if value.DurationMS != nil {
					old.DurationMS = value.DurationMS
				}
				old.ModelsTruncated = old.ModelsTruncated || value.ModelsTruncated
				for _, model := range value.ReportedModels {
					addAgentModel(old, model)
				}
			}
		}
	}
	for index := range sessions {
		keys := []string{}
		for key := range agents[index] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ordinals := map[string]int{}
		for i, key := range keys {
			ordinals[key] = i + 1
		}
		for _, key := range keys {
			copy := *agents[index][key]
			copy.Parent = ordinals[copy.ParentKey]
			copy.ParentKey = ""
			sessions[index].Agents = append(sessions[index].Agents, copy)
		}
	}
	return sessions
}

var digestKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateAgents(u Usage) error {
	if len(u.Agents) > maxAgents {
		return fmt.Errorf("too many agent observations")
	}
	for key, a := range u.Agents {
		if !digestKey.MatchString(key) || a == nil {
			return fmt.Errorf("invalid agent identity")
		}
		if a.Parent != 0 || a.ParentKey != "" && !digestKey.MatchString(a.ParentKey) {
			return fmt.Errorf("invalid agent parent identity")
		}
		if a.TaskKey != "" && !digestKey.MatchString(a.TaskKey) {
			return fmt.Errorf("invalid agent task identity")
		}
		if (a.ReportedEffort != "" && a.ReportedEffort != "unknown") || (a.EffectiveSpeed != "" && a.EffectiveSpeed != "unknown") {
			return fmt.Errorf("unsupported agent settings evidence")
		}
		if a.Source != "codex.collab_tool_call" && a.Source != "claude.agent_tool" {
			return fmt.Errorf("invalid agent source")
		}
		if a.Type != "" && !safeID.MatchString(a.Type) || a.RequestedModel != "" && !safeModel(a.RequestedModel) || len(a.ReportedModels) > maxAgentModels {
			return fmt.Errorf("invalid agent metadata")
		}
		for _, model := range a.ReportedModels {
			if !safeModel(model) {
				return fmt.Errorf("invalid agent model")
			}
		}
		if a.FinalRequestUsage != nil {
			t := a.FinalRequestUsage
			if t.Source != "claude.agent_tool_result" || t.Scope != "final_request" {
				return fmt.Errorf("invalid child request source")
			}
			for _, p := range tokenFields(t) {
				if *p != nil && (**p < 0 || **p > maxTokenCount) {
					return fmt.Errorf("invalid child request tokens")
				}
			}
		}
		switch a.Status {
		case "", "pending_init", "pending", "running", "interrupted", "completed", "errored", "shutdown", "not_found", "failed", "stopped", "killed", "paused":
		default:
			return fmt.Errorf("invalid agent status")
		}
		for _, count := range []*int64{a.TotalTokens, a.ToolUses, a.DurationMS, a.FinalRequestTokens} {
			if count != nil && (*count < 0 || *count > maxTokenCount) {
				return fmt.Errorf("invalid agent counter")
			}
		}
	}
	return nil
}
