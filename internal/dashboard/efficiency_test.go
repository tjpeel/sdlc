package dashboard

import (
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runstatus"
)

func TestCodexRequestInputIsNotPresentedAsNativeHeadroom(t *testing.T) {
	tokens, window := int64(200), int64(1000)
	s := usageSummary(runstatus.Usage{ModelMatches: true, Context: &runstatus.ContextUsage{Source: "codex.rollout.last_token_usage", Scope: "request_context", Tokens: &tokens, Window: &window}})
	if !strings.Contains(s, "Last observed request input: 200") || !strings.Contains(s, "remaining headroom unknown") || strings.Contains(s, "20.0%") {
		t.Fatal(s)
	}
}
