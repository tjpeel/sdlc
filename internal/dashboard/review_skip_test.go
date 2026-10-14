package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestDashboardHumanCheckpointShowsMissingIndependentReview(t *testing.T) {
	pub := workrun.Publication{URL: "https://github.com/example/project/pull/3", Number: 3, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}
	j := workrun.Journal{State: "ready", Publication: pub, Outcome: workrun.Outcome{Status: "implemented"}, ReviewSkip: &workrun.ReviewSkip{Publication: pub, Provider: "codex", Model: "selected-review-model", Attempt: 7, Window: "account_usage"}}
	view := runstatus.View{ID: strings.Repeat("a", 24), Available: true, Stopped: true, State: "ready", Journal: &j, PR: pub}
	var out bytes.Buffer
	if err := Detail(&out, view, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Independent review skipped", "selected-review-model", "attempt 7", pub.HeadSHA, "Human review is required"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("dashboard hid missing independent review", want)
		}
	}
}
