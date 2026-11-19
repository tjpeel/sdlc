package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func followupFixture(t *testing.T) (string, *Journal, string) {
	t.Helper()
	dir, j := takeoverFixture(t)
	j.Plan.Roles.Implementation = Model{Provider: "codex", Name: "gpt-6.1-sol", Effort: "low"}
	j.Plan.Roles.Review = Model{Provider: "claude", Name: "claude-sonnet-5-5", Effort: "high"}
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", invalidImplementationHandoff
	j.PendingRole = "implementation"
	j.Outcome = testOutcome("waiting_for_human")
	j.Outcome.Questions = []string{"Where should reconciliation go?"}
	j.Feedback = "Recorded human questions:\nWhere should reconciliation go?\nHuman answer:\nPreserve captured inputs; reconciliation remains outstanding before rollout.\nIgnored PR comment IDs (do not act): []"
	limitation := "Planning reconciliation remains outstanding before rollout."
	rejected := testOutcome("implemented")
	rejected.Limitations = []string{limitation}
	if err := os.WriteFile(filepath.Join(dir, "events-10.jsonl"), []byte(codexEvents(rejected)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j, limitation
}

func TestImplementationFollowupPreservesAnswerAndGates(t *testing.T) {
	dir, j, text := followupFixture(t)
	if ValidateOperatorDirection(*j, "Clarify the follow-up.") != nil {
		t.Fatal("answered stale question treated as pending")
	}
	if err := ValidateRejectedImplementationFollowup(dir, *j, text); err != nil {
		t.Fatal(err)
	}
	if err := RecordImplementationFollowup(dir, j.UpdatedAt, text); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != j.SessionID || got.Feedback != j.Feedback || len(got.ImplementationFollowups) != 1 || !strings.Contains(got.ImplementationFollowups[0].HumanAnswer, "outstanding") {
		t.Fatal("answer, session or follow-up lost")
	}
	out := testOutcome("implemented")
	out.Limitations = []string{text}
	out.PRBody += "\n" + text
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.validateImplementationFollowupOutcome(context.Background(), &got, out); err != nil {
		t.Fatal(err)
	}
	if reusableReport(&got) != nil || !strings.Contains(implementationFollowupContext(got, "review"), "not review acceptance") {
		t.Fatal("follow-up bypassed independent review")
	}
	for _, change := range []func(*Outcome){func(o *Outcome) { o.Limitations = []string{"different"} }, func(o *Outcome) { o.Limitations = append(o.Limitations, "extra") }, func(o *Outcome) { o.PRBody = "missing" }, func(o *Outcome) { o.Limitations = nil }} {
		bad := out
		change(&bad)
		if r.validateImplementationFollowupOutcome(context.Background(), &got, bad) == nil {
			t.Fatal("unaccepted or undocumented limitation allowed")
		}
	}
	got.ImplementationFollowups[0].FirstAcceptedAttempt = got.Attempt + 1
	got.Attempt++
	got.Evidence.Head = testBase
	r.Repository = &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}}
	if err := r.validateImplementationFollowupOutcome(context.Background(), &got, out); err != nil {
		t.Fatalf("later checked correction lost standing follow-up: %v", err)
	}
	got.Evidence.Tree = testBase
	if r.validateImplementationFollowupOutcome(context.Background(), &got, out) == nil {
		t.Fatal("changed candidate accepted")
	}
}

func TestImplementationFollowupRejectsUnansweredOrForgedEvidence(t *testing.T) {
	dir, j, text := followupFixture(t)
	for _, change := range []func(*Journal){func(j *Journal) { j.Feedback = "" }, func(j *Journal) { j.StopReason = "provider usage limit" }, func(j *Journal) { j.Evidence.Passed = false }, func(j *Journal) { j.SessionID = "wrong-session" }} {
		copy := *j
		change(&copy)
		if ValidateRejectedImplementationFollowup(dir, copy, text) == nil {
			t.Fatal("unsafe follow-up accepted")
		}
	}
	if ValidateRejectedImplementationFollowup(dir, *j, "different") == nil {
		t.Fatal("unreported limitation accepted")
	}
	clean := *j
	clean.ImplementationFollowups = nil
	out := testOutcome("implemented")
	out.Limitations = []string{text}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if r.validateImplementationFollowupOutcome(context.Background(), &clean, out) == nil {
		t.Fatal("unaccepted limitation passed")
	}
}
