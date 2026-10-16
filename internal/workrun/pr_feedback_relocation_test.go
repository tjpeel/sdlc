package workrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func capturedInlineFeedback(t *testing.T) (GitHubPublisher, *feedbackGitHubFake, Plan, Publication, PRFeedbackComment) {
	t.Helper()
	p, f, plan, pr := githubFeedbackSetup(t)
	item := githubItem(1, "Rename the response handler", "reviewer", "User")
	item.Path, item.Line, item.OriginalLine, item.OriginalPosition, item.OriginalCommit, item.DiffHunk = "old_handler.go", 16, 16, 15, testBase, "@@ -14,4 +14,4 @@\n original handler"
	f.items["repos/example/project/pulls/1/comments"] = []githubFeedbackItem{item}
	f.threads = []map[string]any{{"id": "original-thread", "isOutdated": false, "isResolved": false, "comments": map[string]any{"nodes": []map[string]any{{"databaseId": 1}}, "pageInfo": map[string]any{"hasNextPage": false}}}}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 1 {
		t.Fatal("inline capture failed", err)
	}
	return p, f, plan, pr, comments[0]
}

func TestGitHubInlineRenameAcknowledgesOriginalThreadExactlyOnce(t *testing.T) {
	p, f, plan, pr, captured := capturedInlineFeedback(t)
	item := f.items["repos/example/project/pulls/1/comments"][0]
	item.Path, item.Line = "response_handler.go", 20
	f.items["repos/example/project/pulls/1/comments"] = []githubFeedbackItem{item}
	f.threads[0]["isOutdated"] = true
	marker := feedbackMarker("run-one", captured.ID, feedbackFingerprint(captured))
	first, err := p.ReplyFeedback(context.Background(), plan, pr, captured, marker, "Renamed the handler in the correction commit.")
	if err != nil || f.posts != 1 || first.ID == "" {
		t.Fatal("derived location change blocked the original-thread reply", err)
	}
	second, err := p.ReplyFeedback(context.Background(), plan, pr, captured, marker, "Renamed the handler in the correction commit.")
	if err != nil || first != second || f.posts != 1 {
		t.Fatal("same captured intent was posted twice", err)
	}
	replies := f.items["repos/example/project/pulls/1/comments"]
	if len(replies) != 2 || replies[1].ReplyTo != captured.ReplyID {
		t.Fatal("relocated feedback reply escaped its original thread")
	}
	if replies[1].Body != "Renamed the handler in the correction commit.\n\n"+marker {
		t.Fatal("inline response unexpectedly quoted or reformatted the original comment")
	}

}

func TestInlineHumanOrOriginalAnchorChangesStillReconcile(t *testing.T) {
	for _, change := range []string{"body", "updated", "author", "original_commit", "original_line", "original_position", "diff_hunk", "thread", "resolved", "missing"} {
		t.Run(change, func(t *testing.T) {
			p, f, plan, pr, captured := capturedInlineFeedback(t)
			item := f.items["repos/example/project/pulls/1/comments"][0]
			item.Path, item.Line = "response_handler.go", 20
			switch change {
			case "body":
				item.Body = "A different human request"
			case "updated":
				item.Updated = "2026-10-14T17:00:00Z"
			case "author":
				item.User.Login = "another-reviewer"
			case "original_commit":
				item.OriginalCommit = testTree
			case "original_line":
				item.OriginalLine = 17
			case "original_position":
				item.OriginalPosition = 16
			case "diff_hunk":
				item.DiffHunk = "@@ -10,2 +10,2 @@\n different original context"
			case "thread":
				f.threads[0]["id"] = "different-thread"
			case "resolved":
				f.threads[0]["isResolved"] = true
			}
			f.items["repos/example/project/pulls/1/comments"] = []githubFeedbackItem{item}
			if change == "missing" {
				f.items["repos/example/project/pulls/1/comments"] = nil
			}
			_, err := p.ReplyFeedback(context.Background(), plan, pr, captured, feedbackMarker("run-one", captured.ID, feedbackFingerprint(captured)), "Recorded correction")
			var changed *FeedbackRevisionChangedError
			if !errors.As(err, &changed) || f.posts != 0 {
				t.Fatal("actual revision change did not reconcile before posting", change, err)
			}
		})
	}
}

func TestRelocatedInlineFeedbackDoesNotStartNewImplementationCycle(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	_, _, _, _, captured := capturedInlineFeedback(t)
	pub.comments = []PRFeedbackComment{captured}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(captured, "no_change"), testOutcome("implemented"), testOutcome("reviewed")}}
	r.Provider, r.ProcessPRFeedback = provider, true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	entry := j.PRFeedback.Entries[0]
	current := captured
	current.Path, current.Line, current.Outdated = "response_handler.go", 20, true
	pub.comments = []PRFeedbackComment{current}
	beforeAttempts, beforeChecks, beforeReplies := j.Attempt, checker.calls, pub.replies
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.Attempt != beforeAttempts || checker.calls != beforeChecks || pub.replies != beforeReplies || len(j.PRFeedback.Entries) != 1 || j.PRFeedback.Entries[0].Fingerprint != entry.Fingerprint || !reflect.DeepEqual(j.PRFeedback.Entries[0].Comment, entry.Comment) || j.PRFeedback.Entries[0].Marker != entry.Marker {
		t.Fatal("derived location update redispatched or replaced captured feedback")
	}
	current.Body = "A new human correction"
	pub.comments = []PRFeedbackComment{current}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if len(j.PRFeedback.Entries) != 2 || pendingFeedback(j) == nil || pendingFeedback(j).Comment.Body != current.Body || j.State != "repairing" {
		t.Fatal("real human edit was suppressed after relocation")
	}
}

func TestMissingOriginalAnchorCannotHideDisplayChange(t *testing.T) {
	_, _, _, _, original := capturedInlineFeedback(t)
	for _, missing := range []string{"commit", "location", "hunk", "thread", "reply"} {
		t.Run(missing, func(t *testing.T) {
			captured := original
			switch missing {
			case "commit":
				captured.OriginalCommit = ""
			case "location":
				captured.OriginalLine, captured.OriginalPosition = 0, 0
			case "hunk":
				captured.DiffHunk = ""
			case "thread":
				captured.ThreadID = ""
			case "reply":
				captured.ReplyID = 0
			}
			current := captured
			current.Path, current.Line = "response_handler.go", 20
			if sameFeedbackRevision(captured, current) {
				t.Fatal("missing original anchor allowed display metadata to hide a revision")
			}
		})
	}
}
