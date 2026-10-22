package workrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRLinkReviewUsesMinimalPromptAndOnDemandEvidence(t *testing.T) {
	_, j := testRun(t)
	j.Plan.Roles.ReviewContext = "pr-link"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	j.Plan.PRBody = "whole-scope-marker"
	j.Feedback = "implementation-transcript-marker"
	j.Evidence.Output = []CommandOutput{{CombinedOutput: "private-check-log-marker"}}
	prompt := (Runner{}).prompt(*j, "review")
	for _, required := range []string{j.Publication.URL, testBase, testHead, prLinkContextPath, "review_incomplete", "tjpeel-pr-review", "review_coverage.inspection"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing %q", required)
		}
	}
	for _, forbidden := range []string{j.Plan.PRBody, j.Feedback, "Selected launch context:"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt embeds %q", forbidden)
		}
	}
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePRLinkReviewContext(workspace, *j); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, prLinkContextPath))
	if err != nil {
		t.Fatal(err)
	}
	var packet map[string]json.RawMessage
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packet["pr_metadata"]), j.Plan.PRBody) || (strings.Contains(string(raw), j.Feedback) || strings.Contains(string(raw), "private-check-log-marker")) {
		t.Fatal("lost metadata or leaked implementation feedback")
	}
	j.Plan.Roles.ReviewContext = ""
	if !strings.Contains((Runner{}).prompt(*j, "review"), "Selected launch context:") {
		t.Fatal("default mode changed")
	}
}

func TestPRLinkReviewKeepsExistingContinuation(t *testing.T) {
	_, j := testRun(t)
	j.Plan.Roles.ReviewContext = "pr-link"
	j.ReviewContinuation = &ReviewContinuation{Gaps: []string{"Trace producer"}}
	prompt := (Runner{}).prompt(*j, "review")
	if !strings.Contains(prompt, "Trace producer") || strings.Contains(prompt, "Experimental pr-link review") {
		t.Fatal("continuation restarted review")
	}
}

func TestReviewContextSavedRolesAndValidation(t *testing.T) {
	roles := DefaultModels().Codex
	roles.ReviewContext = "pr-link"
	raw, err := json.Marshal(roles)
	if err != nil {
		t.Fatal(err)
	}
	var saved Roles
	if err := json.Unmarshal(raw, &saved); err != nil || saved.ReviewContext != "pr-link" {
		t.Fatalf("lost saved mode: %v", err)
	}
	if err := ValidateSpeeds(saved); err != nil {
		t.Fatal(err)
	}
	saved.ReviewContext = "unknown"
	if err := ValidateSpeeds(saved); err == nil {
		t.Fatal("invalid saved mode accepted")
	}
}
