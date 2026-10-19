package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type conversationReplyPublisher struct {
	*feedbackPublisherFake
	bodies, markers []string
}

func (p *conversationReplyPublisher) ReplyFeedback(ctx context.Context, plan Plan, pr Publication, c PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	p.bodies = append(p.bodies, body)
	p.markers = append(p.markers, marker)
	return p.feedbackPublisherFake.ReplyFeedback(ctx, plan, pr, c, marker, body)
}
func conversationReplyFixture(t *testing.T) (string, *Journal, Runner, *conversationReplyPublisher) {
	dir, j, r, p, _ := feedbackReadyRun(t)
	root := generalFeedback("comment:1", "Initial concern")
	child := generalFeedback("comment:2", "Use the simpler implementation")
	child.ParentIDs = []string{root.ID}
	h := &PRFeedbackHistory{Version: 1, Active: true, Cycle: 1, CandidateStart: testHead}
	for _, c := range []PRFeedbackComment{root, child} {
		a := assessedOutcome(c, "no_change").PRFeedbackAssessments[0]
		a.Reason = "Assessment of " + c.ID
		e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason}
		e.Marker = feedbackEntryMarker(j.ID, e)
		h.Entries = append(h.Entries, e)
	}
	j.PRFeedback = h
	pub := &conversationReplyPublisher{feedbackPublisherFake: p}
	r.Publisher = pub
	return dir, j, r, pub
}
func TestConversationReceivesOneReplyForRootAndChildren(t *testing.T) {
	dir, j, r, p := conversationReplyFixture(t)
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if p.replies != 1 || len(p.bodies) != 1 || !strings.Contains(p.bodies[0], "Assessment of comment:1") || !strings.Contains(p.bodies[0], "Assessment of comment:2") {
		t.Fatalf("conversation split into multiple replies: replies %d bodies %v", p.replies, p.bodies)
	}
	entries := j.PRFeedback.Entries
	if entries[0].Reply == nil || entries[1].Reply == nil || *entries[0].Reply != *entries[1].Reply {
		t.Fatal("single receipt not associated with every assessed input")
	}
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil || p.replies != 1 {
		t.Fatal("repeat acknowledgement", err, p.replies)
	}
}
func TestConversationReplyFailureRetainsOneRetryIntent(t *testing.T) {
	dir, j, r, p := conversationReplyFixture(t)
	p.replyErr = errors.New("temporary reply failure")
	if err := r.replyPRFeedback(context.Background(), dir, j); err == nil {
		t.Fatal("expected failure")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.replyErr = nil
	if err := r.replyPRFeedback(context.Background(), dir, &saved); err != nil {
		t.Fatal(err)
	}
	if len(p.bodies) != 2 || p.bodies[0] != p.bodies[1] || p.markers[0] != p.markers[1] || p.replies != 2 {
		t.Fatal("retry changed conversation intent or sent separate child acknowledgement")
	}
}
func TestConversationWaitsForAllPendingAssessmentsBeforeReply(t *testing.T) {
	dir, j, r, p := conversationReplyFixture(t)
	j.PRFeedback.Entries[1].Assessment = nil
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if p.replies != 0 {
		t.Fatal("partial conversation received premature response")
	}
}
func TestRocketClosesDescendantsWithoutClosingSiblingConversation(t *testing.T) {
	root := generalFeedback("inline:1", "root")
	root.Addressed = &PRFeedbackIgnore{By: "operator", UserID: 1, ReactionID: "r", CreatedAt: "2026-10-19T12:00:00Z"}
	child := generalFeedback("inline:2", "child")
	child.ParentIDs = []string{root.ID}
	grandchild := generalFeedback("inline:3", "grandchild")
	grandchild.ParentIDs = []string{child.ID}
	sibling := generalFeedback("inline:4", "separate")
	comments := []PRFeedbackComment{grandchild, sibling, child, root}
	inheritFeedbackAddressed(comments)
	if comments[0].Addressed == nil || comments[2].Addressed == nil || comments[0].Addressed.SourceCommentID != root.ID || comments[1].Addressed != nil {
		t.Fatal("rocket was not inherited through its subtree")
	}
}
func TestConversationStatusCountsOneOutstandingReply(t *testing.T) {
	_, j, _, _ := conversationReplyFixture(t)
	if s := PRFeedbackStatus(*j); s.OutstandingReplies != 1 {
		t.Fatalf("reply count is per comment: %+v", s)
	}
}

func TestGitHubConversationReplyRetriesOnePostAndRevalidatesChild(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	root := githubItem(1, "Original concern", "reviewer", "User")
	child := githubItem(2, "> Original concern\n\nSimplify the implementation", "reviewer", "User")
	endpoint := "repos/example/project/issues/1/comments"
	f.items[endpoint] = []githubFeedbackItem{root, child}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	marker := feedbackMarker("run-one", comments[0].ID, feedbackFingerprint(comments[0]))
	body := "Handled the concern using the clarification."
	f.ambiguous = true
	if _, err := p.ReplyFeedbackConversation(context.Background(), plan, pr, comments[0], comments, marker, body); err == nil {
		t.Fatal("expected uncertain POST response")
	}
	if _, err := p.ReplyFeedbackConversation(context.Background(), plan, pr, comments[0], comments, marker, body); err != nil || f.posts != 1 {
		t.Fatalf("duplicate grouped POST or failed reconciliation: %v posts %d", err, f.posts)
	}
	items := f.items[endpoint]
	items[1].Body = "Changed guidance"
	f.items[endpoint] = items
	if _, err := p.ReplyFeedbackConversation(context.Background(), plan, pr, comments[0], comments, marker, body); err == nil || f.posts != 1 {
		t.Fatal("edited child accepted as unchanged group input")
	}
}
func TestConversationNewReplyUsesNewMarkerWithoutReacknowledgingParent(t *testing.T) {
	dir, j, r, p := conversationReplyFixture(t)
	original := &j.PRFeedback.Entries[0]
	original.Reply = &PRFeedbackReply{ID: "old-reply"}
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if p.replies != 1 || p.markers[0] != j.PRFeedback.Entries[1].Marker || strings.Contains(p.bodies[0], "Assessment of comment:1") || !strings.Contains(p.bodies[0], "Assessment of comment:2") {
		t.Fatal("new clarification repeated the parent acknowledgement")
	}
}

func TestInlineThreadRootClosesReplyThroughExcludedIntermediate(t *testing.T) {
	p, f, plan, pr, root := thumbsDownFixture(t, "inline")
	child := githubItem(2, "Reply below automation", "reviewer", "User")
	child.ReplyTo = 901
	child.Path = root.Path
	f.items["repos/example/project/pulls/1/comments"] = []githubFeedbackItem{root, child}
	f.threads = []map[string]any{{"id": "thread-1", "comments": map[string]any{"nodes": []any{map[string]any{"databaseId": 1}, map[string]any{"databaseId": 2}}, "pageInfo": map[string]any{"hasNextPage": false}}}}
	reaction := thumbsDownResponses(root, "inline", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", root.Updated, true)
	f.reactionCommand = func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			return json.Marshal(map[string]any{"data": map[string]any{"nodes": []any{
				map[string]any{"id": root.NodeID, "reactionGroups": []any{map[string]any{"content": "ROCKET", "viewerHasReacted": true}}},
				map[string]any{"id": child.NodeID, "reactionGroups": []any{}},
			}}})
		}
		data, err := reaction(args)
		return []byte(strings.ReplaceAll(string(data), "THUMBS_DOWN", "ROCKET")), err
	}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[1].Addressed == nil || comments[1].Addressed.SourceCommentID != "inline:1" {
		t.Fatal("thread root closure was lost behind excluded intermediate")
	}
	for _, c := range comments {
		if err := validateFeedbackComment(c); err != nil {
			t.Fatal(err)
		}
	}
}
