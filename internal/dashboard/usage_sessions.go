package dashboard

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

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

// WriteUsageOverview is the compact completion report; detailed evidence stays in usage/JSON.
func WriteUsageOverview(output io.Writer, summary runusage.Summary) {
	fmt.Fprintf(output, "Run usage: %s across %s", usageCountLabel(summary.Attempts, "attempt"), usageCountLabel(len(summary.Sessions), "session"))
	if summary.ElapsedMS > 0 {
		fmt.Fprintf(output, "; provider time %s", compactDuration(summary.ElapsedMS))
	}
	if summary.Failed > 0 {
		fmt.Fprintf(output, "; %s", usageCountLabel(summary.Failed, "failed attempt"))
	}
	fmt.Fprintln(output, ".")
	for _, g := range summary.Groups {
		fmt.Fprintf(output, "  %s: %s/%s", SafeText(g.Role), SafeText(g.Provider), SafeText(g.Model))
		if knownSetting(g.Effort) {
			fmt.Fprintf(output, " | %s effort", SafeText(g.Effort))
		}
		if knownSetting(g.RequestedSpeed) {
			fmt.Fprintf(output, " | %s speed", SafeText(g.RequestedSpeed))
		}
		fmt.Fprintf(output, " | %s", usageCountLabel(g.Attempts, "attempt"))
		if g.ElapsedMS > 0 {
			fmt.Fprintf(output, ", %s", compactDuration(g.ElapsedMS))
		}
		fields := compactTokenFields(g.Tokens)
		if len(fields) > 0 {
			fmt.Fprintf(output, " | %s", strings.Join(fields, ", "))
		}
		if g.MeasuredAttempts < g.Attempts {
			fmt.Fprintf(output, " (%d/%d measured)", g.MeasuredAttempts, g.Attempts)
		}
		if knownSetting(g.EffectiveSpeed) {
			fmt.Fprintf(output, " | observed speed %s", SafeText(g.EffectiveSpeed))
		}
		if g.PeakContextTokens != nil {
			fmt.Fprintf(output, " | peak context %s", commaCount(*g.PeakContextTokens))
		}
		if g.Compactions > 0 {
			fmt.Fprintf(output, " | %d compactions", g.Compactions)
		}
		if g.Retries > 0 {
			fmt.Fprintf(output, " | %d retries", g.Retries)
		}
		fmt.Fprintln(output)
	}
	writeAgentOverview(output, summary.Sessions)
	gaps := []string{}
	if summary.Incomplete > 0 {
		gaps = append(gaps, fmt.Sprintf("%d incomplete", summary.Incomplete))
	}
	if summary.Missing > 0 {
		gaps = append(gaps, fmt.Sprintf("%d missing", summary.Missing))
	}
	if summary.UnknownBaseline > 0 {
		gaps = append(gaps, fmt.Sprintf("%d resume baselines unavailable", summary.UnknownBaseline))
	}
	if len(gaps) > 0 {
		fmt.Fprintln(output, "  Measurement gaps:", strings.Join(gaps, "; ")+".")
	}
	WriteSubscriptionOverview(output, summary.Subscriptions)
	fmt.Fprintln(output, "  Model/effort/speed are requested settings unless marked observed; time includes setup, queue and tools. Unreported fields are omitted.")
}

func knownSetting(s string) bool { return s != "" && s != "unknown" }
func compactDuration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
func commaCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func compactTokenFields(t runusage.TokenUsage) []string {
	fields := []string{}
	for _, x := range []struct {
		name  string
		value *int64
	}{{"in", t.InputTokens}, {"cached", t.CachedInputTokens}, {"cache write", t.CacheCreationInputTokens}, {"out", t.OutputTokens}} {
		if x.value != nil && (x.name != "cache write" || *x.value > 0) {
			fields = append(fields, x.name+" "+commaCount(*x.value))
		}
	}
	return fields
}
func writeAgentOverview(output io.Writer, sessions []runusage.SessionSummary) {
	type group struct {
		count, measured int
		tokens          int64
		duration        int64
		timed           int
	}
	groups := map[string]*group{}
	total, truncated := 0, false
	for _, session := range sessions {
		truncated = truncated || session.AgentsTruncated
		for _, a := range session.Agents {
			total++
			model := strings.Join(a.ReportedModels, ", ")
			if model == "" && a.RequestedModel != "" {
				model = a.RequestedModel + " (requested)"
			}
			if model == "" {
				model = "model unreported"
			}
			key := session.Provider + "/" + model
			if knownSetting(a.Type) {
				key += " | " + a.Type
			}
			if knownSetting(a.Status) && a.Status != "completed" {
				key += " | " + a.Status
			}
			if knownSetting(a.ReportedEffort) {
				key += " | " + a.ReportedEffort + " effort observed"
			}
			if knownSetting(a.EffectiveSpeed) {
				key += " | " + a.EffectiveSpeed + " speed observed"
			}
			g := groups[key]
			if g == nil {
				g = &group{}
				groups[key] = g
			}
			g.count++
			if a.TotalTokens != nil {
				g.tokens += *a.TotalTokens
				g.measured++
			}
			if a.DurationMS != nil {
				g.duration += *a.DurationMS
				g.timed++
			}
		}
	}
	if total == 0 {
		fmt.Fprintln(output, "  Subagents: none observed (provider visibility is partial).")
	} else {
		fmt.Fprintf(output, "  Subagents: %d observed (provider visibility is partial; task counters are not added again to native totals).\n", total)
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i == 5 {
				fmt.Fprintf(output, "    %d more model/settings groups; see detailed usage.\n", len(keys)-i)
				break
			}
			g := groups[key]
			fmt.Fprintf(output, "    %s: %s", SafeText(key), usageCountLabel(g.count, "agent"))
			if g.measured > 0 {
				fmt.Fprintf(output, " | task tokens %s (%d/%d measured)", commaCount(g.tokens), g.measured, g.count)
			}
			if g.timed > 0 {
				fmt.Fprintf(output, " | task time %s (%d/%d measured)", compactDuration(g.duration), g.timed, g.count)
			}
			fmt.Fprintln(output)
		}
	}
	if truncated {
		fmt.Fprintln(output, "    Subagent collection limit reached; counts are lower bounds.")
	}
}

func writeSessionInvocations(output io.Writer, session runusage.SessionSummary) {
	for _, call := range session.Invocations {
		fmt.Fprintf(output, "    Attempt %d %s (%s): requested model %s, reasoning %s, speed %s; reported model %s, reasoning %s, speed %s.\n", call.Attempt, SafeText(call.Role), SafeText(call.Outcome), usageSetting(call.RequestedModel), usageSetting(call.RequestedEffort), usageSetting(call.RequestedSpeed), usageSetting(call.ReportedModel), usageSetting(call.ReportedEffort), usageSetting(call.EffectiveSpeed))
		if call.ReviewEffortSelection != nil {
			fmt.Fprintf(output, "      Automatic review effort (%s): %s.\n", SafeText(call.ReviewEffortSelection.Policy), SafeText(strings.Join(call.ReviewEffortSelection.Reasons, "; ")))
		}
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

func usageCountLabel(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}
