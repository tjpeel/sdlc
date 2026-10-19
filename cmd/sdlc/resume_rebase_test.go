package main

import "testing"

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
