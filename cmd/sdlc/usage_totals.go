package main

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
)

// usageMeasure counts only values actually reported by native attempts.
type usageMeasure struct {
	Value           *int64 `json:"value"`
	ReportingGroups int    `json:"reporting_groups"`
}

type providerTotals struct {
	Provider           string       `json:"provider"`
	Attempts           int          `json:"attempts"`
	MeasuredAttempts   int          `json:"measured_attempts"`
	ElapsedMS          int64        `json:"elapsed_ms"`
	Input              usageMeasure `json:"native_input_tokens"`
	CacheRead          usageMeasure `json:"cache_read_tokens"`
	CacheCreation      usageMeasure `json:"cache_creation_tokens"`
	Output             usageMeasure `json:"output_tokens"`
	PromptBytes        int64        `json:"prompt_bytes"`
	PromptAttempts     int          `json:"prompt_attempts"`
	ToolResultBytes    int64        `json:"tool_result_bytes"`
	StreamAttempts     int          `json:"stream_attempts"`
	ContextAttempts    int          `json:"context_attempts"`
	CompactionComplete int          `json:"compaction_complete_attempts"`
	CompactionPartial  int          `json:"compaction_partial_attempts"`
	CompactionUnknown  int          `json:"compaction_unknown_attempts"`
}

func (m *usageMeasure) add(value *int64) {
	if value == nil {
		return
	}
	if m.Value == nil {
		m.Value = new(int64)
	}
	*m.Value += *value
	m.ReportingGroups++
}

func selectedProviderTotals(rows []usageRow) []providerTotals {
	groups := map[string]*providerTotals{}
	for _, row := range rows {
		if row.Metrics == nil {
			continue
		}
		for _, g := range row.Metrics.Groups {
			p := groups[g.Provider]
			if p == nil {
				p = &providerTotals{Provider: g.Provider}
				groups[g.Provider] = p
			}
			p.Attempts += g.Attempts
			p.MeasuredAttempts += g.MeasuredAttempts
			p.ElapsedMS += g.ElapsedMS
			p.Input.add(g.Tokens.InputTokens)
			p.CacheRead.add(g.Tokens.CachedInputTokens)
			p.CacheCreation.add(g.Tokens.CacheCreationInputTokens)
			p.Output.add(g.Tokens.OutputTokens)
			if e := g.Efficiency; e != nil {
				p.PromptBytes += e.PromptBytes
				p.PromptAttempts += e.PromptAttempts
				p.ToolResultBytes += e.Stream.ToolResultBytes
				p.StreamAttempts += e.StreamAttempts
				p.ContextAttempts += e.ContextAttempts
				p.CompactionComplete += e.CompleteCompactions
				p.CompactionPartial += e.PartialCompactions
				p.CompactionUnknown += e.UnknownCompactions
			} else {
				p.CompactionUnknown += g.Attempts
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]providerTotals, 0, len(keys))
	for _, k := range keys {
		result = append(result, *groups[k])
	}
	return result
}

func writeProviderTotals(out io.Writer, rows []usageRow) {
	totals := selectedProviderTotals(rows)
	if len(totals) == 0 {
		return
	}
	fmt.Fprintln(out, "Selected runs: provider totals (recorded values; incomplete coverage means lower bounds)")
	for _, p := range totals {
		fmt.Fprintf(out, " %s: %d/%d token attempts measured; provider time %s (setup/queue/execution).\n", dashboard.SafeText(p.Provider), p.MeasuredAttempts, p.Attempts, time.Duration(p.ElapsedMS)*time.Millisecond)
		for _, x := range []struct {
			label string
			m     usageMeasure
		}{{"native input", p.Input}, {"cache read", p.CacheRead}, {"cache creation", p.CacheCreation}, {"output", p.Output}} {
			fmt.Fprintf(out, "  %s %s (%d groups reporting);", x.label, usageCount(x.m.Value), x.m.ReportingGroups)
		}
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  Prompt bytes %d (%d/%d attempts); tool result bytes %d (%d/%d attempts); context observations %d/%d; compaction coverage complete %d, partial %d, unknown %d.\n", p.PromptBytes, p.PromptAttempts, p.Attempts, p.ToolResultBytes, p.StreamAttempts, p.Attempts, p.ContextAttempts, p.Attempts, p.CompactionComplete, p.CompactionPartial, p.CompactionUnknown)
	}
	fmt.Fprintln(out, " Native input semantics differ by provider: Codex includes cache reads; Claude reports uncached input separately. Tool-result bytes use provider-specific encoding. Missing run records are not included.")
}
