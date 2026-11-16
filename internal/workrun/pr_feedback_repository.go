package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func (repository DockerRepository) FeedbackReplayMapping(ctx context.Context, workspace, oldBase, newBase string, commits []string) (map[string]string, error) {
	if !objectID.MatchString(oldBase) || !objectID.MatchString(newBase) || len(commits) > 1000 {
		return nil, fmt.Errorf("invalid feedback replay boundary")
	}
	for _, sha := range commits {
		if !objectID.MatchString(sha) {
			return nil, fmt.Errorf("invalid feedback correction commit")
		}
	}
	data, err := repository.run(ctx, workspace, "feedback_replay_map:"+oldBase+":"+newBase+":"+strings.Join(commits, ","), "")
	if err != nil {
		return nil, err
	}
	mapping := map[string]string{}
	if len(data) > 128*1024 || json.Unmarshal(data, &mapping) != nil || len(mapping) != len(commits) {
		return nil, fmt.Errorf("invalid feedback replay mapping")
	}
	for _, sha := range commits {
		if !objectID.MatchString(mapping[sha]) {
			return nil, fmt.Errorf("missing feedback replay commit")
		}
	}
	return mapping, nil
}
