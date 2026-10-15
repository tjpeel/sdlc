package dashboard

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func usageSetting(value string) string {
	if value == "" {
		return "unknown"
	}
	return SafeText(value)
}

// WriteUsageSettings keeps requested configuration separate from observed evidence.
func WriteUsageSettings(output io.Writer, group runusage.Group) {
	fmt.Fprintf(output, "    Native fast-mode state at initialization: %s (does not establish delivered service speed).\n", usageSetting(strings.Join(group.FastModeStates, ", ")))
	fmt.Fprintf(output, "    Observed peak input context %s; compactions %d; retries %d; reasoning output %s (subset of output).\n", tokenCount(group.PeakContextTokens), group.Compactions, group.Retries, tokenCount(group.Tokens.ReasoningOutputTokens))
	fmt.Fprintf(output, "    Requested reasoning %s; speed %s; effective reasoning unknown; speed %s.\n", usageSetting(group.Effort), usageSetting(group.RequestedSpeed), usageSetting(group.EffectiveSpeed))
	models := make([]string, 0, len(group.Models))
	for model := range group.Models {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		tokens := group.Models[model]
		fmt.Fprintf(output, "    Observed model %s: input %s, cache read %s, cache creation %s, output %s (includes native subagents; reasoning/speed attribution unknown).\n", SafeText(model), tokenCount(tokens.InputTokens), tokenCount(tokens.CachedInputTokens), tokenCount(tokens.CacheCreationInputTokens), tokenCount(tokens.OutputTokens))
	}
}

// WriteSessions reports session checkpoints and delegation evidence separately.
func WriteSessions(output io.Writer, summary runusage.Summary) {
	fmt.Fprintln(output, "Provider sessions (native cumulative checkpoints; do not add to attempt totals):")
	if len(summary.Sessions) == 0 {
		fmt.Fprintln(output, "  Session and subagent evidence: unknown (no recorded attempts).")
	}
	for _, session := range summary.Sessions {
		identity := "observed"
		if !session.IdentityObserved {
			identity = "unknown; invocation shown separately"
		}
		fmt.Fprintf(output, "  Session %d: %s; identity %s; usage %s.\n", session.Number, SafeText(session.Provider), identity, session.Completeness)
		if session.Tokens != nil {
			t := session.Tokens
			fmt.Fprintf(output, "    Session input %s, cache read %s, cache creation %s, output %s.\n", tokenCount(t.InputTokens), tokenCount(t.CachedInputTokens), tokenCount(t.CacheCreationInputTokens), tokenCount(t.OutputTokens))
		} else {
			fmt.Fprintln(output, "    Session tokens: unknown (latest checkpoint incomplete or unreported).")
		}
		writeSessionInvocations(output, session)
		writeSessionAgents(output, session)
	}
	fmt.Fprintln(output, "  Subagent task counters are separate observations; never add them to native session totals. Codex child usage and model/settings are not exposed by the pinned exec protocol.")
}

func WriteUsageOverview(output io.Writer, summary runusage.Summary) {
	fmt.Fprintf(output, "Run usage: %d provider attempts; %d session records; incomplete %d, missing %d, unknown resume baseline %d.\n", summary.Attempts, len(summary.Sessions), summary.Incomplete, summary.Missing, summary.UnknownBaseline)
	for _, g := range summary.Groups {
		fmt.Fprintf(output, "  %s %s / %s: input %s, cache read %s, output %s; measured %d/%d attempts.\n", SafeText(g.Provider), SafeText(g.Model), SafeText(g.Role), tokenCount(g.Tokens.InputTokens), tokenCount(g.Tokens.CachedInputTokens), tokenCount(g.Tokens.OutputTokens), g.MeasuredAttempts, g.Attempts)
		WriteUsageSettings(output, g)
	}
	for _, s := range summary.Sessions {
		fmt.Fprintf(output, "  Session %d %s: %d observed subagents; usage %s", s.Number, SafeText(s.Provider), len(s.Agents), s.Completeness)
		if s.Tokens != nil {
			fmt.Fprintf(output, "; input %s, cache read %s, output %s", tokenCount(s.Tokens.InputTokens), tokenCount(s.Tokens.CachedInputTokens), tokenCount(s.Tokens.OutputTokens))
		}
		fmt.Fprintln(output, ".")
		writeSessionInvocations(output, s)
		writeSessionAgents(output, s)
	}
	fmt.Fprintln(output, "  Subagent visibility is partial; unreported settings and counters remain unknown. Session checkpoints and child counters are not added to attempt totals.")
	WriteSubscriptions(output, summary.Subscriptions)
}

func writeSessionInvocations(output io.Writer, session runusage.SessionSummary) {
	for _, call := range session.Invocations {
		fmt.Fprintf(output, "    Attempt %d %s (%s): requested model %s, reasoning %s, speed %s; reported model %s, reasoning %s, speed %s.\n", call.Attempt, SafeText(call.Role), SafeText(call.Outcome), usageSetting(call.RequestedModel), usageSetting(call.RequestedEffort), usageSetting(call.RequestedSpeed), usageSetting(call.ReportedModel), usageSetting(call.ReportedEffort), usageSetting(call.EffectiveSpeed))
		fmt.Fprintf(output, "      Native fast-mode state at initialization: %s.\n", usageSetting(call.FastModeState))
	}
}

func writeSessionAgents(output io.Writer, session runusage.SessionSummary) {
	fmt.Fprintf(output, "    Subagents observed: %d (partial visibility; absence does not prove none were used).\n", len(session.Agents))
	for index, a := range session.Agents {
		reported := strings.Join(a.ReportedModels, ", ")
		fmt.Fprintf(output, "      Agent %d: type %s; status %s; requested model %s; reported models %s; reasoning %s; speed %s; task tokens %s; tool uses %s; duration ms %s.\n", index+1, usageSetting(a.Type), usageSetting(a.Status), usageSetting(a.RequestedModel), usageSetting(reported), usageSetting(a.ReportedEffort), usageSetting(a.EffectiveSpeed), tokenCount(a.TotalTokens), tokenCount(a.ToolUses), tokenCount(a.DurationMS))
		if a.FinalRequestTokens != nil {
			fmt.Fprintf(output, "        Final request combined tokens %s (not whole-agent usage).\n", tokenCount(a.FinalRequestTokens))
		}
		if a.FinalRequestUsage != nil {
			t := a.FinalRequestUsage
			fmt.Fprintf(output, "        Final request only: input %s, cache read %s, cache creation %s, output %s (not whole-agent usage).\n", tokenCount(t.InputTokens), tokenCount(t.CachedInputTokens), tokenCount(t.CacheCreationInputTokens), tokenCount(t.OutputTokens))
		}
		if a.Parent != 0 {
			fmt.Fprintf(output, "        Parent: agent %d.\n", a.Parent)
		}
		if a.ModelsTruncated {
			fmt.Fprintln(output, "        Additional observed models omitted by collection limit.")
		}
	}
	if session.AgentsTruncated {
		fmt.Fprintln(output, "    Additional subagent evidence omitted by collection limit.")
	}
}
