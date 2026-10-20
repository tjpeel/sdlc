package workrun

import (
	"context"
	"fmt"
)

func (r Runner) autoAcceptRecommendations(ctx context.Context, dir string, j *Journal) (bool, error) {
	// A quota-skipped current review is a human checkpoint, not authority to
	// accept recommendations retained from an older publication.
	if ReviewSkipNotice(*j) != "" {
		if err := validateReviewSkip(j); err != nil {
			return false, err
		}
		return false, nil
	}
	if reviewReuseCurrent(j) || !j.Plan.Roles.AutoAcceptRecommendations || len(j.ReviewReports) == 0 || len(j.ReviewReports[len(j.ReviewReports)-1].Recommendations) == 0 {
		return false, nil
	}
	text, err := RecommendationFeedback(*j)
	if err != nil {
		return false, err
	}
	if settledNoChangeRecommendation(j, text) {
		return false, nil
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

// A justified no-change assessment settles the accepted recommendation. It is
// not a failed repair and must not automatically reopen its own feedback cycle.
func settledNoChangeRecommendation(j *Journal, text string) bool {
	h := j.PRFeedback
	if h == nil || h.Active || h.CompletedHead != j.Publication.HeadSHA {
		return false
	}
	id := localFeedbackID(text)
	for i := len(h.Entries) - 1; i >= 0; i-- {
		e := h.Entries[i]
		if e.Comment.ID != id {
			continue
		}
		return e.Comment.Kind == "local" && e.Comment.Body == text && !e.Comment.Deleted && !e.Provisional && e.Assessment != nil && e.Assessment.Disposition == "no_change" && len(e.Assessment.Commits) == 0 && e.Reply != nil
	}
	return false
}
