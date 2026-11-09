package main

import (
	"github.com/tjpeel/sdlc/internal/workrun"
	"testing"
)

func TestParseResumeRebase(t *testing.T) {
	base := []string{"--reference", "TASK-1", "--ticket", "01-work.md", "--resume", "abc123", "--rebase"}
	o, err := parseRunOptions(base)
	if err != nil || !o.rebase {
		t.Fatalf("%+v %v", o, err)
	}
	for _, extra := range [][]string{{"--pr-feedback"}, {"--auto-accept-recommendations"}, {"--auto-accept-recommendations=false"}, {"--start-at", "checks"}, {"--answer-file", "answer.txt"}, {"--override-run-limits"}, {"--ci-feedback-file", "ci.txt"}} {
		args := append(append([]string{}, base...), extra...)
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("accepted incompatible flags: %v", args)
		}
	}
	if _, err := parseRunOptions([]string{"--reference", "TASK-1", "--ticket", "01-work.md", "--rebase"}); err == nil {
		t.Fatal("accepted rebase without resume")
	}
}

func TestRebasePreviewShowsUnpublishedCandidate(t *testing.T) {
	j := &workrun.Journal{Evidence: workrun.CheckEvidence{Head: "checked"}}
	plan := rebaseDryRunPlan(j)
	if plan["recorded_head"] != "checked" || plan["target"] != "current live base branch (verified during execution)" {
		t.Fatal(plan)
	}
	j.Publication = workrun.Publication{Number: 1, HeadSHA: "published"}
	plan = rebaseDryRunPlan(j)
	if plan["recorded_head"] != "published" || plan["target"] != "current live PR target (verified during execution)" {
		t.Fatal(plan)
	}
}
