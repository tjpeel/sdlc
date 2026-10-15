package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestResumeRecommendationAcceptancePreviewIsOfflineAndNonMutating(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	j.Attempt = 1
	j.ReviewReports = []workrun.ReviewReport{{Publication: j.Publication, Attempt: 1, Coverage: &workrun.ReviewCoverage{BaseSHA: j.Publication.BaseSHA, HeadSHA: j.Publication.HeadSHA, Scope: "full", Gaps: []string{}}, Recommendations: []workrun.Finding{{Priority: "P3", Path: "app.go", Line: 12, Scenario: "Unused data has a write cost.", Recommendation: "Remove the unused index."}}}}
	dir := filepath.Dir(j.Workspace)
	if err := workrun.Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, dir)
	var out bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID[:6], "--accept-recommendations", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"\n1. P3 app.go:12\n", "Remove the unused index.", j.Publication.HeadSHA, "Preview only"} {
		if !strings.Contains(out.String(), value) {
			t.Fatal("preview omitted accepted evidence", value)
		}
	}
	if !reflect.DeepEqual(before, dashboardTree(t, dir)) {
		t.Fatal("preview changed run files")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview dispatched runtime")
	}
}

func TestResumeRecommendationAcceptanceRejectsConflictingActions(t *testing.T) {
	for _, extra := range [][]string{{"--pr-feedback"}, {"--start-at", "review"}, {"--override-run-limits"}, {"--extend-session-turn-budget", "2"}, {"--review-repair-limit", "3"}, {"--ci-feedback-file", "context.txt"}} {
		args := append([]string{"--run", "example", "--accept-recommendations", "--dry-run"}, extra...)
		if err := resumeCommand(context.Background(), args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatal("conflicting acceptance allowed", args, err)
		}
	}
}

func TestResumeRecommendationAcceptanceValidatesExplicitTimeout(t *testing.T) {
	for _, value := range []string{"0", "1s", "25h"} {
		err := resumeCommand(context.Background(), []string{"--run", "example", "--accept-recommendations", "--timeout", value}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "timeout must be between") {
			t.Fatal("invalid timeout accepted", value, err)
		}
	}
}
