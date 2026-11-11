package main

import "testing"

func TestAbandonRuntimeRecheckOptionCombinations(t *testing.T) {
	if _, err := parseRunOptions(runArgs("--resume", "saved-run", "--abandon-runtime-recheck", "--dry-run")); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{
		{"--rebase"}, {"--pr-feedback"}, {"--review-ci-hold"}, {"--discard-ci-held-review"}, {"--start-at", "checks"}, {"--ci-feedback-file", "ci.txt"}, {"--initialize-ci-repair-budget"}, {"--answer-file", "answer.txt"},
	} {
		args := append(runArgs("--resume", "saved-run", "--abandon-runtime-recheck"), extra...)
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("admitted combination %v", extra)
		}
	}
	if _, err := parseRunOptions(runArgs("--abandon-runtime-recheck")); err == nil {
		t.Fatal("admitted abandonment without a saved run")
	}
}
