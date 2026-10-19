package runusage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Byte counts describe supplied text, not provider tokens or cache savings.
type PromptMetrics struct {
	Bytes         int64 `json:"bytes"`
	FeedbackBytes int64 `json:"feedback_bytes_subset"`
}

func MeasurePrompt(prompt, feedback string) *PromptMetrics {
	m := &PromptMetrics{Bytes: int64(len(prompt))}
	if feedback != "" && strings.Contains(prompt, feedback) {
		m.FeedbackBytes = int64(len(feedback))
	}
	return m
}

// CatalogueMetrics measures available instruction files, not files read by a model.
// No source names, paths or instruction contents enter these records.
type CatalogueMetrics struct {
	PromptBytes            int64  `json:"prompt_bytes"`
	Status                 string `json:"status"`
	SkillFiles             int64  `json:"skill_files"`
	SkillBytes             int64  `json:"skill_bytes_available"`
	AgentFiles             int64  `json:"agent_files"`
	AgentBytes             int64  `json:"agent_bytes_available"`
	SharedInstructionBytes int64  `json:"shared_instruction_bytes"`
	SchemaBytes            int64  `json:"schema_bytes"`
	Fingerprint            string `json:"fingerprint,omitempty"`
}

// StreamMetrics is invocation-local. It never enters cumulative token accounting.
// Only supported root events contribute tool measurements. Oversized/malformed
// events leave explicit coverage gaps rather than invented zero tool usage.
type StreamMetrics struct {
	UnattributedToolEvents int64 `json:"unattributed_tool_events"`
	Bytes                  int64 `json:"bytes"`
	Events                 int64 `json:"events"`
	MalformedLines         int64 `json:"malformed_lines"`
	OversizedLines         int64 `json:"oversized_lines"`
	ToolCalls              int64 `json:"tool_calls_observed"`
	ToolResultBytes        int64 `json:"tool_result_bytes_observed"`
	InstructionCommands    int64 `json:"instruction_named_commands"`
	InstructionOutputBytes int64 `json:"instruction_named_command_output_bytes"`
}

type EfficiencySummary struct {
	ContextCheckpointErrors int64         `json:"context_checkpoint_errors"`
	PromptAttempts          int           `json:"prompt_attempts"`
	MissingPrompts          int           `json:"missing_prompt_attempts"`
	PromptBytes             int64         `json:"prompt_bytes"`
	FeedbackBytes           int64         `json:"feedback_bytes_subset"`
	StreamAttempts          int           `json:"stream_attempts"`
	MissingStreams          int           `json:"missing_stream_attempts"`
	Stream                  StreamMetrics `json:"stream"`
	ContextAttempts         int           `json:"context_attempts"`
	MissingContext          int           `json:"missing_context_attempts"`
	CompleteCompactions     int           `json:"complete_compaction_attempts"`
	PartialCompactions      int           `json:"partial_compaction_attempts"`
	UnknownCompactions      int           `json:"unknown_compaction_attempts"`
}

func (s *EfficiencySummary) add(a Attempt) {
	s.ContextCheckpointErrors += a.Usage.ContextCheckpointErrors
	if a.Prompt == nil {
		s.MissingPrompts++
	} else {
		s.PromptAttempts++
		s.PromptBytes += a.Prompt.Bytes
		s.FeedbackBytes += a.Prompt.FeedbackBytes
	}
	if a.Usage.Stream == nil {
		s.MissingStreams++
	} else {
		s.StreamAttempts++
		v := a.Usage.Stream
		s.Stream.Bytes += v.Bytes
		s.Stream.Events += v.Events
		s.Stream.MalformedLines += v.MalformedLines
		s.Stream.OversizedLines += v.OversizedLines
		s.Stream.UnattributedToolEvents += v.UnattributedToolEvents
		s.Stream.ToolCalls += v.ToolCalls
		s.Stream.ToolResultBytes += v.ToolResultBytes
		s.Stream.InstructionCommands += v.InstructionCommands
		s.Stream.InstructionOutputBytes += v.InstructionOutputBytes
	}
	if a.Usage.Context == nil || a.Usage.Context.Tokens == nil {
		s.MissingContext++
	} else {
		s.ContextAttempts++
	}
	switch a.Usage.CompactionCoverage {
	case "complete":
		s.CompleteCompactions++
	case "partial":
		s.PartialCompactions++
	default:
		s.UnknownCompactions++
	}
}

func (o *Observer) observeStream(line []byte, provider string, usage *Usage) {
	var event struct {
		Type   string          `json:"type"`
		Parent json.RawMessage `json:"parent_tool_use_id"`
		Item   struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Command string `json:"command"`
			Output  string `json:"aggregated_output"`
		} `json:"item"`
		Message struct {
			Content []struct {
				ID      string          `json:"id"`
				ToolID  string          `json:"tool_use_id"`
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	m := usage.Stream
	if m == nil {
		return
	}
	if json.Unmarshal(line, &event) != nil {
		m.MalformedLines++
		return
	}
	if event.Type == "" {
		return
	}
	m.Events++
	if len(event.Parent) > 0 && string(event.Parent) != "null" {
		return
	}
	if provider == "codex" && event.Type == "item.completed" && event.Item.Type == "command_execution" {
		if !o.onceTool("command", event.Item.ID, m) {
			return
		}
		m.ToolCalls++
		m.ToolResultBytes += int64(len(event.Item.Output))
		// This is a command reference, not proof a file was read successfully.
		if strings.Contains(event.Item.Command, "SKILL.md") || strings.Contains(event.Item.Command, "AGENTS.md") || strings.Contains(event.Item.Command, "CLAUDE.md") {
			m.InstructionCommands++
			m.InstructionOutputBytes += int64(len(event.Item.Output))
		}
	}
	if provider == "claude" && (event.Type == "assistant" || event.Type == "user") {
		for _, c := range event.Message.Content {
			if event.Type == "assistant" && c.Type == "tool_use" && o.onceTool("call", c.ID, m) {
				m.ToolCalls++
			}
			if event.Type == "user" && c.Type == "tool_result" && o.onceTool("result", c.ToolID, m) {
				m.ToolResultBytes += int64(len(c.Content))
			}
		}
	}
}

func observeCatalogue(line []byte, provider string, usage *Usage) {
	var e struct {
		Type, Provider string
		Metrics        *CatalogueMetrics `json:"metrics"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "sdlc.instruction_footprint" || e.Provider != provider || e.Metrics == nil {
		return
	}
	if validateCatalogue(e.Metrics) == nil {
		usage.Catalogue = e.Metrics
	}
}

func validateCatalogue(c *CatalogueMetrics) error {
	if c == nil {
		return nil
	}
	if c.Status != "complete" && c.Status != "partial" && c.Status != "unavailable" {
		return fmt.Errorf("invalid instruction footprint status")
	}
	for _, n := range []int64{c.SkillFiles, c.SkillBytes, c.AgentFiles, c.AgentBytes, c.SharedInstructionBytes, c.SchemaBytes, c.PromptBytes} {
		if n < 0 || n > maxTokenCount {
			return fmt.Errorf("invalid instruction footprint count")
		}
	}
	if c.Fingerprint != "" && !fingerprintPattern.MatchString(c.Fingerprint) {
		return fmt.Errorf("invalid instruction fingerprint")
	}
	return nil
}

func (u Usage) validateEfficiency() error {
	if u.ContextObservations < 0 || u.ContextObservations > maxTokenCount || u.ContextCheckpointErrors < 0 || u.ContextCheckpointErrors > maxTokenCount {
		return fmt.Errorf("invalid context telemetry counter")
	}
	if err := validateCatalogue(u.Catalogue); err != nil {
		return err
	}
	if u.Stream != nil {
		s := u.Stream
		for _, n := range []int64{s.UnattributedToolEvents, s.Bytes, s.Events, s.MalformedLines, s.OversizedLines, s.ToolCalls, s.ToolResultBytes, s.InstructionCommands, s.InstructionOutputBytes} {
			if n < 0 || n > maxTokenCount {
				return fmt.Errorf("invalid stream counter")
			}
		}
	}
	if u.CompactionCoverage != "" && u.CompactionCoverage != "complete" && u.CompactionCoverage != "partial" && u.CompactionCoverage != "unavailable" {
		return fmt.Errorf("invalid compaction coverage")
	}
	if u.Context != nil && !u.Context.ObservedAt.IsZero() && (u.Context.ObservedAt.Before(time.Unix(0, 0)) || u.Context.ObservedAt.After(time.Now().Add(24*time.Hour))) {
		return fmt.Errorf("invalid context timestamp")
	}
	return nil
}

// Supplementary pinned-client log observations never replace cumulative usage.
func (o *Observer) observeCodexContext(line []byte, provider string, u *Usage) bool {
	var e struct {
		Type         string        `json:"type"`
		Provider     string        `json:"provider"`
		Context      *ContextUsage `json:"context"`
		Peak         *int64        `json:"peak_input_tokens"`
		Compactions  *int64        `json:"compactions"`
		Coverage     string        `json:"coverage"`
		Observations *int64        `json:"observations"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "sdlc.codex_context" || provider != "codex" || e.Provider != provider {
		return false
	}
	if e.Coverage != "complete" && e.Coverage != "partial" && e.Coverage != "unavailable" {
		return true
	}
	for _, n := range []*int64{e.Peak, e.Compactions, e.Observations} {
		if n != nil && (*n < 0 || *n > maxTokenCount) {
			return true
		}
	}
	if c := e.Context; c != nil {
		if c.Source != "codex.rollout.last_token_usage" || c.Scope != "request_context" || c.Tokens == nil || c.InputTokens == nil || *c.Tokens != *c.InputTokens {
			return true
		}
		for _, n := range []*int64{c.Tokens, c.InputTokens, c.CachedInputTokens, c.Window} {
			if n != nil && (*n < 0 || *n > maxTokenCount) {
				return true
			}
		}
		if c.CachedInputTokens != nil && *c.CachedInputTokens > *c.InputTokens {
			return true
		}
		if !c.ObservedAt.IsZero() && (c.ObservedAt.Before(time.Unix(0, 0)) || c.ObservedAt.After(o.receivedAt.Add(24*time.Hour))) {
			return true
		}
		u.Context = c
	}
	if e.Peak != nil && (u.PeakContextTokens == nil || *e.Peak > *u.PeakContextTokens) {
		u.PeakContextTokens = e.Peak
	}
	if e.Observations != nil {
		u.ContextObservations = *e.Observations
	}
	if e.Compactions != nil && *e.Compactions > u.Compactions {
		u.Compactions = *e.Compactions
	}
	u.CompactionCoverage = e.Coverage
	return true
}

func (o *Observer) onceTool(kind, id string, m *StreamMetrics) bool {
	if id == "" || len(id) > 256 {
		m.UnattributedToolEvents++
		return false
	}
	key := sha256.Sum256([]byte(kind + "\x00" + id))
	if o.observedTools == nil {
		o.observedTools = map[[32]byte]struct{}{}
	}
	if _, exists := o.observedTools[key]; exists {
		return false
	}
	if len(o.observedTools) >= maxRecords {
		m.UnattributedToolEvents++
		return false
	}
	o.observedTools[key] = struct{}{}
	return true
}
