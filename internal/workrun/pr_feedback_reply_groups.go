package workrun

import "context"

// Optional for older adapters; production publishers validate every captured
// conversation member immediately before acknowledging the combined result.
type PRFeedbackConversationPublisher interface {
	ReplyFeedbackConversation(context.Context, Plan, Publication, PRFeedbackComment, []PRFeedbackComment, string, string) (PRFeedbackReply, error)
}

type feedbackReplyGroup struct {
	Target   PRFeedbackComment
	Comments []PRFeedbackComment
	Entries  []*PRFeedbackEntry
	Deferred bool
}

func feedbackReplyGroups(h *PRFeedbackHistory) []feedbackReplyGroup {
	if h == nil {
		return nil
	}
	latest := map[string]*PRFeedbackEntry{}
	for i := range h.Entries {
		e := &h.Entries[i]
		latest[e.Comment.ID] = e
	}
	groups := []feedbackReplyGroup{}
	for _, conversation := range feedbackConversations(h) {
		g := feedbackReplyGroup{Comments: conversation.Comments}
		ids := map[string]bool{}
		for _, c := range conversation.Comments {
			ids[c.ID] = true
		}
		for _, c := range conversation.Comments {
			if c.Deleted || c.Resolved || c.Ignored != nil || c.Addressed != nil {
				continue
			}
			e := latest[c.ID]
			if e == nil || e.Reply != nil {
				continue
			}
			if e.Assessment == nil || e.Provisional {
				g.Deferred = true
				continue
			}
			g.Entries = append(g.Entries, e)
		}
		if len(g.Entries) == 0 {
			continue
		}
		g.Target = g.Entries[0].Comment
		// Prefer the actual parent for one thread response. The leader marker still
		// belongs to the newly assessed input if the parent was acknowledged earlier.
		for _, c := range conversation.Comments {
			if c.Ignored != nil || c.Addressed != nil {
				continue
			}
			parent := false
			for _, id := range c.ParentIDs {
				if ids[id] {
					parent = true
				}
			}
			if !parent {
				g.Target = c
				break
			}
		}
		groups = append(groups, g)
	}
	return groups
}
