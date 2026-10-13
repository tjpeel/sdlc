package workrun

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Keep each page and the complete feedback packet bounded. GitHub's official
// clients own authentication; this adapter only invokes documented gh APIs.
func (p GitHubPublisher) feedbackBoundary(ctx context.Context, plan Plan, pr Publication) error {
	if !githubRepository.MatchString(plan.Repository) || pr.Number < 1 || pr.URL != fmt.Sprintf("https://github.com/%s/pull/%d", plan.Repository, pr.Number) || !objectID.MatchString(pr.HeadSHA) || !objectID.MatchString(pr.BaseSHA) {
		return fmt.Errorf("feedback requires exact PR identity")
	}
	if p.Frozen != nil {
		if err := p.Frozen.Validate(); err != nil {
			return err
		}
		if err := p.verifyIdentity(ctx); err != nil {
			return err
		}
		if err := p.verifyRepositoryIdentity(ctx, plan); err != nil {
			return err
		}
	}
	data, err := p.command(ctx, "gh", "pr", "view", strconv.Itoa(pr.Number), "--repo", plan.Repository, "--json", "number,url,state,headRefName,headRefOid,baseRefName,baseRefOid")
	var remote struct {
		Number  int    `json:"number"`
		URL     string `json:"url"`
		State   string `json:"state"`
		Head    string `json:"headRefName"`
		HeadSHA string `json:"headRefOid"`
		Base    string `json:"baseRefName"`
		BaseSHA string `json:"baseRefOid"`
	}
	if err != nil || json.Unmarshal(data, &remote) != nil || remote.Number != pr.Number || remote.URL != pr.URL || remote.State != "OPEN" || remote.Head != plan.Branch || remote.HeadSHA != pr.HeadSHA || remote.Base != plan.Base || remote.BaseSHA != pr.BaseSHA {
		return fmt.Errorf("PR identity changed during feedback processing")
	}
	return nil
}

type githubFeedbackItem struct {
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	Body      string `json:"body"`
	Updated   string `json:"updated_at"`
	Submitted string `json:"submitted_at"`
	URL       string `json:"html_url"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Path             string `json:"path"`
	Line             int    `json:"line"`
	OriginalLine     int    `json:"original_line"`
	OriginalPosition int    `json:"original_position"`
	OriginalCommit   string `json:"original_commit_id"`
	DiffHunk         string `json:"diff_hunk"`
	ReplyTo          int64  `json:"in_reply_to_id"`
}

func (p GitHubPublisher) feedbackPages(ctx context.Context, endpoint string) ([]githubFeedbackItem, error) {
	items := []githubFeedbackItem{}
	for page := 1; page <= 5; page++ {
		data, err := p.command(ctx, "gh", "api", endpoint+fmt.Sprintf("?per_page=100&page=%d", page))
		if err != nil || len(data) > maxCIAPIBytes {
			return nil, fmt.Errorf("GitHub feedback page could not be read within bounds")
		}
		var batch []githubFeedbackItem
		if json.Unmarshal(data, &batch) != nil || batch == nil || len(batch) > 100 {
			return nil, fmt.Errorf("invalid GitHub feedback page")
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			return items, nil
		}
	}
	return nil, fmt.Errorf("GitHub feedback exceeds paginated response bound")
}

type feedbackThread struct {
	ID                 string
	Root               int64
	Outdated, Resolved bool
}

const feedbackThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){nodes{id isOutdated isResolved comments(first:100){nodes{databaseId} pageInfo{hasNextPage}}} pageInfo{hasNextPage endCursor}}}}}`

func (p GitHubPublisher) feedbackThreads(ctx context.Context, plan Plan, pr Publication) (map[int64]feedbackThread, error) {
	parts := strings.Split(plan.Repository, "/")
	cursor := ""
	seen := map[string]bool{}
	result := map[int64]feedbackThread{}
	for page := 0; page < 5; page++ {
		args := []string{"api", "graphql", "-f", "query=" + feedbackThreadsQuery, "-f", "owner=" + parts[0], "-f", "name=" + parts[1], "-F", fmt.Sprintf("number=%d", pr.Number)}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		data, err := p.command(ctx, "gh", args...)
		var response struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Repository struct {
					PR struct {
						Threads struct {
							Nodes []struct {
								ID       string `json:"id"`
								Outdated bool   `json:"isOutdated"`
								Resolved bool   `json:"isResolved"`
								Comments struct {
									Nodes []struct {
										ID int64 `json:"databaseId"`
									} `json:"nodes"`
									Page struct {
										More bool `json:"hasNextPage"`
									} `json:"pageInfo"`
								} `json:"comments"`
							} `json:"nodes"`
							Page struct {
								More   bool   `json:"hasNextPage"`
								Cursor string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err != nil || len(data) > maxCIAPIBytes || json.Unmarshal(data, &response) != nil || len(response.Errors) > 0 || response.Data.Repository.PR.Threads.Nodes == nil {
			return nil, fmt.Errorf("review thread context could not be read")
		}
		threads := response.Data.Repository.PR.Threads
		if len(threads.Nodes) > 100 {
			return nil, fmt.Errorf("review thread page exceeds bound")
		}
		for _, thread := range threads.Nodes {
			if thread.ID == "" || len(thread.Comments.Nodes) == 0 || len(thread.Comments.Nodes) > 100 || thread.Comments.Page.More {
				return nil, fmt.Errorf("review thread context exceeds comment bound")
			}
			root := thread.Comments.Nodes[0].ID
			for _, item := range thread.Comments.Nodes {
				if item.ID < 1 {
					return nil, fmt.Errorf("review thread has no comment identity")
				}
				if _, exists := result[item.ID]; exists {
					return nil, fmt.Errorf("duplicate review comment identity")
				}
				result[item.ID] = feedbackThread{thread.ID, root, thread.Outdated, thread.Resolved}
			}
		}
		if !threads.Page.More {
			return result, nil
		}
		cursor = threads.Page.Cursor
		if cursor == "" || seen[cursor] {
			return nil, fmt.Errorf("invalid review thread pagination")
		}
		seen[cursor] = true
	}
	return nil, fmt.Errorf("review threads exceed pagination bound")
}
func (p GitHubPublisher) humanFeedback(item githubFeedbackItem) bool {
	if item.ID < 1 || item.User.Login == "" || item.User.Type != "User" || strings.TrimSpace(item.Body) == "" {
		return false
	}
	// Only the frozen controller account's actual trailing acknowledgement is
	// excluded. A reviewer quoting a marker remains human feedback.
	if p.Frozen != nil && item.User.Login == p.Frozen.GitHubLogin {
		const prefix = "<!-- sdlc-pr-feedback:"
		if index := strings.LastIndex(item.Body, "\n\n"+prefix); index >= 0 {
			marker := item.Body[index+2:]
			if len(marker) == 90 && strings.HasSuffix(marker, " -->") {
				if _, err := hex.DecodeString(marker[len(prefix) : len(marker)-4]); err == nil {
					return false
				}
			}
		}
	}
	return true
}
func (p GitHubPublisher) Feedback(ctx context.Context, plan Plan, pr Publication) ([]PRFeedbackComment, error) {
	if err := p.feedbackBoundary(ctx, plan, pr); err != nil {
		return nil, err
	}
	threads, err := p.feedbackThreads(ctx, plan, pr)
	if err != nil {
		return nil, err
	}
	result := []PRFeedbackComment{}
	seen := map[string]bool{}
	for _, kind := range []string{"inline", "review", "comment"} {
		endpoint := fmt.Sprintf("repos/%s/pulls/%d/comments", plan.Repository, pr.Number)
		if kind == "review" {
			endpoint = fmt.Sprintf("repos/%s/pulls/%d/reviews", plan.Repository, pr.Number)
		}
		if kind == "comment" {
			endpoint = fmt.Sprintf("repos/%s/issues/%d/comments", plan.Repository, pr.Number)
		}
		items, err := p.feedbackPages(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !p.humanFeedback(item) {
				continue
			}
			c := PRFeedbackComment{ID: kind + ":" + strconv.FormatInt(item.ID, 10), Kind: kind, Author: item.User.Login, Body: item.Body, UpdatedAt: item.Updated}
			if c.UpdatedAt == "" {
				c.UpdatedAt = item.Submitted
			}
			if kind == "inline" {
				t, ok := threads[item.ID]
				if !ok {
					return nil, fmt.Errorf("inline feedback lacks original review thread")
				}
				c.ThreadID = t.ID
				c.ReplyID = t.Root
				c.Outdated = t.Outdated
				c.Resolved = t.Resolved
				c.Path = item.Path
				c.Line = item.Line
				c.OriginalLine = item.OriginalLine
				c.OriginalPosition = item.OriginalPosition
				c.OriginalCommit = item.OriginalCommit
				c.DiffHunk = item.DiffHunk
			}
			if err := validateFeedbackComment(c); err != nil {
				return nil, err
			}
			if seen[c.ID] {
				return nil, fmt.Errorf("duplicate PR feedback identity")
			}
			seen[c.ID] = true
			result = append(result, c)
			if len(result) > maxPRFeedbackComments {
				return nil, fmt.Errorf("human PR feedback exceeds comment bound")
			}
		}
	}
	// IDs and kinds supply a stable order without trusting mutable API page order.
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if err := p.feedbackBoundary(ctx, plan, pr); err != nil {
		return nil, err
	}
	return result, nil
}

func (p GitHubPublisher) ReplyFeedback(ctx context.Context, plan Plan, pr Publication, c PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	if err := validateFeedbackComment(c); err != nil {
		return PRFeedbackReply{}, err
	}
	if len(marker) != 90 || !strings.HasPrefix(marker, "<!-- sdlc-pr-feedback:") || !strings.HasSuffix(marker, " -->") || strings.TrimSpace(body) == "" || len(body) > 10000 {
		return PRFeedbackReply{}, fmt.Errorf("invalid bounded feedback reply intent")
	}
	if p.Frozen == nil {
		return PRFeedbackReply{}, fmt.Errorf("feedback replies require frozen account identity")
	}
	if err := p.feedbackBoundary(ctx, plan, pr); err != nil {
		return PRFeedbackReply{}, err
	}
	feedback, err := p.Feedback(ctx, plan, pr)
	if err != nil {
		return PRFeedbackReply{}, err
	}
	found := false
	for _, current := range feedback {
		if current.ID == c.ID {
			if current.Body != c.Body || current.UpdatedAt != c.UpdatedAt || current.Path != c.Path || current.OriginalCommit != c.OriginalCommit || current.ThreadID != c.ThreadID {
				return PRFeedbackReply{}, &FeedbackRevisionChangedError{}
			}
			found = true
			break
		}
	}
	if !found {
		return PRFeedbackReply{}, &FeedbackRevisionChangedError{}
	}
	endpoint := fmt.Sprintf("repos/%s/issues/%d/comments", plan.Repository, pr.Number)
	if c.Kind == "inline" {
		endpoint = fmt.Sprintf("repos/%s/pulls/%d/comments", plan.Repository, pr.Number)
	}
	replies, err := p.feedbackPages(ctx, endpoint)
	if err != nil {
		return PRFeedbackReply{}, err
	}
	text := body + "\n\n" + marker
	if c.Kind != "inline" {
		text = "Response to " + c.ID + ":\n\n" + text
	}
	var acknowledged *PRFeedbackReply
	for _, item := range replies {
		if strings.Contains(item.Body, marker) {
			if item.User.Login != p.Frozen.GitHubLogin || item.Body != text || c.Kind == "inline" && item.ReplyTo != c.ReplyID {
				return PRFeedbackReply{}, fmt.Errorf("remote feedback marker differs from saved reply intent")
			}
			if acknowledged != nil {
				return PRFeedbackReply{}, fmt.Errorf("duplicate remote feedback acknowledgement")
			}
			ack := PRFeedbackReply{ID: strconv.FormatInt(item.ID, 10), URL: item.URL}
			acknowledged = &ack
		}
	}
	if acknowledged != nil {
		return *acknowledged, nil
	}
	if err := p.feedbackBoundary(ctx, plan, pr); err != nil {
		return PRFeedbackReply{}, err
	}
	if c.Kind == "inline" {
		endpoint = fmt.Sprintf("repos/%s/pulls/%d/comments/%d/replies", plan.Repository, pr.Number, c.ReplyID)
	}
	data, err := p.command(ctx, "gh", "api", endpoint, "--method", "POST", "-f", "body="+text)
	if err != nil {
		return PRFeedbackReply{}, fmt.Errorf("feedback reply was not confirmed; resume to reconcile its saved marker")
	}
	var item githubFeedbackItem
	if len(data) > maxCIAPIBytes || json.Unmarshal(data, &item) != nil || item.ID < 1 || item.Body != text || item.User.Login != p.Frozen.GitHubLogin {
		return PRFeedbackReply{}, fmt.Errorf("feedback reply acknowledgement is invalid; resume to reconcile")
	}
	return PRFeedbackReply{ID: strconv.FormatInt(item.ID, 10), URL: item.URL}, nil
}
