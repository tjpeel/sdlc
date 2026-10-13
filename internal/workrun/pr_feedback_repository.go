package workrun

import (
	"context"
	"encoding/json"
	"fmt"
)

func (repository DockerRepository) FeedbackCommits(ctx context.Context, workspace, start, head string) ([]string, error) {
	if !objectID.MatchString(start) || !objectID.MatchString(head) {
		return nil, fmt.Errorf("feedback history requires exact candidate revisions")
	}
	data, err := repository.run(ctx, workspace, "feedback_commits:"+start+":"+head, "")
	if err != nil {
		return nil, err
	}
	var commits []string
	if len(data) > 128*1024 || json.Unmarshal(data, &commits) != nil || commits == nil || len(commits) > 1000 {
		return nil, fmt.Errorf("invalid bounded feedback correction history")
	}
	for _, sha := range commits {
		if !objectID.MatchString(sha) {
			return nil, fmt.Errorf("invalid feedback correction commit")
		}
	}
	return commits, nil
}
