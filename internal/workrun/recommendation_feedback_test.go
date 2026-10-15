package workrun

import (
	"context"
	"strings"
	"testing"
)

func withRecommendation(j *Journal) {
	j.ReviewReports = []ReviewReport{{Publication: j.Publication, Attempt: j.Attempt, Coverage: coverage("full"), Recommendations: []Finding{{Priority: "P3", Path: "app.go", Line: 12, Scenario: "Unused metadata adds storage cost.", Recommendation: "Defer the metadata index until its producer exists."}}}}
}

func TestRecommendationAcceptanceUsesLatestCurrentReviewOnly(t *testing.T) {
	_, j, _, _, _ := feedbackReadyRun(t)
	withRecommendation(j)
	old := j.ReviewReports[0]
	old.Recommendations = []Finding{{Priority: "P3", Path: "old.go", Line: 1, Scenario: "Old concern.", Recommendation: "Unselected old suggestion."}}
	j.ReviewReports = append([]ReviewReport{old}, j.ReviewReports...)
	text, err := RecommendationFeedback(*j)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{j.Publication.HeadSHA, j.Publication.BaseSHA, "app.go:12", "Unused metadata adds storage cost.", "Defer the metadata index until its producer exists."} {
		if !strings.Contains(text, selected) {
			t.Fatal("selected evidence missing", selected)
		}
	}
	if strings.Contains(text, "Unselected old suggestion") {
		t.Fatal("historical recommendation accepted")
	}
	j.ReviewReports[len(j.ReviewReports)-1].Recommendations = nil
	if _, err := RecommendationFeedback(*j); err == nil {
		t.Fatal("fell back to historical suggestions")
	}
}

func TestRecommendationAcceptanceRejectsStaleOrIncompleteEvidence(t *testing.T) {
	_, j, _, _, _ := feedbackReadyRun(t)
	for _, kind := range []string{"no report", "wrong head", "wrong base", "wrong PR", "partial", "unknown coverage", "coverage mismatch", "blocked", "pending question", "invalid recommendation", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			copy := *j
			withRecommendation(&copy)
			report := &copy.ReviewReports[0]
			switch kind {
			case "no report":
				copy.ReviewReports = nil
			case "wrong head":
				report.Publication.HeadSHA = strings.Repeat("e", 40)
			case "wrong base":
				report.Publication.BaseSHA = strings.Repeat("e", 40)
			case "wrong PR":
				report.Publication.Number++
			case "partial":
				report.Coverage = coverage("partial", "Unread test assertions")
			case "unknown coverage":
				report.Coverage = nil
			case "coverage mismatch":
				report.Coverage.HeadSHA = strings.Repeat("e", 40)
			case "blocked":
				copy.State = "blocked"
			case "pending question":
				copy.State = "waiting_for_human"
			case "invalid recommendation":
				report.Recommendations[0].Path = "../outside.go"
			case "oversize":
				report.Recommendations[0].Scenario = strings.Repeat("x", 64*1024)
			}
			if _, err := RecommendationFeedback(copy); err == nil {
				t.Fatal("unsafe acceptance allowed", kind)
			}
		})
	}
}

func TestAcceptedRecommendationsFollowExistingRepairGates(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	withRecommendation(j)
	text, err := RecommendationFeedback(*j)
	if err != nil {
		t.Fatal(err)
	}
	original := j.SessionID
	p := &localFeedbackPublisher{feedbackPublisherFake: pub}
	provider := &localAssessmentProvider{j: j}
	r.Publisher, r.Provider, r.ProcessPRFeedback, r.LocalPRFeedback = p, provider, true, text
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls != 2 || len(provider.sessions) != 3 || provider.sessions[0].ResumeID != original || provider.sessions[2].Role != "review" || provider.sessions[2].ResumeID != "" {
		t.Fatal("acceptance bypassed the repair/check/review cycle")
	}
	if pub.reads != 0 || pub.replies != 0 {
		t.Fatal("local acceptance posted GitHub feedback")
	}
	if len(j.PRFeedback.Entries) != 1 || j.PRFeedback.Entries[0].Comment.Body != text {
		t.Fatal("acceptance evidence not retained")
	}
}
