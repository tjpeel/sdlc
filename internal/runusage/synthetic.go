package runusage

import (
	"crypto/sha256"
	"encoding/json"
)

// OpenCode projects root message checkpoints, replacing repeated updates before
// emitting a single invocation total. Missing delegate coverage stays partial.
func (o *Observer) observeSynthetic(line []byte, provider string, usage *Usage) bool {
	if provider != "synthetic" {
		return false
	}
	var event struct {
		Type          string          `json:"type"`
		Provider      string          `json:"provider"`
		Complete      bool            `json:"complete"`
		Usage         json.RawMessage `json:"usage"`
		Compactions   json.RawMessage `json:"compactions"`
		ClientVersion string          `json:"client_version"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type != "sdlc.synthetic_metrics" {
		return false
	}
	if event.Provider != provider {
		return true
	}
	tokens, valid := counts(event.Usage, "cached_input_tokens")
	compactions, compactionsValid := parseCount(event.Compactions)
	if !valid || tokens == nil || !compactionsValid || compactions == nil {
		return true
	}
	hash := sha256.Sum256(line)
	if hash == o.lastResult {
		return true
	}
	o.lastResult = hash
	tokens.Source, tokens.Scope = "opencode.message.updated", "attempt_total"
	usage.Aggregate = tokens
	if clientVersion.MatchString(event.ClientVersion) {
		usage.ClientVersion = event.ClientVersion
	}
	usage.Compactions = *compactions
	usage.CompactionCoverage = "partial"
	usage.Completeness = "partial"
	if event.Complete {
		usage.Completeness = "observed"
	}
	usage.CompletedTurns++
	return true
}
