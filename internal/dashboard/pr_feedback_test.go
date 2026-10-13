package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestHumanFeedbackHandoffShowsUnresolvedReasonAndResumeAction(t *testing.T) {
	j := workrun.Journal{PRFeedback: &workrun.PRFeedbackHistory{Version: 1, Cycle: 2, Entries: []workrun.PRFeedbackEntry{
		{Comment: workrun.PRFeedbackComment{ID: "comment:1"}, Assessment: &workrun.PRFeedbackAssessment{Disposition: "unaddressable", Reason: "Choose the supported retention period."}, Reply: &workrun.PRFeedbackReply{ID: "reply:1"}},
		{Comment: workrun.PRFeedbackComment{ID: "comment:2"}},
	}}}
	v := runstatus.View{ID: "abcdef0123456789abcdef01", Available: true, Stopped: true, State: "ready", Journal: &j, PR: workrun.Publication{URL: "https://github.com/example/project/pull/1"}}
	var out bytes.Buffer
	if err := Detail(&out, v, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"pending: 1 | unresolved: 1 | outstanding replies: 0", "Choose the supported retention period."} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("human feedback handoff omitted %q: %s", expected, out.String())
		}
	}
	if action := NextAction(v); !strings.Contains(action, "sdlc resume --run "+v.ID+" --pr-feedback") {
		t.Fatal("feedback resume action missing", action)
	}
}
