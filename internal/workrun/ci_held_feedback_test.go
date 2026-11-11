package workrun

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type ciHeldFeedbackPublisher struct {
	*ciHeldPublisher
	comments []PRFeedbackComment
	reads    int
}

func (p *ciHeldFeedbackPublisher) Feedback(context.Context, Plan, Publication) ([]PRFeedbackComment, error) {
	p.reads++
	return p.comments, nil
}
func (p *ciHeldFeedbackPublisher) ReplyFeedback(context.Context, Plan, Publication, PRFeedbackComment, string, string) (PRFeedbackReply, error) {
	return PRFeedbackReply{}, errors.New("unexpected reply")
}

func ciHeldFeedbackFixture(t *testing.T) (string, *Journal, *ciHeldFeedbackPublisher, Runner) {
	t.Helper()
	dir, j, _, base, runner := ciHeldReviewFixture(t)
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, testHead, testTree)
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("review: %v", err)
	}
	pub := &ciHeldFeedbackPublisher{ciHeldPublisher: base}
	runner.Publisher = pub
	runner.ReviewCIHold = false
	return dir, j, pub, runner
}

func TestCIHeldFeedbackEmptyScanRetainsHoldAndReceipt(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected CI hold: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "blocked" || saved.ResumeState != "ci" || saved.CI.Status == "passed" || saved.PRFeedback == nil || saved.PRFeedback.Active || saved.PRFeedback.CIHeldScan == nil || saved.PRFeedback.CIHeldScan.Publication != saved.Publication || saved.PRFeedback.CIHeldScan.Attempt != saved.Attempt || pub.reads != 1 || pub.calls != 0 || pub.ci != 0 {
		t.Fatalf("invalid CI-held feedback result: %+v", saved)
	}
}

func TestCIHeldFeedbackChangedBoundaryLeavesPendingFetch(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	pub.failAt = pub.boundaries + 1
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected boundary hold: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PRFeedback == nil || !saved.PRFeedback.FetchPending || saved.PRFeedback.CIHeldScan != nil || saved.CI.Status == "passed" || pub.calls != 0 {
		t.Fatal("changed boundary certified or published")
	}
}

func TestCIHeldFeedbackPendingCommentEntersRepairWithoutCertifyingCI(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	pub.comments = []PRFeedbackComment{generalFeedback("comment:1", "Please fix this")}
	if err := runner.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	status := PRFeedbackStatus(saved)
	if saved.State != "repairing" || status.Pending == 0 || saved.CI.Status == "passed" || saved.Evidence.Passed || pub.calls != 0 {
		t.Fatalf("feedback not retained for repair: %+v", status)
	}
}

type interruptedMigratedProvider struct{ calls []Session }

func (*interruptedMigratedProvider) Status(context.Context, string) (string, error) {
	return "stored", nil
}
func (p *interruptedMigratedProvider) Execute(_ context.Context, s Session, _, _ io.Writer) (SessionResult, error) {
	p.calls = append(p.calls, s)
	return SessionResult{}, errors.New("interrupted")
}

func TestMigratedCIHeldReviewAllowsOneFreshImplementationAttempt(t *testing.T) {
	dir, j, _, base, _, runner := runtimeRecheckFixture(t)
	runner.rawOutput = io.Discard
	if err := runner.recheckFeatureRuntime(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("recheck: %v", err)
	}
	reviewer := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	runner.Provider = reviewer
	runner.ReviewCIHold = true
	if err := runner.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("review: %v", err)
	}
	if j.SessionID != "" || j.Attempt <= j.RuntimeMigrations[0].PreviousAttempt || j.RuntimeRecheck.ImplementationAttempt != 0 {
		t.Fatal("review was mistaken for implementation")
	}
	pub := &ciHeldFeedbackPublisher{ciHeldPublisher: base, comments: []PRFeedbackComment{generalFeedback("comment:1", "Please fix this")}}
	runner.Publisher, runner.ReviewCIHold = pub, false
	if err := runner.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	implementer := &interruptedMigratedProvider{}
	runner.Provider = implementer
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected interrupted implementation")
	}
	if len(implementer.calls) != 1 || implementer.calls[0].ResumeID != "" || j.RuntimeRecheck.ImplementationAttempt == 0 {
		t.Fatal("fresh implementation did not start once")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), dir, &saved, ""); err == nil || len(implementer.calls) != 1 {
		t.Fatal("interrupted fresh session was started again")
	}
}

func TestCIHeldReviewFindingsStartBoundRepairWithoutPassingCI(t *testing.T) {
	dir, j, provider, pub, runner := ciHeldReviewFixture(t)
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, testHead, testTree)
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 12}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider.outcomes = []Outcome{reviewedFinding("P1")}
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected interrupted implementation")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ReviewAssessment == nil || !saved.ReviewAssessment.Pending || !saved.ReviewAssessment.CIHeld || saved.ReviewAssessment.Publication != saved.Publication || saved.CI.Status == "passed" || pub.ci != 0 || pub.calls != 0 || len(provider.calls) != 2 {
		t.Fatalf("review repair was not bound to CI-held publication: %+v", saved.ReviewAssessment)
	}
}

func TestCIHeldReviewFindingsRejectChangedPRBeforeRepair(t *testing.T) {
	dir, j, provider, pub, runner := ciHeldReviewFixture(t)
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, testHead, testTree)
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 12}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider.outcomes = []Outcome{reviewedFinding("P1")}
	pub.failAt = 4
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected changed boundary")
	}
	if len(provider.calls) != 1 || pub.calls != 0 || j.ReviewAssessment != nil || j.CI.Status == "passed" {
		t.Fatal("changed publication started repair or passed CI")
	}
}

func TestCIHeldReviewRepairPublicationInvalidatesOldReview(t *testing.T) {
	dir, j, provider, pub, runner := ciHeldReviewFixture(t)
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, testHead, testTree)
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 12}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider.outcomes = []Outcome{reviewedFinding("P1"), testOutcome("implemented")}
	pub.heads = []string{testTree}
	pub.statuses = []string{"failed"}
	if err := runner.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("expected failed CI hold")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pub.calls != 1 || saved.Publication.HeadSHA != testTree || saved.CIHeldReview == nil || !saved.CIHeldReview.Invalidated || completedReviewAwaitingCI(&saved) || saved.State == "ready" || saved.CI.Status == "passed" {
		t.Fatalf("old review reused after publication: state=%s calls=%d", saved.State, pub.calls)
	}
}

func TestCIHeldFeedbackBoundaryFailureDiscardPreservesReviewAndHistory(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	prior := *j.CIHeldReview
	pub.failAt = pub.boundaries + 1
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected boundary failure: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ValidatePRFeedbackResume(saved) != nil || ValidateDiscardCIHeldFeedbackScan(saved) != nil {
		t.Fatal("failed boundary recovery not exposed")
	}
	runner.DiscardCIHeldFeedbackScan = true
	if err := runner.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	saved, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "blocked" || saved.ResumeState != "ci" || !reflect.DeepEqual(*saved.CIHeldReview, prior) || saved.PRFeedback.FetchPending || saved.PRFeedback.Active || len(saved.PRFeedback.CIHeldBoundaryFailures) != 1 || saved.PRFeedback.CIHeldBoundaryFailures[0].DiscardedAt == nil || pub.calls != 0 || pub.ci != 0 {
		t.Fatal("scan discard changed review or publication")
	}
}

func TestCIHeldFeedbackBoundaryFailureCannotDiscardPendingEntries(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	comment := generalFeedback("comment:existing", "A previous unresolved comment")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, Entries: []PRFeedbackEntry{{Comment: comment, Fingerprint: feedbackFingerprint(comment)}}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	pub.failAt = pub.boundaries + 1
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected boundary failure: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateDiscardCIHeldFeedbackScan(saved) == nil {
		t.Fatal("discard admitted unresolved feedback")
	}
}

func TestCIHeldFeedbackFailedScanCanRetryWithoutLosingUnresolvedComments(t *testing.T) {
	dir, j, pub, runner := ciHeldFeedbackFixture(t)
	comment := generalFeedback("comment:existing", "A previous unresolved comment")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, Entries: []PRFeedbackEntry{{Comment: comment, Fingerprint: feedbackFingerprint(comment)}}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	pub.failAt = pub.boundaries + 1
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected boundary failure: %v", err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub.failAt = 0
	pub.comments = []PRFeedbackComment{comment}
	if err := runner.beginPRFeedback(context.Background(), dir, &saved); err != nil {
		t.Fatal(err)
	}
	saved, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "repairing" || PRFeedbackStatus(saved).Pending != 1 || len(saved.PRFeedback.Entries) != 1 || saved.PRFeedback.CIHeldBoundaryFailures[0].RecoveredAt == nil {
		t.Fatal("retry lost feedback or boundary audit")
	}
}

func TestProvisionalParentProofRejectsStaleEvidenceAndUnresolvedFeedback(t *testing.T) {
	dir, j, _, runner := ciHeldFeedbackFixture(t)
	j.Plan.Ticket = "01-example.md"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("scan: %v", err)
	}
	proof, err := ProvisionalProofForCIHold(*j)
	if err != nil {
		t.Fatal(err)
	}
	if proof.RunID != j.ID || proof.Publication != j.Publication || proof.CheckKey != j.Evidence.Key || proof.Attempt != j.Attempt || proof.ScannedAt.IsZero() {
		t.Fatal("proof lost checkpoint identity")
	}
	cases := []struct {
		name   string
		mutate func(*Journal)
	}{
		{"attempt", func(c *Journal) { c.Attempt++ }},
		{"publication", func(c *Journal) { c.Publication.HeadSHA = testTree }},
		{"checks", func(c *Journal) { c.Evidence.Passed = false }},
		{"scan", func(c *Journal) { c.PRFeedback.CIHeldScan = nil }},
		{"CI passed", func(c *Journal) { c.CI.Status = "passed" }},
		{"feedback active", func(c *Journal) { c.PRFeedback.Active = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&saved)
			if _, err := ProvisionalProofForCIHold(saved); err == nil {
				t.Fatal("stale proof accepted")
			}
		})
	}
}

func TestProvisionalParentRequiresFreshUnchangedFeedback(t *testing.T) {
	dir, j, _, runner := ciHeldFeedbackFixture(t)
	j.Plan.Ticket = "01-example.md"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if err := ValidateProvisionalFeedback(*j, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProvisionalFeedback(*j, []PRFeedbackComment{generalFeedback("comment:new", "A new comment")}); err == nil {
		t.Fatal("new feedback accepted")
	}
}

func TestProvisionalCIIdentityNeverUsesAnotherHead(t *testing.T) {
	dir, j, _, runner := ciHeldFeedbackFixture(t)
	j.Plan.Ticket = "01-example.md"
	j.CI = CIResult{Status: "failed", FailureContext: &CIFailureContext{Head: j.Publication.HeadSHA, Checks: []CIFailedCheck{{Kind: "actions", Name: "build", AppID: 15368, AppSlug: "github-actions", RepositoryID: 1, RunID: 41, RunAttempt: 2, JobID: 99, CheckID: 101, Conclusion: "failure"}}}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.beginPRFeedback(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	proof, err := ProvisionalProofForCIHold(*j)
	if err != nil {
		t.Fatal(err)
	}
	if proof.CIHead != j.Publication.HeadSHA || proof.CIStatus != "failed" || proof.CIHistorical || !strings.Contains(proof.CIIdentities, `"run_id":41`) || !strings.Contains(proof.CIIdentities, `"run_attempt":2`) || !strings.Contains(proof.CIIdentities, `"job_id":99`) {
		t.Fatalf("missing CI identity: %+v", proof)
	}
	j.CI.FailureContext.Head = testTree
	_, identities := captureCIIdentity(j)
	if identities != "" {
		t.Fatal("another head's CI identifiers were retained")
	}
	j.RuntimeRecheck = &RuntimeRecheck{Publication: j.Publication, PreviousCIStatus: "failed", PreviousCIIdentities: proof.CIIdentities}
	j.CI = CIResult{}
	proof, err = ProvisionalProofForCIHold(*j)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.CIHistorical || proof.CIStatus != "failed" || proof.CIIdentities == "" {
		t.Fatal("historical CI identity not labelled")
	}
	j.RuntimeRecheck.Publication.HeadSHA = testTree
	proof, err = ProvisionalProofForCIHold(*j)
	if err != nil {
		t.Fatal(err)
	}
	if proof.CIHistorical || proof.CIIdentities != "" || proof.CIStatus != "unverified" {
		t.Fatal("historical CI copied onto another head")
	}
}
