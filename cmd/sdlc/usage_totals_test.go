package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestProviderTotalsKeepProvidersAndMissingValuesSeparate(t *testing.T) {
	a, b, cache := int64(100), int64(30), int64(70)
	rows := []usageRow{
		{Metrics: &runusage.Summary{Groups: []runusage.Group{{Provider: "codex", Attempts: 2, MeasuredAttempts: 2, Tokens: runusage.TokenUsage{InputTokens: &a, CachedInputTokens: &cache}}}}},
		{Metrics: &runusage.Summary{Groups: []runusage.Group{{Provider: "claude", Attempts: 2, MeasuredAttempts: 1, Tokens: runusage.TokenUsage{InputTokens: &b}}}}},
		{Metrics: &runusage.Summary{Groups: []runusage.Group{{Provider: "codex", Attempts: 1, MeasuredAttempts: 0}}}},
	}
	totals := selectedProviderTotals(rows)
	if len(totals) != 2 || totals[0].Provider != "claude" || *totals[0].Input.Value != 30 || totals[0].CacheRead.Value != nil || totals[1].Attempts != 3 || totals[1].MeasuredAttempts != 2 || *totals[1].Input.Value != 100 || *totals[1].CacheRead.Value != 70 || totals[1].CompactionUnknown != 3 {
		t.Fatalf("incorrect totals or coverage: %+v", totals)
	}
	var out bytes.Buffer
	writeProviderTotals(&out, rows)
	for _, want := range []string{"1/2 token attempts measured", "cache read unknown", "2/3 token attempts measured", "Codex includes cache reads", "compaction coverage complete 0, partial 0, unknown 3"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}
