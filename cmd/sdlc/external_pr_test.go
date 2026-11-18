package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestExternalPRResumePreviewAndOptionValidation(t *testing.T) {
	j, _ := readyPRFeedbackFixture(t)
	url := "https://github.com/example/project/pull/2"
	var output bytes.Buffer
	if err := resumeCommand(context.Background(), []string{"--run", j.ID[:6], "--merge-pr", url, "--dry-run"}, &output); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Action   string `json:"action"`
		Incoming string `json:"incoming_pr"`
		Offline  bool   `json:"offline"`
	}
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil || plan.Action != "merge_external_pr" || plan.Incoming != url || !plan.Offline {
		t.Fatalf("unexpected plan %q: %v", output.String(), err)
	}
	for _, args := range [][]string{
		{"--run", j.ID, "--merge-pr", ""},
		{"--run", j.ID, "--merge-pr", url, "--rebase"},
		{"--run", j.ID, "--merge-pr", "https://github.com/example/other/pull/2", "--dry-run"},
		{"--run", j.ID, "--merge-pr", "https://github.com/example/project/pull/1", "--dry-run"},
	} {
		if err := resumeCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
