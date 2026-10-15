package main

import (
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func printPRCheckpointResult(output io.Writer, j workrun.Journal) {
	if j.State != "awaiting_feedback" || len(j.PRCheckpoints) == 0 {
		return
	}
	c := j.PRCheckpoints[len(j.PRCheckpoints)-1]
	fmt.Fprintf(output, "Awaiting feedback: %s\nCaptured PR head: %s\n", j.Publication.URL, c.Head)
	if c.HumanReviewedAt != nil {
		fmt.Fprintln(output, "Human review declaration recorded for this head.")
	}
	if c.StartAt == "checks" && c.Completed {
		fmt.Fprintln(output, "Configured checks passed; no publication or independent review was performed by this action.")
	}
	if c.ReviewOutcome != nil {
		out := c.ReviewOutcome
		fmt.Fprintf(output, "Independent review: %d findings; %d limitations.\n%s\n", len(out.Findings), len(out.Limitations), dashboard.SafeText(out.Summary))
		for _, finding := range out.Findings {
			fmt.Fprintf(output, "  %s %s:%d: %s\n", dashboard.SafeText(finding.Priority), dashboard.SafeText(finding.Path), finding.Line, dashboard.SafeText(finding.Scenario))
		}
	}
	fmt.Fprintf(output, "Next: sdlc feedback --run %s\nGitHub comments: sdlc resume --run %s --pr-feedback\n", j.ID, j.ID)
}
