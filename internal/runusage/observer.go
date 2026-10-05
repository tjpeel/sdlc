package runusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"math"
	"sync"
	"time"
	"unicode"
)

const maxEventLine = 256 * 1024
const maxTokenCount int64 = 1_000_000_000_000

// Usage is optional native-client telemetry, not accounting or a model-identity
// guarantee. Nil fields mean unknown. Aggregate counters are replaced, never
// added across resumes, and cannot establish a current request's context size.
type Usage struct {
	Aggregate         *TokenUsage           `json:"aggregate,omitempty"`
	Context           *ContextUsage         `json:"context,omitempty"`
	ClientVersion     string                `json:"client_version,omitempty"`
	ReportedModel     string                `json:"reported_model,omitempty"`
	ModelMatches      bool                  `json:"model_matches"`
	Models            map[string]TokenUsage `json:"models,omitempty"`
	EstimatedCostUSD  *float64              `json:"estimated_cost_usd,omitempty"`
	DurationMS        *int64                `json:"duration_ms,omitempty"`
	APIDurationMS     *int64                `json:"api_duration_ms,omitempty"`
	Turns             *int64                `json:"turns,omitempty"`
	RateLimits        map[string]RateLimit  `json:"rate_limits,omitempty"`
	PeakContextTokens *int64                `json:"peak_context_tokens,omitempty"`
	Compactions       int64                 `json:"compactions,omitempty"`
	Retries           int64                 `json:"retries,omitempty"`
	CompletedTurns    int64                 `json:"completed_turns,omitempty"`
	Completeness      string                `json:"completeness,omitempty"`
}

type TokenUsage struct {
	Source                   string `json:"source"`
	Scope                    string `json:"scope"`
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens        *int64 `json:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens    *int64 `json:"reasoning_output_tokens,omitempty"`
	CacheWriteInputTokens    *int64 `json:"cache_write_input_tokens,omitempty"`
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

// The pinned Codex 0.160.0 emitter uses ThreadTokenUsage.total here:
// https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/exec/src/event_processor_with_jsonl_output.rs
// Claude primary assistant usage and result modelUsage are native wire fields:
// https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/types.py
// Claude documents input + cache read + cache creation as current input context:
// https://code.claude.com/docs/en/statusline
// The Claude 2.1.287 CLI may omit contextWindow: no window or percentage is
// inferred from model names, aggregate usage, or a provider's advertised limit.
type Observer struct {
	mu                                 sync.Mutex
	provider, requested, nativeSession string
	usage                              Usage

	line       []byte
	dropping   bool
	messageID  [32]byte
	sessionID  [32]byte
	lastResult [32]byte
}

func (o *Observer) reset() {
	clear(o.line)
	o.line = nil
	o.dropping = false
	o.messageID = [32]byte{}
	o.sessionID = [32]byte{}
}

func (o *Observer) feed(data []byte, provider, model string, usage *Usage) {
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
	for key, target := range map[string]**int64{"input_tokens": &u.InputTokens, cachedKey: &u.CachedInputTokens, "cache_creation_input_tokens": &u.CacheCreationInputTokens, "output_tokens": &u.OutputTokens, "reasoning_output_tokens": &u.ReasoningOutputTokens, "cache_write_input_tokens": &u.CacheWriteInputTokens} {
		n, valid := parseCount(values[key])
		if !valid {
			return nil, false
		}
		*target = n
	}
	if u.InputTokens == nil && u.CachedInputTokens == nil && u.CacheCreationInputTokens == nil && u.OutputTokens == nil && u.ReasoningOutputTokens == nil && u.CacheWriteInputTokens == nil {
		return nil, true
	}
	return u, true
}

func (o *Observer) consume(line []byte, provider, requested string, usage *Usage) {
	var event struct {
		ClientVersion string          `json:"claude_code_version"`
		Type          string          `json:"type"`
		Subtype       string          `json:"subtype"`
		Model         string          `json:"model"`
		SessionID     string          `json:"session_id"`
		ThreadID      string          `json:"thread_id"`
		Parent        json.RawMessage `json:"parent_tool_use_id"`
		Usage         json.RawMessage `json:"usage"`
		Cost          json.RawMessage `json:"total_cost_usd"`
		Duration      json.RawMessage `json:"duration_ms"`
		APIDuration   json.RawMessage `json:"duration_api_ms"`
		Turns         json.RawMessage `json:"num_turns"`
		Rate          json.RawMessage `json:"rate_limit_info"`
		ModelUsage    map[string]struct {
			Window   json.RawMessage `json:"contextWindow"`
			Input    json.RawMessage `json:"inputTokens"`
			Cached   json.RawMessage `json:"cacheReadInputTokens"`
			Creation json.RawMessage `json:"cacheCreationInputTokens"`
			Output   json.RawMessage `json:"outputTokens"`
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
			o.nativeSession = id
		}
	}
	if provider == "claude" && event.Type == "system" && event.Subtype == "init" && clientVersion.MatchString(event.ClientVersion) {
		usage.ClientVersion = event.ClientVersion
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
			if u.ReasoningOutputTokens != nil && u.OutputTokens != nil && *u.ReasoningOutputTokens > *u.OutputTokens {
				return
			}
			if h := sha256.Sum256(line); h != o.lastResult {
				usage.CompletedTurns++
				o.lastResult = h
			}
			usage.Completeness = "observed"
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
	if event.Type == "system" && event.Subtype == "api_retry" {
		usage.Retries++
	}
	if event.Type == "system" && event.Subtype == "compact_boundary" {
		usage.Compactions++
	}
	if event.Type == "rate_limit_event" {
		observeRate(event.Rate, usage)
	}
	if usage.Context != nil && usage.Context.Tokens != nil && (usage.PeakContextTokens == nil || *usage.Context.Tokens > *usage.PeakContextTokens) {
		n := *usage.Context.Tokens
		usage.PeakContextTokens = &n
	}
	if event.Type == "result" {
		if event.Subtype == "error_during_execution" {
			usage.Completeness = "incomplete"
			return
		}
		usage.Completeness = "observed"
		usage.Models = nil
		usage.EstimatedCostUSD = nil
		if h := sha256.Sum256(line); h != o.lastResult {
			usage.CompletedTurns++
			o.lastResult = h
		}
		if n, ok := parseCount(event.Duration); ok {
			usage.DurationMS = n
		}
		if n, ok := parseCount(event.APIDuration); ok {
			usage.APIDurationMS = n
		}
		if n, ok := parseCount(event.Turns); ok {
			usage.Turns = n
		}
		if n, ok := parseNumber(event.Cost, 1e9); ok {
			usage.EstimatedCostUSD = n
		}
		if len(event.ModelUsage) > 0 && len(event.ModelUsage) <= 64 {
			models := make(map[string]TokenUsage)
			valid := true
			for model, entry := range event.ModelUsage {
				if !safeModel(model) {
					valid = false
					break
				}
				v := TokenUsage{Source: "claude.result.modelUsage", Scope: "session_total"}
				for _, field := range []struct {
					raw    json.RawMessage
					target **int64
				}{{entry.Input, &v.InputTokens}, {entry.Cached, &v.CachedInputTokens}, {entry.Creation, &v.CacheCreationInputTokens}, {entry.Output, &v.OutputTokens}} {
					n, ok := parseCount(field.raw)
					if !ok {
						valid = false
					}
					*field.target = n
				}
				models[model] = v
			}
			if valid {
				usage.Models = models
			}
		}

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

// NewObserver reads native JSONL only; it never changes the provider lifecycle.
func NewObserver(provider, requested string) *Observer {
	return &Observer{provider: provider, requested: requested}
}
func (o *Observer) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.feed(data, o.provider, o.requested, &o.usage)
	return len(data), nil
}
func (o *Observer) Consume(line []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.consume(line, o.provider, o.requested, &o.usage)
}
func (o *Observer) Finish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.dropping && len(o.line) > 0 {
		o.consume(o.line, o.provider, o.requested, &o.usage)
	}
	clear(o.line)
	o.line = nil
	o.dropping = false
	if o.usage.Completeness == "" {
		if o.usage.Context != nil || o.usage.Aggregate != nil {
			o.usage.Completeness = "partial"
		} else {
			o.usage.Completeness = "unavailable"
		}
	}
}
func (o *Observer) Snapshot() Usage   { o.mu.Lock(); defer o.mu.Unlock(); return Clone(o.usage) }
func (o *Observer) SessionID() string { o.mu.Lock(); defer o.mu.Unlock(); return o.nativeSession }
func Clone(u Usage) Usage {
	data, _ := json.Marshal(u)
	var v Usage
	_ = json.Unmarshal(data, &v)
	return v
}
func parseNumber(raw json.RawMessage, max float64) (*float64, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > max {
		return nil, false
	}
	return &n, true
}

type RateLimit struct {
	ObservedAt  time.Time `json:"observed_at"`
	Source      string    `json:"source"`
	Status      string    `json:"status"`
	ResetsAt    *int64    `json:"resets_at,omitempty"`
	Utilization *float64  `json:"utilization,omitempty"`
}

func observeRate(raw json.RawMessage, u *Usage) {
	var r struct {
		Status      string          `json:"status"`
		Window      string          `json:"rateLimitType"`
		Reset       json.RawMessage `json:"resetsAt"`
		Utilization json.RawMessage `json:"utilization"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	switch r.Status {
	case "allowed", "allowed_warning", "rejected":
	default:
		return
	}
	switch r.Window {
	case "five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "overage":
	default:
		return
	}
	reset, ok := parseCount(r.Reset)
	if !ok {
		return
	}
	fraction, ok := parseNumber(r.Utilization, 1)
	if !ok {
		return
	}
	if u.RateLimits == nil {
		u.RateLimits = make(map[string]RateLimit)
	}
	u.RateLimits[r.Window] = RateLimit{ObservedAt: time.Now().UTC(), Source: "claude.rate_limit_event", Status: r.Status, ResetsAt: reset, Utilization: fraction}
}
