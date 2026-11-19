package workrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func passedCIRecoveryFixture(t *testing.T) (string, *Journal, Runner, *fakeProvider, *fakePublisher) {
	t.Helper()
	dir, j := overrideFixture(t)
	if err := GrantReviewAcceptance(dir, j.UpdatedAt, "Prior human acceptance."); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	j = &loaded
	j.State, j.ResumeState, j.PendingRole = "blocked", "implementing", "implementation"
	j.Outcome = testOutcome("blocked")
	j.Outcome.Summary = "CI failed because the package mirror timed out. Re-run CI; no source change is needed."
	j.Outcome.Limitations = []string{"The failed workflow needs a rerun when the mirror is available."}
	j.StopReason = j.Outcome.Summary
	j.CI = CIResult{Status: "failed"}
	j.CIRepairBudget = &CIRepairBudget{Used: 1, StartRound: j.Rounds - 1, LastGrantedHead: j.Publication.HeadSHA}
	j.RestackNeedsChecks = true
	j.ReviewHumanInput = &ReviewHumanInput{Questions: []string{"Previous question?"}, Answer: "Previous human decision."}
	c := generalFeedback("comment:1", "An external follow-up is needed.")
	a := assessedOutcome(c, "unaddressable").PRFeedbackAssessments[0]
	e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason, Reply: &PRFeedbackReply{ID: "already-replied"}}
	e.Marker = feedbackEntryMarker(j.ID, e)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{e}}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	pub := &fakePublisher{statuses: []string{"passed"}}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	return dir, j, r, p, pub
}

func TestPassedCIRecoveryRunsFreshReviewWithoutImplementation(t *testing.T) {
	dir, j, r, p, pub := passedCIRecoveryFixture(t)
	oldOutcome, oldEntries, oldAcceptance := j.Outcome, append([]PRFeedbackEntry(nil), j.PRFeedback.Entries...), append([]ReviewAcceptance(nil), j.ReviewAcceptances...)
	if !canRecoverPassedCI(*j) {
		t.Fatal("settled feedback and CI-only hold were not recognised")
	}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0].Role != "review" || p.calls[0].ResumeID != "" || pub.ci < 2 || !strings.Contains(p.calls[0].Prompt, "package mirror timed out") || !strings.Contains(p.calls[0].Prompt, "unresolved PR feedback") || !strings.Contains(p.calls[0].Prompt, "Prior human acceptance.") || !strings.Contains(p.calls[0].Prompt, "Previous human decision.") {
		t.Fatal("did not recheck CI and run a fresh independent review")
	}
	if j.State != "ready" || len(j.CIPassedRecoveries) != 1 || j.CIPassedRecoveries[0].PriorCI.Status != "failed" || j.CIPassedRecoveries[0].BlockSummary != oldOutcome.Summary || !reflect.DeepEqual(j.CIPassedRecoveries[0].BlockLimitations, oldOutcome.Limitations) || !reflect.DeepEqual(j.PRFeedback.Entries, oldEntries) || PRFeedbackStatus(*j).Unresolved != 1 || !reflect.DeepEqual(j.ReviewAcceptances, oldAcceptance) {
		t.Fatal("recovery lost the block, unresolved feedback or human acceptance")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.CIPassedRecoveries) != 1 || PRFeedbackStatus(saved).Unresolved != 1 {
		t.Fatal("recovery was not retained")
	}
}

func TestPassedCIRecoveryCarriesPriorAcceptanceFromEarlierHead(t *testing.T) {
	dir, j, r, p, _ := passedCIRecoveryFixture(t)
	j.Publication.HeadSHA = testSigned
	j.CIRepairBudget.LastGrantedHead = testSigned
	r.Repository = &fakeRepository{revisions: []Revision{{Head: testHead, Tree: testTree, Clean: true}, {Head: testSigned, Tree: testTree, Clean: true}}}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || !strings.Contains(p.calls[0].Prompt, "Prior human acceptance.") || !strings.Contains(p.calls[0].Prompt, "not as acceptance of this revision") {
		t.Fatal("prior decision was not scoped to its original publication")
	}
}

func TestPassedCIRecoveryRejectsUnsafeOrIncompleteState(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Journal)
	}{
		{"unrelated hold", func(j *Journal) {
			j.Outcome.Summary = "CI failed; authentication credentials are unavailable, rerun CI"
			j.StopReason = j.Outcome.Summary
		}},
		{"product decision", func(j *Journal) {
			j.Outcome.Summary = "CI failed; product decision required before re-run"
			j.StopReason = j.Outcome.Summary
		}},
		{"question", func(j *Journal) { j.Outcome.Questions = []string{"Choose?"} }},
		{"pending feedback", func(j *Journal) { j.PRFeedback.Entries[0].Assessment = nil; j.PRFeedback.Entries[0].Reply = nil }},
		{"outstanding reply", func(j *Journal) { j.PRFeedback.Entries[0].Reply = nil }},
		{"candidate checks", func(j *Journal) { j.Evidence.Passed = false }},
		{"failed CI on other head", func(j *Journal) { j.CIRepairBudget.LastGrantedHead = testBase }},
		{"review recovery", func(j *Journal) { j.ReviewAssessment.Pending = true }},
		{"different native block", func(j *Journal) { j.StopReason = "other hold" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, j, _, _, _ := passedCIRecoveryFixture(t)
			tc.change(j)
			if canRecoverPassedCI(*j) {
				t.Fatal("unsafe recovery admitted")
			}
		})
	}
}

func TestPassedCIRecoveryRetainsHoldWhenBoundaryFails(t *testing.T) {
	for _, name := range []string{"still failing", "pending", "source changed", "published tree changed", "transport"} {
		t.Run(name, func(t *testing.T) {
			dir, j, r, p, pub := passedCIRecoveryFixture(t)
			before := j.Outcome.Summary
			switch name {
			case "still failing":
				pub.statuses = []string{"failed"}
			case "pending":
				pub.statuses = []string{"pending"}
			case "source changed":
				r.Repository = &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}}
			case "published tree changed":
				r.Repository = &fakeRepository{revisions: []Revision{{Head: testHead, Tree: testTree, Clean: true}, {Head: testHead, Tree: testBase, Clean: true}}}
			case "transport":
				pub.ciErr = errors.New("unavailable")
			}
			err := r.recoverPassedCI(context.Background(), dir, j)
			if err == nil || j.State != "blocked" || j.Outcome.Summary != before || !j.PRFeedback.Active || len(j.CIPassedRecoveries) != 0 || len(p.calls) != 0 {
				t.Fatal("failed boundary did not retain original hold", err)
			}
		})
	}
}

func TestPassedCIRecoveryRechecksBeforeReview(t *testing.T) {
	dir, j, r, p, pub := passedCIRecoveryFixture(t)
	pub.statuses = []string{"passed", "failed"}
	err := r.Run(context.Background(), dir, j, "")
	if err == nil || j.State != "blocked" || len(p.calls) != 0 || len(j.CIPassedRecoveries) != 1 || PRFeedbackStatus(*j).Unresolved != 1 {
		t.Fatal("CI changed after preflight without preserving the hold", err)
	}
}

func TestPassedCIRecoveryAuditRejectsCorruptEvidence(t *testing.T) {
	dir, j, r, _, _ := passedCIRecoveryFixture(t)
	if err := r.recoverPassedCI(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*CIPassedRecovery){
		func(a *CIPassedRecovery) { a.Candidate.Tree = "invalid" },
		func(a *CIPassedRecovery) { a.Publication.HeadSHA = "invalid" },
		func(a *CIPassedRecovery) { a.PriorCI.Status = "passed" },
		func(a *CIPassedRecovery) { a.PassedCI.Status = "failed" },
		func(a *CIPassedRecovery) { a.BlockSummary = "" },
	} {
		copy := *j
		copy.CIPassedRecoveries = append([]CIPassedRecovery(nil), j.CIPassedRecoveries...)
		change(&copy.CIPassedRecoveries[0])
		if validateCIPassedRecoveries(&copy) == nil {
			t.Fatal("corrupt recovery evidence accepted")
		}
	}
}

func TestCIRerunOnlyHoldRefusesUnrelatedRequests(t *testing.T) {
	if !ciRerunOnlyHold("CI failed when build without service credentials timed out; re-run CI. No code change is needed.", nil) {
		t.Fatal("irrelevant credential mention blocked CI-only hold")
	}
	for _, v := range []string{"CI failed, rerun after authentication; no source change needed", "CI failed, rerun after product decision; no source change needed", "CI failed due to missing credentials; rerun; no source change needed", "CI failed; rerun once human approval is obtained; no source change needed", "CI failed; fix source"} {
		if ciRerunOnlyHold(v, nil) {
			t.Fatal("unsafe hold accepted", v)
		}
	}
	if ciRerunOnlyHold(strings.Repeat("x", 17000)+" CI failed rerun", nil) {
		t.Fatal("oversized hold accepted")
	}
	if ciRerunOnlyHold("CI failed; re-run with no source change.", []string{"Another unrelated issue remains unresolved."}) {
		t.Fatal("unrelated limitation bypassed")
	}
}
