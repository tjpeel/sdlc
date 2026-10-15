package workrun

import (
	"context"
	"fmt"
)

func (r Runner) autoAcceptRecommendations(ctx context.Context, dir string, j *Journal) (bool, error) {
	if !j.Plan.Roles.AutoAcceptRecommendations || len(j.ReviewReports) == 0 || len(j.ReviewReports[len(j.ReviewReports)-1].Recommendations) == 0 {
		return false, nil
	}
	text, err := RecommendationFeedback(*j)
	if err != nil {
		return false, err
	}
	for _, head := range j.AutoRecommendationHeads {
		if head == j.Publication.HeadSHA {
			return false, fmt.Errorf("accepted recommendations returned without source progress; inspect the retained review and acceptance before continuing")
		}
	}
	before := j.State
	j.AutoRecommendationHeads = append(j.AutoRecommendationHeads, j.Publication.HeadSHA)
	r.LocalPRFeedback = text
	if err := r.beginPRFeedback(ctx, dir, j); err != nil {
		if j.State == before {
			j.AutoRecommendationHeads = j.AutoRecommendationHeads[:len(j.AutoRecommendationHeads)-1]
		}
		return false, err
	}
	fmt.Fprintln(r.Output, "Automatically accepted current independent-review recommendations; continuing the normal repair and verification cycle.")
	return true, nil
}
