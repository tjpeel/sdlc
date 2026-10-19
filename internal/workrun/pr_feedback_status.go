package workrun

import (
	"fmt"
	"strings"
)

type PRFeedbackSummary struct {
	Addressed          int    `json:"addressed_by_reaction,omitempty"`
	Ignored            int    `json:"ignored,omitempty"`
	Cycle              int    `json:"cycle"`
	Pending            int    `json:"pending"`
	Unresolved         int    `json:"unresolved"`
	OutstandingReplies int    `json:"outstanding_replies"`
	Summary            string `json:"summary"`
}

// PRFeedbackStatus is private handoff text; callers must not publish it as
// telemetry because review bodies and assessment reasons may contain private data.
func PRFeedbackStatus(j Journal) PRFeedbackSummary {
	result := PRFeedbackSummary{}
	if j.PRFeedback == nil {
		return result
	}
	result.Cycle = j.PRFeedback.Cycle
	latest := map[string]int{}
	for i, e := range j.PRFeedback.Entries {
		latest[e.Comment.ID] = i
	}
	lines := []string{}
	replyConversations := map[string]bool{}
	conversationKeys := map[string]string{}
	for _, g := range feedbackConversations(j.PRFeedback) {
		for _, c := range g.Comments {
			conversationKeys[c.ID] = g.Comments[0].ID
		}
	}
	for i, e := range j.PRFeedback.Entries {
		if latest[e.Comment.ID] != i || e.Comment.Deleted || e.Comment.Resolved {
			continue
		}
		if e.Comment.Ignored != nil {
			result.Ignored++
			continue
		}
		if e.Comment.Addressed != nil {
			result.Addressed++
			continue
		}
		if e.Assessment == nil || e.Provisional {
			result.Pending++
			continue
		}
		if e.Reply == nil {
			key := conversationKeys[e.Comment.ID]
			if key == "" {
				key = e.Comment.ID
			}
			replyConversations[key] = true
		}
		if e.Assessment.Disposition == "unaddressable" {
			result.Unresolved++
			lines = append(lines, e.Comment.ID+": "+e.Assessment.Reason)
		}
	}
	result.OutstandingReplies = len(replyConversations)
	if result.Pending > 0 || result.Unresolved > 0 || result.OutstandingReplies > 0 || result.Ignored > 0 || result.Addressed > 0 {
		result.Summary = fmt.Sprintf("PR feedback cycle %d: %d pending, %d unresolved, %d replies outstanding.", result.Cycle, result.Pending, result.Unresolved, result.OutstandingReplies)
		if result.Ignored > 0 {
			result.Summary += fmt.Sprintf(" %d ignored by operator reaction (including replies).", result.Ignored)
		}
		if result.Addressed > 0 {
			result.Summary += fmt.Sprintf(" %d marked already addressed by 🚀.", result.Addressed)
		}

		if len(lines) > 0 {
			result.Summary += "\n" + strings.Join(lines, "\n")
		}
		result.Summary = boundedReviewSummary(result.Summary)
	}
	return result
}
