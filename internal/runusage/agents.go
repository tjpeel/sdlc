package runusage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const maxAgents = 64
const maxAgentModels = 16

// AgentEvidence is an observation, never an inferred inheritance of the parent's settings.
// Task token totals remain separate from session totals and observed child models.
type AgentEvidence struct {
	FinalRequestTokens *int64      `json:"final_request_tokens,omitempty"`
	FinalRequestUsage  *TokenUsage `json:"final_request_usage,omitempty"`
	ParentKey          string      `json:"parent_key,omitempty"`
	Parent             int         `json:"parent,omitempty"`
	TaskKey            string      `json:"task_key,omitempty"`
	ModelsTruncated    bool        `json:"models_truncated,omitempty"`
	ReportedEffort     string      `json:"reported_effort"`
	EffectiveSpeed     string      `json:"effective_speed"`
	Type               string      `json:"type,omitempty"`
	RequestedModel     string      `json:"requested_model,omitempty"`
	ReportedModels     []string    `json:"reported_models,omitempty"`
	Status             string      `json:"status,omitempty"`
	TotalTokens        *int64      `json:"total_tokens,omitempty"`
	ToolUses           *int64      `json:"tool_uses,omitempty"`
	DurationMS         *int64      `json:"duration_ms,omitempty"`
	Source             string      `json:"source"`
}

func agentKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func agent(usage *Usage, id, source string) *AgentEvidence {
	if id == "" || len(id) > 256 {
		return nil
	}
	if usage.Agents == nil {
		usage.Agents = map[string]*AgentEvidence{}
	}
	key := agentKey(id)
	if a := usage.Agents[key]; a != nil {
		return a
	}
	if len(usage.Agents) >= maxAgents {
		usage.AgentsTruncated = true
		return nil
	}
	a := &AgentEvidence{Source: source, ReportedEffort: "unknown", EffectiveSpeed: "unknown"}
	usage.Agents[key] = a
	return a
}

func addAgentModel(a *AgentEvidence, model string) {
	if !safeModel(model) {
		return
	}
	for _, old := range a.ReportedModels {
		if old == model {
			return
		}
	}
	if len(a.ReportedModels) < maxAgentModels {
		a.ReportedModels = append(a.ReportedModels, model)
		sort.Strings(a.ReportedModels)
	} else {
		a.ModelsTruncated = true
	}
}

// observeAgents projects only protocol metadata. Prompts, descriptions, arguments,
// messages, tool results and native identifiers never enter the report.
func observeAgents(line []byte, provider string, usage *Usage) {
	if provider == "codex" {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type      string   `json:"type"`
				Tool      string   `json:"tool"`
				Receivers []string `json:"receiver_thread_ids"`
				States    map[string]struct {
					Status string `json:"status"`
				} `json:"agents_states"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &event) != nil || (event.Type != "item.started" && event.Type != "item.updated" && event.Type != "item.completed") || event.Item.Type != "collab_tool_call" {
			return
		}
		switch event.Item.Tool {
		case "spawn_agent", "send_input", "wait", "close_agent":
		default:
			return
		}
		for _, id := range event.Item.Receivers {
			a := agent(usage, id, "codex.collab_tool_call")
			if a == nil {
				continue
			}
			switch status := event.Item.States[id].Status; status {
			case "pending_init", "running", "interrupted", "completed", "errored", "shutdown", "not_found":
				a.Status = status
			}
		}
		return
	}
	if provider != "claude" {
		return
	}
	var event struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		Parent     string `json:"parent_tool_use_id"`
		ToolID     string `json:"tool_use_id"`
		Status     string `json:"status"`
		TaskID     string `json:"task_id"`
		ToolResult struct {
			Status string          `json:"status"`
			Model  string          `json:"resolvedModel"`
			Models []string        `json:"modelsUsed"`
			Total  json.RawMessage `json:"totalTokens"`
			Usage  json.RawMessage `json:"usage"`
		} `json:"tool_use_result"`
		Patch struct {
			Status string `json:"status"`
		} `json:"patch"`
		Usage struct {
			Total    json.RawMessage `json:"total_tokens"`
			Tools    json.RawMessage `json:"tool_uses"`
			Duration json.RawMessage `json:"duration_ms"`
		} `json:"usage"`
		Message struct {
			Model   string `json:"model"`
			Content []struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				ID     string `json:"id"`
				ToolID string `json:"tool_use_id"`
				Input  struct {
					Type  string `json:"subagent_type"`
					Model string `json:"model"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &event) != nil {
		return
	}
	parentKey := ""
	if event.Type == "assistant" && event.Parent != "" {
		parent := usage.Agents[agentKey(event.Parent)]
		if parent == nil {
			return
		}
		addAgentModel(parent, event.Message.Model)
		parentKey = agentKey(event.Parent)
	}

	if event.Type == "assistant" {
		for _, block := range event.Message.Content {
			if block.Type != "tool_use" || (block.Name != "Agent" && block.Name != "Task") {
				continue
			}
			a := agent(usage, block.ID, "claude.agent_tool")
			if a == nil {
				continue
			}
			a.ParentKey = parentKey
			if safeID.MatchString(block.Input.Type) {
				a.Type = block.Input.Type
			}
			if safeModel(block.Input.Model) {
				a.RequestedModel = block.Input.Model
			}
		}
	}
	if event.Type == "user" {
		for _, block := range event.Message.Content {
			if block.Type != "tool_result" {
				continue
			}
			a := usage.Agents[agentKey(block.ToolID)]
			if a == nil {
				continue
			}
			addAgentModel(a, event.ToolResult.Model)
			for _, model := range event.ToolResult.Models {
				addAgentModel(a, model)
			}
			switch event.ToolResult.Status {
			case "completed":
				a.Status = "completed"
			case "async_launched":
				a.Status = "running"
			}
			if value, valid := parseCount(event.ToolResult.Total); valid && value != nil {
				a.FinalRequestTokens = value
			}
			if value, valid := counts(event.ToolResult.Usage, "cache_read_input_tokens"); valid && value != nil {
				value.Source = "claude.agent_tool_result"
				value.Scope = "final_request"
				a.FinalRequestUsage = value
			}
		}
	}
	if event.Type == "system" && (event.Subtype == "task_started" || event.Subtype == "task_progress" || event.Subtype == "task_notification" || event.Subtype == "task_updated") {
		a := usage.Agents[agentKey(event.ToolID)]
		if a == nil && event.TaskID != "" && len(event.TaskID) <= 256 {
			for _, candidate := range usage.Agents {
				if candidate.TaskKey == agentKey(event.TaskID) {
					a = candidate
					break
				}
			}
		}
		if a == nil {
			return
		} // Shell tasks are not subagents.
		if event.TaskID != "" && len(event.TaskID) <= 256 {
			a.TaskKey = agentKey(event.TaskID)
		}
		if event.Subtype == "task_updated" {
			event.Status = event.Patch.Status
		}
		if event.Subtype == "task_started" {
			a.Status = "running"
		}
		switch event.Status {
		case "pending", "running", "paused", "completed", "failed", "stopped", "killed":
			a.Status = event.Status
		}
		for _, field := range []struct {
			raw json.RawMessage
			dst **int64
		}{{event.Usage.Total, &a.TotalTokens}, {event.Usage.Tools, &a.ToolUses}, {event.Usage.Duration, &a.DurationMS}} {
			if value, valid := parseCount(field.raw); valid && value != nil {
				*field.dst = value
			}
		}
	}
}
