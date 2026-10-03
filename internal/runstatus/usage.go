package runstatus

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"unicode"
)

const maxEventLine = 256 * 1024
const maxTokenCount int64 = 1_000_000_000_000

// Usage is optional native-client telemetry, not accounting or a model-identity
// guarantee. Nil fields mean unknown. Aggregate counters are replaced, never
// added across resumes, and cannot establish a current request's context size.
type Usage struct {
	Aggregate     *TokenUsage   `json:"aggregate,omitempty"`
	Context       *ContextUsage `json:"context,omitempty"`
	ReportedModel string        `json:"reported_model,omitempty"`
	ModelMatches  bool          `json:"model_matches"`
}

type TokenUsage struct {
	Source                   string `json:"source"`
	Scope                    string `json:"scope"`
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens        *int64 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
}

type ContextUsage struct {
	Source                   string `json:"source"`
	Scope                    string `json:"scope"`
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens        *int64 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	Tokens                   *int64 `json:"tokens,omitempty"`
	Window                   *int64 `json:"window,omitempty"`
}

// The pinned Codex 0.159.3 emitter uses ThreadTokenUsage.total here:
// https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/exec/src/event_processor_with_jsonl_output.rs
// Claude primary assistant usage and result modelUsage are native wire fields:
// https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/types.py
// Claude documents input + cache read + cache creation as current input context:
// https://code.claude.com/docs/en/statusline
// The Claude 2.1.287 CLI may omit contextWindow: no window or percentage is
// inferred from model names, aggregate usage, or a provider's advertised limit.
type usageObserver struct {
	line      []byte
	dropping  bool
	messageID [32]byte
	sessionID [32]byte
}

func (o *usageObserver) reset() {
	clear(o.line)
	*o = usageObserver{}
}

func (o *usageObserver) feed(data []byte, provider, model string, usage *Usage) {
	for len(data) > 0 {
		at := bytes.IndexByte(data, '\n')
		part := data
		if at >= 0 {
			part = data[:at]
		}
		if !o.dropping {
			if len(o.line)+len(part) > maxEventLine {
				clear(o.line)
				o.line = nil
				o.dropping = true
			} else {
				o.line = append(o.line, part...)
			}
		}
		if at < 0 {
			return
		}
		if !o.dropping {
			o.consume(o.line, provider, model, usage)
		}
		clear(o.line)
		o.line = o.line[:0]
		o.dropping = false
		data = data[at+1:]
	}
}

func safeModel(model string) bool {
	if len(model) == 0 || len(model) > 256 {
		return false
	}
	for _, r := range model {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func parseCount(raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > maxTokenCount {
		return nil, false
	}
	return &n, true
}

func counts(raw json.RawMessage, cachedKey string) (*TokenUsage, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	u := new(TokenUsage)
	for key, target := range map[string]**int64{"input_tokens": &u.InputTokens, cachedKey: &u.CachedInputTokens, "cache_creation_input_tokens": &u.CacheCreationInputTokens, "output_tokens": &u.OutputTokens} {
		n, valid := parseCount(values[key])
		if !valid {
			return nil, false
		}
		*target = n
	}
	if u.InputTokens == nil && u.CachedInputTokens == nil && u.CacheCreationInputTokens == nil && u.OutputTokens == nil {
		return nil, true
	}
	return u, true
}

func (o *usageObserver) consume(line []byte, provider, requested string, usage *Usage) {
	var event struct {
		Type       string          `json:"type"`
		Subtype    string          `json:"subtype"`
		Model      string          `json:"model"`
		SessionID  string          `json:"session_id"`
		ThreadID   string          `json:"thread_id"`
		Parent     json.RawMessage `json:"parent_tool_use_id"`
		Usage      json.RawMessage `json:"usage"`
		ModelUsage map[string]struct {
			Window json.RawMessage `json:"contextWindow"`
		} `json:"modelUsage"`
		Message struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if len(event.Parent) != 0 && !bytes.Equal(bytes.TrimSpace(event.Parent), []byte("null")) {
		return
	}
	if event.SessionID != "" && o.sessionID != ([32]byte{}) && !(event.Type == "system" && event.Subtype == "init") {
		if len(event.SessionID) > 256 || sha256.Sum256([]byte(event.SessionID)) != o.sessionID {
			return
		}
	}
	if (provider == "codex" && event.Type == "thread.started") || (provider == "claude" && event.Type == "system" && event.Subtype == "init") {
		id := event.SessionID
		if provider == "codex" {
			id = event.ThreadID
		}
		if id != "" && len(id) <= 256 {
			hash := sha256.Sum256([]byte(id))
			if o.sessionID != ([32]byte{}) && o.sessionID != hash {
				*usage = Usage{}
				o.messageID = [32]byte{}
			}
			o.sessionID = hash
		}
	}
	reported := event.Model
	if event.Message.Model != "" {
		reported = event.Message.Model
	}
	if safeModel(reported) {
		if usage.ReportedModel != "" && usage.ReportedModel != reported {
			usage.Context = nil
			o.messageID = [32]byte{}
		}
		usage.ReportedModel, usage.ModelMatches = reported, reported == requested
	}
	if provider == "codex" && event.Type == "turn.completed" {
		u, valid := counts(event.Usage, "cached_input_tokens")
		if valid && u != nil {
			if u.InputTokens != nil && u.CachedInputTokens != nil && *u.CachedInputTokens > *u.InputTokens {
				return
			}
			u.Source, u.Scope = "codex.turn.completed", "session_total"
			usage.Aggregate = u
		}
	}
	if provider != "claude" {
		return
	}
	if event.Type == "assistant" && safeModel(event.Message.Model) && event.Message.ID != "" && len(event.Message.ID) <= 256 {
		id := sha256.Sum256([]byte(event.Message.ID))
		if o.messageID != id || usage.Context == nil {
			window := (*int64)(nil)
			if usage.Context != nil && usage.ModelMatches {
				window = usage.Context.Window
			}
			usage.Context = &ContextUsage{Source: "claude.assistant.message", Scope: "request_context", Window: window}
			o.messageID = id
		}
		u, valid := counts(event.Message.Usage, "cache_read_input_tokens")
		if !valid || u == nil {
			return
		}
		c := usage.Context
		if u.InputTokens != nil {
			c.InputTokens = u.InputTokens
		}
		if u.CachedInputTokens != nil {
			c.CachedInputTokens = u.CachedInputTokens
		}
		if u.CacheCreationInputTokens != nil {
			c.CacheCreationInputTokens = u.CacheCreationInputTokens
		}
		c.Tokens = nil
		if c.InputTokens != nil && c.CachedInputTokens != nil && c.CacheCreationInputTokens != nil {
			total := *c.InputTokens + *c.CachedInputTokens + *c.CacheCreationInputTokens
			if total <= maxTokenCount {
				c.Tokens = &total
			}
		}
	}
	if event.Type == "result" {
		u, valid := counts(event.Usage, "cache_read_input_tokens")
		if valid && u != nil {
			u.Source, u.Scope = "claude.result", "reported_total"
			usage.Aggregate = u
		}
		// A matching map key alone cannot establish the primary model identity.
		if usage.ModelMatches && usage.ReportedModel == requested {
			if entry, ok := event.ModelUsage[usage.ReportedModel]; ok {
				window, valid := parseCount(entry.Window)
				if valid && window != nil && *window > 0 && *window <= 100_000_000 {
					if usage.Context == nil {
						usage.Context = &ContextUsage{Source: "claude.result.modelUsage", Scope: "request_context"}
					}
					usage.Context.Window = window
				}
			}
		}
	}
}
