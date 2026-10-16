package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The fake is at the existing official gh boundary. Each test observes the
// collected comment and its eligibility, not just the query text.
func thumbsDownFixture(t *testing.T, kind string) (GitHubPublisher, *feedbackGitHubFake, Plan, Publication, githubFeedbackItem) {
	p, f, plan, pr := githubFeedbackSetup(t)
	item := githubItem(1, "Handled feedback", "reviewer", "User")
	endpoint := "repos/example/project/issues/1/comments"
	if kind == "review" {
		endpoint = "repos/example/project/pulls/1/reviews"
	}
	if kind == "inline" {
		endpoint = "repos/example/project/pulls/1/comments"
		item.Path, item.OriginalCommit, item.OriginalLine, item.DiffHunk = "api.go", testBase, 1, "@@ original"
		f.threads = []map[string]any{{"id": "thread-1", "isOutdated": false, "isResolved": false, "comments": map[string]any{"nodes": []map[string]any{{"databaseId": 1}}, "pageInfo": map[string]any{"hasNextPage": false}}}}
	}
	f.items[endpoint] = []githubFeedbackItem{item}
	return p, f, plan, pr, item
}

func thumbsDownResponses(item githubFeedbackItem, kind, actor string, actorID int64, created, updated string, viewer bool) func([]string) ([]byte, error) {
	return func(args []string) ([]byte, error) {
		var result any
		if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			result = map[string]any{"data": map[string]any{"nodes": []any{map[string]any{"id": item.NodeID, "reactionGroups": []any{map[string]any{"content": "THUMBS_DOWN", "viewerHasReacted": viewer}}}}}}
		} else {
			result = map[string]any{"data": map[string]any{"node": map[string]any{"id": item.NodeID, "__typename": map[string]string{"comment": "IssueComment", "review": "PullRequestReview", "inline": "PullRequestReviewComment"}[kind], "databaseId": fmt.Sprintf("%d", item.ID), "body": item.Body, "updatedAt": updated, "reactions": map[string]any{"nodes": []any{map[string]any{"id": "thumbs-down-1", "content": "THUMBS_DOWN", "createdAt": created, "user": map[string]any{"login": actor, "databaseId": actorID}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}
		}
		return json.Marshal(result)
	}
}

func TestGitHubThumbsDownIgnoresOnlyFrozenAccountReactionOnUnchangedFeedback(t *testing.T) {
	for _, kind := range []string{"comment", "review", "inline"} {
		for _, mode := range []string{"handled", "another account", "wrong numeric identity", "edited", "removed"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				p, f, plan, pr, item := thumbsDownFixture(t, kind)
				actor, id, viewer := p.Frozen.GitHubLogin, p.Frozen.GitHubID, true
				created, updated := "2026-10-15T12:00:00Z", item.Updated
				switch mode {
				case "another account":
					actor, id = "other", 456
				case "wrong numeric identity":
					id = 456
				case "edited":
					updated = "2026-10-15T13:00:00Z"
				case "removed":
					viewer = false
				}
				f.reactionCommand = thumbsDownResponses(item, kind, actor, id, created, updated, viewer)
				comments, err := p.Feedback(context.Background(), plan, pr)
				if err != nil || len(comments) != 1 {
					t.Fatal(comments, err)
				}
				if (comments[0].Ignored != nil) != (mode == "handled") {
					t.Fatal("incorrect feedback suppression", comments)
				}
				j := Journal{PRFeedback: &PRFeedbackHistory{Active: true, Cycle: 1, Entries: []PRFeedbackEntry{{Comment: comments[0], Fingerprint: feedbackFingerprint(comments[0])}}}}
				pending := pendingFeedbackEntries(&j)
				if mode == "handled" {
					if len(pending) != 0 || PRFeedbackStatus(j).Ignored != 1 || PRFeedbackStatus(j).Pending != 0 {
						t.Fatal("handled feedback remains actionable")
					}
					ignored := comments[0].Ignored
					if ignored.By != p.Frozen.GitHubLogin || ignored.UserID != p.Frozen.GitHubID || ignored.ReactionID != "thumbs-down-1" {
						t.Fatal("ignore evidence missing", ignored)
					}
				} else if len(pending) != 1 {
					t.Fatal("unhandled feedback lost")
				}
			})
		}
	}
}

func TestThumbsDownReadFailuresCannotSilentlySuppressComments(t *testing.T) {
	for _, mode := range []string{"request", "partial GraphQL", "wrong node", "wrong subject", "changed body", "invalid edit time", "invalid reaction time", "pagination"} {
		t.Run(mode, func(t *testing.T) {
			p, f, plan, pr, item := thumbsDownFixture(t, "comment")
			base := thumbsDownResponses(item, "comment", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", item.Updated, true)
			f.reactionCommand = func(args []string) ([]byte, error) {
				if mode == "request" {
					return nil, errors.New("offline failure")
				}
				if mode == "partial GraphQL" {
					return []byte(`{"errors":[{"message":"denied"}],"data":{"nodes":[]}}`), nil
				}
				data, err := base(args)
				if err != nil {
					return nil, err
				}
				if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
					return data, nil
				}
				switch mode {
				case "wrong node":
					data = []byte(strings.Replace(string(data), `"id":"node-1"`, `"id":"node-2"`, 1))
				case "wrong subject":
					data = []byte(strings.Replace(string(data), `"databaseId":"1",`, `"databaseId":"2",`, 1))
				case "changed body":
					data = []byte(strings.Replace(string(data), "Handled feedback", "Changed feedback", 1))
				case "invalid edit time":
					data = []byte(strings.Replace(string(data), item.Updated, "invalid", 1))
				case "invalid reaction time":
					data = []byte(strings.Replace(string(data), "2026-10-15T12:00:00Z", "invalid", 1))
				case "pagination":
					data = []byte(strings.Replace(string(data), `"hasNextPage":false`, `"hasNextPage":true,"endCursor":"repeated"`, 1))
				}
				return data, nil
			}
			if comments, err := p.Feedback(context.Background(), plan, pr); err == nil || comments != nil {
				t.Fatal("ambiguous reaction data accepted", comments, err)
			}
		})
	}
}

func TestResolvedThreadDoesNotRequireReactionRead(t *testing.T) {
	p, f, plan, pr, _ := thumbsDownFixture(t, "inline")
	f.threads[0]["isResolved"] = true
	f.reactionCommand = func([]string) ([]byte, error) { return nil, fmt.Errorf("resolved thread should not need reactions") }
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 1 || !comments[0].Resolved {
		t.Fatal(comments, err)
	}
}

func TestIgnoredCommentDoesNotSuppressUnrelatedComments(t *testing.T) {
	p, f, plan, pr, item := thumbsDownFixture(t, "comment")
	reply := githubItem(2, "New human follow-up", "reviewer", "User")
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item, reply}
	base := thumbsDownResponses(item, "comment", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", item.Updated, true)
	f.reactionCommand = func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			return json.Marshal(map[string]any{"data": map[string]any{"nodes": []any{
				map[string]any{"id": item.NodeID, "reactionGroups": []any{map[string]any{"content": "THUMBS_DOWN", "viewerHasReacted": true}}},
				map[string]any{"id": reply.NodeID, "reactionGroups": []any{}},
			}}})
		}
		return base(args)
	}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	j := Journal{PRFeedback: &PRFeedbackHistory{Active: true}}
	for _, c := range comments {
		j.PRFeedback.Entries = append(j.PRFeedback.Entries, PRFeedbackEntry{Comment: c})
	}
	pending := pendingFeedbackEntries(&j)
	if len(pending) != 1 || pending[0].Comment.ID != "comment:2" {
		t.Fatal("ignored parent swallowed new feedback", pending)
	}
}

func TestAdoptionRetainsThumbsDownSkipAndLaterProcessesUnmarkedComment(t *testing.T) {
	dir, j := testRun(t)
	a := adoptionBoundary()
	j.Plan.Adoption = &a
	j.Plan.SourceSHA, j.Plan.StartingSHA = testHead, testHead
	j.Plan.PRTitle, j.Plan.PRBody = a.Title, a.Body
	j.Plan.PublicationIdentity = frozenTestIdentity()
	j.State = "adopting"
	j.Publication = Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
	comment := generalFeedback("comment:1", "Unique ignored top-level feedback")
	comment.Ignored = &PRFeedbackIgnore{By: "example", UserID: 123, ReactionID: "thumbs-down-1", CreatedAt: "2026-10-15T12:00:00Z"}
	pub := &feedbackPublisherFake{fakePublisher: &fakePublisher{}, comments: []PRFeedbackComment{comment}}
	provider := &feedbackScriptProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("implemented"), testOutcome("reviewed")}}
	checks := &fakeChecker{}
	r := fakeRunner(&fakeProvider{}, checks, pub.fakePublisher, &fakeRepository{})
	r.Publisher, r.Provider = pub, provider
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || pub.replies != 0 || checks.calls != 1 || len(provider.sessions) != 3 || PRFeedbackStatus(*j).Ignored != 1 {
		t.Fatal("adoption did not honour skip", j.State, pub.replies, checks.calls)
	}
	for _, s := range provider.sessions {
		if strings.Contains(s.Prompt, comment.Body) {
			t.Fatal("ignored body reached provider context")
		}
	}
	saved, err := Load(dir)
	if err != nil || saved.PRFeedback.Entries[0].Comment.Ignored == nil {
		t.Fatal("skip evidence lost", err)
	}
	comment.Ignored = nil
	pub.comments = []PRFeedbackComment{comment}
	provider.outcomes = []Outcome{assessedOutcome(comment, "no_change"), testOutcome("implemented"), testOutcome("reviewed")}
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(j.PRFeedback.Entries) != 2 || pub.replies != 1 || PRFeedbackStatus(*j).Ignored != 0 {
		t.Fatal("removing marker did not restore feedback eligibility")
	}
}

func TestThumbsDownFindsConfiguredAccountAfterFirstPageWithLargeCommentID(t *testing.T) {
	p, f, plan, pr, _ := thumbsDownFixture(t, "comment")
	item := githubItem(6345345111, "Handled feedback", "reviewer", "User")
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item}
	base := thumbsDownResponses(item, "comment", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", item.Updated, true)
	reads := 0
	f.reactionCommand = func(args []string) ([]byte, error) {
		data, err := base(args)
		if err != nil || strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			return data, err
		}
		reads++
		if reads == 1 {
			data = []byte(strings.Replace(string(data), `"login":"example"`, `"login":"another"`, 1))
			data = []byte(strings.Replace(string(data), `"databaseId":123`, `"databaseId":456`, 1))
			data = []byte(strings.Replace(string(data), `"hasNextPage":false`, `"hasNextPage":true,"endCursor":"next"`, 1))
		}
		return data, nil
	}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 1 || comments[0].ID != "comment:6345345111" || comments[0].Ignored == nil || reads != 2 {
		t.Fatal("paginated reaction or large subject ID lost", comments, reads, err)
	}
}

func TestThumbsDownUsesBodyEditTimeRatherThanOtherCommentUpdates(t *testing.T) {
	p, f, plan, pr, item := thumbsDownFixture(t, "comment")
	item.Updated = "2026-10-15T13:00:00Z" // An unrelated metadata update after the reaction.
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item}
	base := thumbsDownResponses(item, "comment", p.Frozen.GitHubLogin, p.Frozen.GitHubID, "2026-10-15T12:00:00Z", "", true)
	f.reactionCommand = func(args []string) ([]byte, error) {
		data, err := base(args)
		if err == nil && !strings.Contains(strings.Join(args, " "), "query SDLCFeedbackReactionGroups") {
			data = []byte(strings.Replace(string(data), `"updatedAt":""`, `"updatedAt":null,"createdAt":"2026-10-13T12:00:00Z"`, 1))
		}
		return data, err
	}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 1 || comments[0].Ignored == nil {
		t.Fatal("unrelated metadata update invalidated skip", comments, err)
	}
}
