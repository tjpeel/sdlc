package main

import (
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
	"strings"
	"testing"
)

func TestReconciliationRequiresExactChildPublicationAndBranch(t *testing.T) {
	old, updated, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	j := workrun.Journal{Plan: workrun.Plan{Base: "parent", Branch: "child"}, Publication: workrun.Publication{Number: 414, BaseSHA: old, HeadSHA: head}}
	target := workseries.Target{Base: "parent", SHA: updated}
	remote := workrun.RemotePR{State: "OPEN", Base: "parent", BaseSHA: old, HeadSHA: head}
	branch := workrun.BranchSnapshot{Branch: "child", SHA: head}
	if err := validateReconciliationPRBoundary(j, target, remote, branch); err != nil {
		t.Fatal(err)
	}
	remote.BaseSHA = updated
	if err := validateReconciliationPRBoundary(j, target, remote, branch); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*workrun.RemotePR, *workrun.BranchSnapshot)
	}{
		{"closed", func(r *workrun.RemotePR, _ *workrun.BranchSnapshot) { r.State = "CLOSED" }},
		{"head", func(r *workrun.RemotePR, _ *workrun.BranchSnapshot) { r.HeadSHA = old }},
		{"base name", func(r *workrun.RemotePR, _ *workrun.BranchSnapshot) { r.Base = "other" }},
		{"base sha", func(r *workrun.RemotePR, _ *workrun.BranchSnapshot) { r.BaseSHA = head }},
		{"branch name", func(_ *workrun.RemotePR, b *workrun.BranchSnapshot) { b.Branch = "other" }},
		{"branch sha", func(_ *workrun.RemotePR, b *workrun.BranchSnapshot) { b.SHA = old }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, b := remote, branch
			tc.change(&r, &b)
			if err := validateReconciliationPRBoundary(j, target, r, b); err == nil {
				t.Fatal("changed child boundary accepted")
			}
		})
	}
}

func TestRestackedCIHoldRequiresFreshImplementationChecks(t *testing.T) {
	j := workrun.Journal{SessionID: "existing-session", RestackNeedsChecks: true}
	old := workrun.Outcome{Status: "reviewed"}
	restoreRestackedHold(&j, "blocked", "ci", "old CI unavailable", old, true)
	if j.State != "blocked" || j.ResumeState != "implementing" || j.SessionID != "existing-session" || !j.RestackNeedsChecks || j.Evidence.Passed || j.CI.Status != "" || j.Outcome.Status != "reviewed" || !strings.Contains(j.StopReason, "fresh checks") {
		t.Fatal("old CI hold or verification survived the restack")
	}
	j = workrun.Journal{SessionID: "existing-session", RestackNeedsChecks: true}
	restoreRestackedHold(&j, "waiting_human", "implementing", "decision needed", old, true)
	if j.State != "waiting_human" || j.StopReason != "decision needed" || j.Outcome.Status != "reviewed" || j.SessionID != "existing-session" {
		t.Fatal("genuine held work was not preserved")
	}
}
