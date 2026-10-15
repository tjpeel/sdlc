package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

type feedbackGitHubFake struct {
	t                        *testing.T
	items                    map[string][]githubFeedbackItem
	threads                  []map[string]any
	posts, views             int
	ambiguous                bool
	wrongHead, wrongIdentity bool
	moreThreads              bool
}

func (f *feedbackGitHubFake) command(_ context.Context, name string, args ...string) ([]byte, error) {
	f.t.Helper()
	if name != "gh" {
		f.t.Fatal("unexpected executable")
	}
	encode := func(v any) []byte {
		data, err := json.Marshal(v)
		if err != nil {
			f.t.Fatal(err)
		}
		return data
	}
	if args[0] == "pr" {
		f.views++
		head := testHead
		if f.wrongHead {
			head = testTree
		}
		return encode(map[string]any{"number": 1, "url": "https://github.com/example/project/pull/1", "state": "OPEN", "headRefName": "TASK-1", "headRefOid": head, "baseRefName": "main", "baseRefOid": testBase}), nil
	}
	if args[0] != "api" {
		f.t.Fatal("unexpected gh command")
	}
	endpoint := args[1]
	if endpoint == "user" {
		id := 123
		if f.wrongIdentity {
			id = 456
		}
		return encode(map[string]any{"id": id, "login": "example"}), nil
	}
	if endpoint == "repos/example/project" {
		return []byte(`{"id":99,"full_name":"example/project"}`), nil
	}
	if endpoint == "graphql" {
		return encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": f.threads, "pageInfo": map[string]any{"hasNextPage": f.moreThreads, "endCursor": "same-cursor"}}}}}}), nil
	}
	if strings.Contains(strings.Join(args, " "), "--method POST") {
		f.posts++
		body := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "body=") {
				body = strings.TrimPrefix(arg, "body=")
			}
		}
		item := githubFeedbackItem{ID: int64(900 + f.posts), Body: body, URL: "https://github.com/example/project/pull/1#issuecomment-900"}
		item.User.Login = "example"
		item.User.Type = "User"
		destination := "repos/example/project/issues/1/comments"
		if strings.HasSuffix(endpoint, "/replies") {
			destination = "repos/example/project/pulls/1/comments"
			item.ReplyTo = 1
		}
		f.items[destination] = append(f.items[destination], item)
		if f.ambiguous {
			f.ambiguous = false
			return nil, errors.New("network ended after POST")
		}
		return encode(item), nil
	}
	pieces := strings.Split(endpoint, "?")
	if len(pieces) != 2 {
		f.t.Fatalf("reader must use bounded pagination: %v", args)
	}
	page := 1
	if _, err := fmt.Sscanf(pieces[1], "per_page=100&page=%d", &page); err != nil {
		f.t.Fatal(err)
	}
	items := f.items[pieces[0]]
	start := (page - 1) * 100
	if start >= len(items) {
		return []byte(`[]`), nil
	}
	end := start + 100
	if end > len(items) {
		end = len(items)
	}
	return encode(items[start:end]), nil
}
func githubItem(id int64, body, author, kind string) githubFeedbackItem {
	item := githubFeedbackItem{ID: id, Body: body, Updated: "2026-10-13T12:00:00Z"}
	item.User.Login = author
	item.User.Type = kind
	return item
}
func githubFeedbackSetup(t *testing.T) (GitHubPublisher, *feedbackGitHubFake, Plan, Publication) {
	t.Helper()
	f := &feedbackGitHubFake{t: t, items: map[string][]githubFeedbackItem{}, threads: []map[string]any{}}
	p := GitHubPublisher{Command: f.command, Frozen: frozenTestIdentity()}
	plan := Plan{Repository: "example/project", Branch: "TASK-1", Base: "main", BaseSHA: testBase}
	pr := Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	return p, f, plan, pr
}
func TestGitHubPRFeedbackIncludesOriginalInlineReviewAndGeneralContext(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	inline := githubItem(1, "Original inline question", "reviewer", "User")
	inline.Path = "api.go"
	inline.OriginalLine = 9
	inline.OriginalPosition = 8
	inline.OriginalCommit = testBase
	inline.DiffHunk = "@@ -9 +9 @@ original"
	inline.Line = 0
	f.items["repos/example/project/pulls/1/comments"] = []githubFeedbackItem{inline}
	f.threads = []map[string]any{{"id": "thread-1", "isOutdated": true, "isResolved": false, "comments": map[string]any{"nodes": []map[string]any{{"databaseId": 1}}, "pageInfo": map[string]any{"hasNextPage": false}}}}
	f.items["repos/example/project/pulls/1/reviews"] = []githubFeedbackItem{githubItem(2, "Summary decision", "reviewer", "User")}
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{githubItem(3, "General comment", "reviewer", "User"), githubItem(4, "Automated result", "ci-bot", "Bot"), githubItem(5, "prior\n\n"+feedbackMarker("run", "comment:3", strings.Repeat("a", 64)), "example", "User")}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 3 {
		t.Fatalf("human comments=%+v", comments)
	}
	var c PRFeedbackComment
	for _, v := range comments {
		if v.Kind == "inline" {
			c = v
		}
	}
	if !c.Outdated || c.Resolved || c.ReplyID != 1 || c.ThreadID != "thread-1" || c.OriginalCommit != testBase || c.OriginalLine != 9 || c.OriginalPosition != 8 || c.DiffHunk != inline.DiffHunk {
		t.Fatalf("original inline context lost: %+v", c)
	}
}
func TestGitHubPRFeedbackPaginatesBeforeFiltering(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	items := []githubFeedbackItem{}
	for i := int64(1); i <= 100; i++ {
		items = append(items, githubItem(i, "bot result", "bot", "Bot"))
	}
	items = append(items, githubItem(101, "human on second page", "reviewer", "User"))
	f.items["repos/example/project/issues/1/comments"] = items
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 1 || comments[0].ID != "comment:101" {
		t.Fatalf("comments=%+v err=%v", comments, err)
	}
}
func TestGitHubPRFeedbackReplyReconcilesAmbiguousPost(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	item := githubItem(1, "Human question", "reviewer", "User")
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	c := comments[0]
	marker := feedbackMarker("run-one", c.ID, feedbackFingerprint(c))
	body := "Needs a product decision."
	f.ambiguous = true
	if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, body); err == nil {
		t.Fatal("unconfirmed POST accepted")
	}
	ack, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, body)
	if err != nil || ack.ID != strconv.Itoa(901) || f.posts != 1 {
		t.Fatalf("ack=%+v posts=%d err=%v", ack, f.posts, err)
	}
	// A remote marker with different content is never silently acknowledged.
	items := f.items["repos/example/project/issues/1/comments"]
	items[1].Body = "different body\n" + marker
	f.items["repos/example/project/issues/1/comments"] = items
	if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, body); err == nil || f.posts != 1 {
		t.Fatal("different remote intent accepted")
	}
}
func TestGitHubPRFeedbackRefusesChangedAccountHeadOrCommentBeforeReply(t *testing.T) {
	for _, change := range []string{"account", "head", "body"} {
		t.Run(change, func(t *testing.T) {
			p, f, plan, pr := githubFeedbackSetup(t)
			item := githubItem(1, "Human question", "reviewer", "User")
			f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item}
			comments, err := p.Feedback(context.Background(), plan, pr)
			if err != nil {
				t.Fatal(err)
			}
			c := comments[0]
			switch change {
			case "account":
				f.wrongIdentity = true
			case "head":
				f.wrongHead = true
			case "body":
				item.Body = "Changed human question"
				f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{item}
			}
			if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, feedbackMarker("run-one", c.ID, feedbackFingerprint(c)), "answer"); err == nil || f.posts != 0 {
				t.Fatal("changed feedback boundary mutated")
			}
		})
	}
}
func TestGitHubPRFeedbackThreadPaginationCannotLoop(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	f.moreThreads = true
	if _, err := p.Feedback(context.Background(), plan, pr); err == nil {
		t.Fatal("repeated thread cursor accepted")
	}
}

func TestGitHubPRFeedbackPreservesHumanQuotesOfControllerMarkers(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	quoted := "The earlier response said:\n\n" + feedbackMarker("run-one", "comment:1", strings.Repeat("a", 64))
	f.items["repos/example/project/issues/1/comments"] = []githubFeedbackItem{githubItem(1, quoted, "reviewer", "User"), githubItem(2, "Please explain <!-- sdlc-pr-feedback: formatting", "reviewer", "User")}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil || len(comments) != 2 {
		t.Fatalf("human quote lost: %+v err=%v", comments, err)
	}
}

func TestGitHubTopLevelRepliesQuoteAndLinkOriginalFeedback(t *testing.T) {
	for _, kind := range []string{"comment", "review"} {
		t.Run(kind, func(t *testing.T) {
			p, f, plan, pr := githubFeedbackSetup(t)
			source := "repos/example/project/issues/1/comments"
			anchor := "issuecomment-1"
			label := "original comment"
			if kind == "review" {
				source = "repos/example/project/pulls/1/reviews"
				anchor = "pullrequestreview-1"
				label = "original review"
			}
			f.items[source] = []githubFeedbackItem{githubItem(1, "First paragraph.\r\n\r\n```go\rreturn nil\n```", "reviewer", "User")}
			comments, err := p.Feedback(context.Background(), plan, pr)
			if err != nil {
				t.Fatal(err)
			}
			c := comments[0]
			marker := feedbackMarker("run-one", c.ID, feedbackFingerprint(c))
			if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, "Corrected in the signed commit."); err != nil {
				t.Fatal(err)
			}
			replies := f.items["repos/example/project/issues/1/comments"]
			got := replies[len(replies)-1].Body
			want := "In reply to [the " + label + "](" + pr.URL + "#" + anchor + "):\n\n> First paragraph.\n> \n> ```go\n> return nil\n> ```\n\nCorrected in the signed commit.\n\n" + marker
			if got != want {
				t.Fatalf("quote reply differs:\ngot %q\nwant %q", got, want)
			}
			if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, "Corrected in the signed commit."); err != nil || f.posts != 1 {
				t.Fatal("quote reply duplicated on resume", err, f.posts)
			}
		})
	}
}

func TestGitHubQuoteReplyReconcilesEarlierIDOnlyAcknowledgement(t *testing.T) {
	p, f, plan, pr := githubFeedbackSetup(t)
	endpoint := "repos/example/project/issues/1/comments"
	f.items[endpoint] = []githubFeedbackItem{githubItem(1, "Original question", "reviewer", "User")}
	comments, err := p.Feedback(context.Background(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	c := comments[0]
	marker := feedbackMarker("run-one", c.ID, feedbackFingerprint(c))
	body := "Recorded correction."
	legacy := githubItem(901, "Response to comment:1:\n\n"+body+"\n\n"+marker, "example", "User")
	f.items[endpoint] = append(f.items[endpoint], legacy)
	ack, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, body)
	if err != nil || ack.ID != "901" || f.posts != 0 {
		t.Fatal("upgrade duplicated or rejected the saved acknowledgement", ack, err, f.posts)
	}
	f.items[endpoint][1].Body = "Response to comment:1:\n\nUnrelated response.\n\n" + marker
	if _, err := p.ReplyFeedback(context.Background(), plan, pr, c, marker, body); err == nil || f.posts != 0 {
		t.Fatal("legacy reconciliation accepted different saved intent", err, f.posts)
	}
}
