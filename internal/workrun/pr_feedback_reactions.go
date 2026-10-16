package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The viewer is checked against the frozen account at both collection boundaries.
// reactionGroups bounds the common read; only the viewer's marked comments need
// paginated reactions to establish the actor and compare reaction/edit times.
const feedbackReactionGroupsQuery = `query SDLCFeedbackReactionGroups($ids:[ID!]!){nodes(ids:$ids){id ... on Reactable{reactionGroups{content viewerHasReacted}}}}`
const feedbackIgnoreReactionQuery = `query SDLCFeedbackIgnoreReaction($id:ID!,$cursor:String){node(id:$id){id __typename ... on IssueComment{databaseId:fullDatabaseId body updatedAt:lastEditedAt createdAt} ... on PullRequestReview{databaseId:fullDatabaseId body updatedAt:lastEditedAt createdAt} ... on PullRequestReviewComment{databaseId:fullDatabaseId body updatedAt:lastEditedAt createdAt} ... on Reactable{reactions(content:THUMBS_DOWN,first:100,after:$cursor){nodes{id content createdAt user{login databaseId}} pageInfo{hasNextPage endCursor}}}}}`

func (p GitHubPublisher) markFeedbackReactions(ctx context.Context, comments []PRFeedbackComment, nodeIDs map[string]string) error {
	if p.Frozen == nil {
		return nil
	} // No trusted actor selection: never suppress feedback.
	args := []string{"api", "graphql", "-f", "query=" + feedbackReactionGroupsQuery}
	wanted := map[string]int{}
	for i, c := range comments {
		if c.Resolved || c.Deleted {
			continue
		}
		node := nodeIDs[c.ID]
		if node == "" || len(node) > 200 {
			return fmt.Errorf("feedback lacks reaction node identity")
		}
		if _, exists := wanted[node]; exists {
			return fmt.Errorf("duplicate feedback reaction node")
		}
		wanted[node] = i
		args = append(args, "-f", "ids[]="+node)
	}
	if len(wanted) == 0 {
		return nil
	}
	var response struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Nodes []struct {
				ID     string `json:"id"`
				Groups []struct {
					Content string `json:"content"`
					Viewer  bool   `json:"viewerHasReacted"`
				} `json:"reactionGroups"`
			} `json:"nodes"`
		} `json:"data"`
	}
	data, err := p.command(ctx, "gh", args...)
	if err != nil || len(data) > maxCIAPIBytes || json.Unmarshal(data, &response) != nil || len(response.Errors) != 0 || len(response.Data.Nodes) != len(wanted) {
		return fmt.Errorf("feedback reaction groups could not be verified")
	}
	seen := map[string]bool{}
	for _, node := range response.Data.Nodes {
		index, ok := wanted[node.ID]
		if !ok || seen[node.ID] || node.Groups == nil || len(node.Groups) > 8 {
			return fmt.Errorf("invalid feedback reaction group identity")
		}
		seen[node.ID] = true
		for _, content := range []string{"THUMBS_DOWN", "ROCKET"} {
			marked := false
			for _, group := range node.Groups {
				if group.Content == content && group.Viewer {
					marked = true
				}
			}
			if !marked {
				continue
			}
			evidence, err := p.feedbackReaction(ctx, comments[index], node.ID, content)
			if err != nil {
				return err
			}
			if content == "THUMBS_DOWN" {
				comments[index].Ignored = evidence
			} else {
				comments[index].Addressed = evidence
			}
		}

	}
	return nil
}

func (p GitHubPublisher) feedbackIgnoreReaction(ctx context.Context, comment PRFeedbackComment, nodeID string) (*PRFeedbackIgnore, error) {
	return p.feedbackReaction(ctx, comment, nodeID, "THUMBS_DOWN")
}

func (p GitHubPublisher) feedbackReaction(ctx context.Context, comment PRFeedbackComment, nodeID, content string) (*PRFeedbackIgnore, error) {
	cursor := ""
	seen := map[string]bool{}
	var ignored *PRFeedbackIgnore
	firstUpdated := ""
	for page := 0; page < 5; page++ {
		args := []string{"api", "graphql", "-f", "query=" + strings.Replace(feedbackIgnoreReactionQuery, "content:THUMBS_DOWN", "content:"+content, 1), "-f", "id=" + nodeID}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		var response struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Node struct {
					ID         string      `json:"id"`
					Type       string      `json:"__typename"`
					DatabaseID json.Number `json:"databaseId"`
					Body       string      `json:"body"`
					Updated    string      `json:"updatedAt"`
					Created    string      `json:"createdAt"`
					Reactions  struct {
						Nodes []struct {
							ID      string `json:"id"`
							Content string `json:"content"`
							Created string `json:"createdAt"`
							User    struct {
								Login string `json:"login"`
								ID    int64  `json:"databaseId"`
							} `json:"user"`
						} `json:"nodes"`
						Page struct {
							More   bool   `json:"hasNextPage"`
							Cursor string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reactions"`
				} `json:"node"`
			} `json:"data"`
		}
		data, err := p.command(ctx, "gh", args...)
		if err != nil || len(data) > maxCIAPIBytes || json.Unmarshal(data, &response) != nil || len(response.Errors) != 0 {
			return nil, fmt.Errorf("feedback operator reactions could not be read within bounds")
		}
		n := response.Data.Node
		subjectID, subjectErr := n.DatabaseID.Int64()
		expectedType := map[string]string{"inline": "PullRequestReviewComment", "review": "PullRequestReview", "comment": "IssueComment"}[comment.Kind]
		if n.ID != nodeID || n.Type != expectedType || subjectErr != nil || comment.ID != comment.Kind+":"+strconv.FormatInt(subjectID, 10) || n.Body != comment.Body || n.Reactions.Nodes == nil || len(n.Reactions.Nodes) > 100 {
			return nil, fmt.Errorf("feedback changed during operator reaction collection")
		}
		editTime := n.Updated
		if editTime == "" {
			editTime = n.Created
		}
		if firstUpdated != "" && firstUpdated != editTime {
			return nil, fmt.Errorf("feedback edit time changed during reaction collection")
		}
		firstUpdated = editTime
		updated, err := time.Parse(time.RFC3339, editTime)
		if err != nil {
			return nil, fmt.Errorf("feedback edit time could not be verified")
		}
		for _, reaction := range n.Reactions.Nodes {
			if reaction.Content != content || reaction.ID == "" || len(reaction.ID) > 200 {
				return nil, fmt.Errorf("invalid feedback operator reaction")
			}
			if reaction.User.ID != p.Frozen.GitHubID || !strings.EqualFold(reaction.User.Login, p.Frozen.GitHubLogin) {
				continue
			}
			created, err := time.Parse(time.RFC3339, reaction.Created)
			if err != nil {
				return nil, fmt.Errorf("feedback reaction time could not be verified")
			}
			if ignored != nil {
				return nil, fmt.Errorf("duplicate account operator reaction")
			}
			if !created.Before(updated) {
				ignored = &PRFeedbackIgnore{By: p.Frozen.GitHubLogin, UserID: p.Frozen.GitHubID, ReactionID: reaction.ID, CreatedAt: reaction.Created}
			}
		}
		if !n.Reactions.Page.More {
			return ignored, nil
		}
		cursor = n.Reactions.Page.Cursor
		if cursor == "" || len(cursor) > 1024 || seen[cursor] {
			return nil, fmt.Errorf("invalid feedback reaction pagination")
		}
		seen[cursor] = true
	}
	return nil, fmt.Errorf("feedback reactions exceed pagination bound")
}
