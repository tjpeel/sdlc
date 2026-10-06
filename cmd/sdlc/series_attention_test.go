package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestFeatureSummaryShowsRecordedQuestionsAndActionForEveryStoppedTicket(t *testing.T) {
	j, _ := attentionFixture(t)
	plan := workseries.Plan{Tickets: []workseries.Ticket{{File: "01-question.md"}, {File: "02-preparation.md"}}}
	state := workseries.State{Results: map[string]workseries.Result{
		"01-question.md":    {RunID: j.ID, Directory: filepath.Dir(j.Workspace), State: "waiting_for_human", StopReason: "Answer the recorded questions"},
		"02-preparation.md": {RunID: "0123456789abcdef01234567", State: "blocked", StopReason: "Missing check input"},
	}}
	var output bytes.Buffer
	printSeriesResults(&output, plan, state)
	for _, expected := range []string{j.Outcome.Questions[0], j.Outcome.Questions[1], "sdlc answer --run " + j.ID, "Missing check input", "Correct the preparation failure"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("summary omitted %q: %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "sdlc resume --run 012345") {
		t.Fatal("preparation failure offered unsupported checkpoint resume")
	}
}
