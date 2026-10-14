package runstatus

import (
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestSkippedReviewRemainsVisibleInPortableStatusAndDiagnosis(t *testing.T) {
	pub := workrun.Publication{URL: "https://github.com/example/project/pull/3", Number: 3, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}
	j := workrun.Journal{ID: strings.Repeat("a", 24), State: "ready", Publication: pub, Outcome: workrun.Outcome{Status: "implemented"}, ReviewSkip: &workrun.ReviewSkip{Publication: pub, Provider: "claude", Model: "selected-review-model", Attempt: 7, Window: "five_hour"}}
	view := project(j, "/example/checkpoint")
	if !strings.Contains(view.StopReason, "Independent review skipped") || !strings.Contains(view.StopReason, pub.HeadSHA) {
		t.Fatal("status projection hid skipped review", view.StopReason)
	}
	report, err := reportSnapshot(view)
	if err != nil || report.ReviewSkip == nil || report.ReviewSkip.Attempt != 7 || report.ReviewSkip.Publication != pub {
		t.Fatal("portable report lost skip provenance", err)
	}
	diagnostic, err := DiagnosticSnapshot(view)
	if err != nil || diagnostic.Review != "skipped_usage_exhausted" || !strings.Contains(diagnostic.NextAction.Instruction, "no independent approval") {
		t.Fatal("diagnosis implied completed review", err, diagnostic.Review)
	}
	if j.Outcome.Status != "implemented" {
		t.Fatal("reporting synthesized review approval")
	}
}
