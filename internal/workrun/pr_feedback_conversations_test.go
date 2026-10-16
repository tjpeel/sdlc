package workrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFeedbackConversationLinksAndInheritedIgnore(t *testing.T) {
	root := generalFeedback("comment:1", "The original issue needs investigation.")
	child := generalFeedback("comment:2", "> The original issue needs investigation.\n\nUse the shared parser to fix it.")
	grandchild := generalFeedback("comment:3", "See https://github.com/example/project/pull/1#issuecomment-2 for the proposed fix.")
	unrelated := generalFeedback("comment:4", "An unrelated issue must still be handled.")
	foreign := generalFeedback("comment:5", "See https://github.com/example/project/pull/2#issuecomment-1")
	comments := []PRFeedbackComment{grandchild, unrelated, child, root, foreign}
	linkFeedbackConversations(comments, "https://github.com/example/project/pull/1")
	comments[3].Ignored = &PRFeedbackIgnore{By: "example", UserID: 123, ReactionID: "ignored-1", CreatedAt: "2026-10-15T12:00:00Z"}
	inheritFeedbackIgnores(comments)
	for _, i := range []int{0, 2, 3} {
		if comments[i].Ignored == nil {
			t.Fatal("descendant escaped ignored root", comments[i].ID)
		}
	}
	for _, i := range []int{1, 4} {
		if comments[i].Ignored != nil {
			t.Fatal("unrelated comment suppressed", comments[i].ID)
		}
	}
	if comments[0].Ignored.SourceCommentID != root.ID {
		t.Fatal("inherited reaction source lost")
	}
	// A marked reply affects its descendants, not its parent or siblings.
	for i := range comments {
		comments[i].Ignored = nil
	}
	comments[2].Ignored = &PRFeedbackIgnore{ReactionID: "ignored-2"}
	inheritFeedbackIgnores(comments)
	if comments[0].Ignored == nil || comments[3].Ignored != nil || comments[1].Ignored != nil {
		t.Fatal("reply ignore escaped its subtree")
	}
}

func TestFeedbackConversationDoesNotGuessAmbiguousQuotes(t *testing.T) {
	comments := []PRFeedbackComment{generalFeedback("comment:1", "Same wording"), generalFeedback("comment:2", "Same wording"), generalFeedback("comment:3", "> Same wording\n\nA reply")}
	linkFeedbackConversations(comments, "https://github.com/example/project/pull/1")
	if len(comments[2].ParentIDs) != 0 {
		t.Fatal("ambiguous quote assigned a parent")
	}
}

func TestGitHubThumbsDownIgnoresReplies(t *testing.T) {
	for _, kind := range []string{"inline", "comment"} {
		t.Run(kind, func(t *testing.T) { testGitHubThumbsDownIgnoresReplies(t, kind) })
	}
}

func testGitHubThumbsDownIgnoresReplies(t *testing.T, kind string) {
	p, f, plan, pr, item := thumbsDownFixture(t, kind)
	reply := item
	reply.ID = 2
	reply.NodeID = "node-2"
	reply.Body = "Use the existing retry helper."
	reply.ReplyTo = item.ID
	endpoint := "repos/example/project/pulls/1/comments"
	if kind == "comment" {
		endpoint = "repos/example/project/issues/1/comments"
		reply.Body = "> " + item.Body + "\n\n" + reply.Body
	}
	f.items[endpoint] = []githubFeedbackItem{item, reply}
	if kind == "inline" {
		f.threads[0]["comments"] = map[string]any{"nodes": []map[string]any{{"databaseId": 1}, {"databaseId": 2}}, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	base := thumbsDownResponses(item, kind, p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", item.Updated, true)
	marked := true
	f.reactionCommand = func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			return json.Marshal(map[string]any{"data": map[string]any{"nodes": []any{
				map[string]any{"id": item.NodeID, "reactionGroups": []any{map[string]any{"content": "THUMBS_DOWN", "viewerHasReacted": marked}}},
				map[string]any{"id": reply.NodeID, "reactionGroups": []any{}},
			}}})
		}
		return base(args)
	}
	for _, wantIgnored := range []bool{true, false} {
		marked = wantIgnored
		comments, err := p.Feedback(context.Background(), plan, pr)
		if err != nil {
			t.Fatal(err)
		}
		if len(comments) != 2 {
			t.Fatal("lost thread reply")
		}
		for _, c := range comments {
			if (c.Ignored != nil) != wantIgnored {
				t.Fatal("ignore inheritance or removal failed", c.ID)
			}
		}
	}
}

func TestFeedbackPromptIncludesAssessedConversationContext(t *testing.T) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	root := generalFeedback("comment:1", "Original issue")
	child := generalFeedback("comment:2", "The correction should reuse the existing parser.")
	child.ParentIDs = []string{root.ID}
	rootAssessment := assessedOutcome(root, "no_change").PRFeedbackAssessments[0]
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{
		{Comment: root, Fingerprint: feedbackFingerprint(root), StartedAttempt: j.Attempt, Assessment: &rootAssessment},
		{Comment: child, Fingerprint: feedbackFingerprint(child)},
	}}
	if err := r.feedbackPrompt(dir, j); err != nil {
		t.Fatal(err)
	}
	groups := feedbackConversations(j.PRFeedback)
	if len(groups) != 1 || len(groups[0].Comments) != 2 {
		t.Fatal("conversation split", groups)
	}
	if !strings.Contains(j.Feedback, root.Body) || !strings.Contains(j.Feedback, child.Body) {
		t.Fatal("parent or fix guidance omitted")
	}
	pending := pendingFeedbackEntries(j)
	if len(pending) != 1 || pending[0].Comment.ID != child.ID {
		t.Fatal("assessed parent was reopened")
	}
	j.PRFeedback.Entries[0].Comment.Ignored = &PRFeedbackIgnore{ReactionID: "marker"}
	comments := []PRFeedbackComment{j.PRFeedback.Entries[0].Comment, child}
	inheritFeedbackIgnores(comments)
	j.PRFeedback.Entries[1].Comment = comments[1]
	if groups := feedbackConversations(j.PRFeedback); len(groups) != 0 {
		t.Fatal("ignored content reached conversation prompt")
	}
}

func TestRocketMarksParentAddressedButKeepsReplyActionable(t *testing.T) {
	p, f, plan, pr, item := thumbsDownFixture(t, "comment")
	reply := githubItem(2, "> "+item.Body+"\n\nPlease also cover the new failure case.", "reviewer", "User")
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item, reply}
	base := thumbsDownResponses(item, "comment", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", item.Updated, true)
	f.reactionCommand = func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			return json.Marshal(map[string]any{"data": map[string]any{"nodes": []any{
				map[string]any{"id": item.NodeID, "reactionGroups": []any{map[string]any{"content": "ROCKET", "viewerHasReacted": true}}},
				map[string]any{"id": reply.NodeID, "reactionGroups": []any{}},
			}}})
		}
		data, err := base(args)
		return []byte(strings.ReplaceAll(string(data), "THUMBS_DOWN", "ROCKET")), err
	}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 2 {
		t.Fatal("collection failed", err)
	}
	if comments[0].Addressed == nil || comments[0].Ignored != nil || comments[1].Addressed != nil || comments[1].Ignored != nil {
		t.Fatal("rocket suppressed a reply or became ignore")
	}
	history := &PRFeedbackHistory{Active: true}
	for _, c := range comments {
		history.Entries = append(history.Entries, PRFeedbackEntry{Comment: c})
	}
	pending := pendingFeedbackEntries(&Journal{PRFeedback: history})
	if len(pending) != 1 || pending[0].Comment.ID != "comment:2" {
		t.Fatal("addressed parent became actionable or reply was lost")
	}
	conversations := feedbackConversations(history)
	if len(conversations) != 1 || len(conversations[0].Comments) != 2 {
		t.Fatal("addressed parent was lost as context")
	}
	status := PRFeedbackStatus(Journal{PRFeedback: history})
	if status.Addressed != 1 || status.Ignored != 0 || status.Pending != 1 || status.OutstandingReplies != 0 {
		t.Fatal("incorrect reaction status", status)
	}
}

func TestRocketAddressedCommentDoesNotReceiveAnotherAcknowledgement(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "This correction is already complete.")
	comment.Addressed = &PRFeedbackIgnore{By: "example", UserID: 123, ReactionID: "rocket-1", CreatedAt: "2026-10-15T12:00:00Z"}
	assessment := assessedOutcome(comment, "no_change").PRFeedbackAssessments[0]
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{{Comment: comment, Fingerprint: feedbackFingerprint(comment), Assessment: &assessment}}}
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if pub.replies != 0 {
		t.Fatal("already-addressed comment received a duplicate reply")
	}
}
