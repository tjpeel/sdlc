package runstatus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestPRFeedbackReportKeepsUnresolvedHandoffWithoutRawCommentContext(t *testing.T) {
	v := reportView()
	v.Journal.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Entries: []workrun.PRFeedbackEntry{
		{Comment: workrun.PRFeedbackComment{ID: "comment:1", Body: "excluded-raw-comment", DiffHunk: "excluded-source-hunk"}, Assessment: &workrun.PRFeedbackAssessment{Disposition: "unaddressable", Reason: "Confirm the required retention period."}},
	}}
	report, err := reportSnapshot(v)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.PRFeedback == nil || report.PRFeedback.Unresolved != 1 || report.PRFeedback.OutstandingReplies != 1 || !strings.Contains(string(data), "Confirm the required retention period.") || strings.Contains(string(data), "excluded-raw-comment") || strings.Contains(string(data), "excluded-source-hunk") {
		t.Fatal("feedback handoff projection lost its privacy boundary", string(data))
	}
}
