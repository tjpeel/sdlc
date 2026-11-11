package main

import "testing"

func TestDiscardCIHeldFeedbackScanOptionCombinations(t *testing.T) {
	if _, err := parseRunOptions(runArgs("--resume", "saved-run", "--discard-ci-held-feedback-scan", "--dry-run")); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--rebase"}, {"--pr-feedback"}, {"--discard-ci-held-review"}, {"--abandon-runtime-recheck"}, {"--review-ci-hold"}, {"--answer-file", "answer.txt"}} {
		if _, err := parseRunOptions(append(runArgs("--resume", "saved-run", "--discard-ci-held-feedback-scan"), extra...)); err == nil {
			t.Fatalf("admitted %v", extra)
		}
	}
	if _, err := parseRunOptions(runArgs("--discard-ci-held-feedback-scan")); err == nil {
		t.Fatal("admitted without saved run")
	}
}
