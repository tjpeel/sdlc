package workrun

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type provisionalRebasePublisher struct {
	*resumeTestPublisher
	observations int
}

func (p *provisionalRebasePublisher) Observe(ctx context.Context, plan Plan, publication Publication) (RemotePR, error) {
	p.observations++
	return p.resumeTestPublisher.Observe(ctx, plan, publication)
}

type provisionalRebaseRepository struct {
	*resumeTestRepository
	inspections int
}

func (r *provisionalRebaseRepository) Inspect(ctx context.Context, workspace string) (Revision, error) {
	r.inspections++
	return r.resumeTestRepository.Inspect(ctx, workspace)
}

func TestStandaloneRebaseRejectsProvisionalChildBeforeMutation(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, published := range []bool{false, true} {
			dir, j := testRun(t)
			j.Plan.ProvisionalParent = &ProvisionalParentProof{Ticket: "01-parent.md"}
			j.ResumeRebase = pending
			if published {
				j.Publication = Publication{Number: 40, HeadSHA: testHead}
			}
			before := *j
			pub := &provisionalRebasePublisher{resumeTestPublisher: &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "other", BaseSHA: testTree, HeadSHA: testHead}}}
			repo := &provisionalRebaseRepository{resumeTestRepository: &resumeTestRepository{}}
			_, err := (Runner{Publisher: pub, Repository: repo, Rebase: !pending}).checkResumeUpstream(context.Background(), dir, j)
			if err == nil || !strings.Contains(err.Error(), "owning feature") || pub.observations != 0 || pub.snapshots != 0 || repo.inspections != 0 || repo.calls != 0 || !reflect.DeepEqual(before, *j) {
				t.Fatalf("provisional rebase crossed a boundary: %v", err)
			}
		}
	}
}

func ciHeldLegacyBudgetFixture(t *testing.T, migrated bool) (string, *Journal, *fakeProvider, *ciHeldPublisher, Runner) {
	t.Helper()
	var dir string
	var j *Journal
	var pub *ciHeldPublisher
	var runner Runner
	if migrated {
		var checker *fakeChecker
		var repo *runtimeRecheckRepository
		dir, j, checker, pub, repo, runner = runtimeRecheckFixture(t)
		_, _ = checker, repo
		runner.rawOutput = io.Discard
		if err := runner.recheckFeatureRuntime(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
			t.Fatalf("recheck: %v", err)
		}
	} else {
		var unused *fakeProvider
		dir, j, unused, pub, runner = ciHeldReviewFixture(t)
		_ = unused
		j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
		j.Evidence.Key = verificationKey(j, VerificationRequest{}, testHead, testTree)
	}
	j.ReviewRepairBudget = nil
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	reviewer := &fakeProvider{outcomes: []Outcome{reviewedFinding("P1")}}
	runner.Provider, runner.ReviewCIHold = reviewer, true
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.ResumeState != "reviewing" || j.StopReason != reviewRepairBound {
		t.Fatalf("legacy hold: %v", err)
	}
	return dir, j, reviewer, pub, runner
}

func TestLegacyCIHeldReviewRepairBudgetInitialization(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		t.Run(map[bool]string{false: "original session", true: "fresh migrated session"}[migrated], func(t *testing.T) {
			dir, j, _, pub, runner := ciHeldLegacyBudgetFixture(t, migrated)
			if j.CI.Status == "passed" || ValidateReviewRepairInitialization(*j, 1) != nil {
				t.Fatal("eligible CI-held repair budget rejected")
			}
			provider := &fakeProvider{executeErr: errors.New("test interrupted")}
			runner.Provider, runner.ReviewCIHold, runner.InitializeReviewRepairBudget = provider, false, 1
			if err := runner.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("expected test interruption")
			}
			saved, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.ReviewRepairBudget == nil || saved.ReviewRepairBudget.Used != 1 || saved.ReviewAssessment == nil || !saved.ReviewAssessment.CIHeld || !saved.ReviewAssessment.Pending || saved.ReviewAssessment.Publication != saved.Publication || pub.ci != 0 || pub.calls != 0 || len(provider.calls) != 1 {
				t.Fatal("legacy grant did not preserve CI-held handoff")
			}
			if migrated && provider.calls[0].ResumeID != "" {
				t.Fatal("migrated repair did not start a fresh implementation session")
			}
			if !migrated && provider.calls[0].ResumeID != testNative {
				t.Fatal("original implementation session not retained")
			}
		})
	}
}

func TestLegacyCIHeldReviewBudgetRejectsStaleOrPendingWork(t *testing.T) {
	_, original, _, _, _ := ciHeldLegacyBudgetFixture(t, true)
	for _, tc := range []struct {
		name   string
		mutate func(*Journal)
	}{
		{"changed publication", func(j *Journal) { j.Publication.HeadSHA = testTree }},
		{"invalidated review", func(j *Journal) { j.CIHeldReview.Invalidated = true }},
		{"unfinished feedback", func(j *Journal) { j.PRFeedback = &PRFeedbackHistory{Active: true} }},
		{"interrupted fresh session", func(j *Journal) { j.RuntimeRecheck.ImplementationAttempt = j.Attempt }},
		{"pending input", func(j *Journal) { j.PendingInputs = &PendingInputRecovery{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *original
			review := *original.CIHeldReview
			copy.CIHeldReview = &review
			recheck := *original.RuntimeRecheck
			copy.RuntimeRecheck = &recheck
			tc.mutate(&copy)
			if ValidateReviewRepairInitialization(copy, 1) == nil {
				t.Fatal("unsafe repair budget accepted")
			}
		})
	}
}

func TestLegacyCIHeldReviewBudgetRechecksLivePublicationBeforeGrant(t *testing.T) {
	dir, j, _, pub, runner := ciHeldLegacyBudgetFixture(t, false)
	pub.failAt = pub.boundaries + 1
	runner.ReviewCIHold, runner.InitializeReviewRepairBudget = false, 1
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("changed PR admitted")
	}
	if j.ReviewRepairBudget != nil || j.ReviewAssessment != nil || pub.calls != 0 || pub.ci != 0 {
		t.Fatal("changed PR granted repair or published")
	}
}

func TestLegacyCIHeldRepairBudgetRetainsSettledFeedback(t *testing.T) {
	_, j, _, _, _ := ciHeldLegacyBudgetFixture(t, true)
	comment := generalFeedback("comment:1", "Already resolved")
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), StartedAttempt: j.Attempt}
	entry.Assessment = &PRFeedbackAssessment{ID: comment.ID, Fingerprint: entry.Fingerprint, Disposition: "no_change", Reason: "verified"}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	entry.Reply = &PRFeedbackReply{ID: "recorded-reply"}
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	if err := ValidateReviewRepairInitialization(*j, 1); err != nil {
		t.Fatalf("settled feedback was rejected: %v", err)
	}
	j.PRFeedback.Entries[0].Reply = nil
	if ValidateReviewRepairInitialization(*j, 1) == nil {
		t.Fatal("unreplied feedback was bypassed")
	}
}
