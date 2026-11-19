package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const takeoverSession = "00000000-0000-4000-8000-000000000002"
const takeoverReviewSession = "00000000-0000-4000-8000-000000000003"

func takeoverFixture(t *testing.T) (string, *Journal) {
	t.Helper()
	dir, j := testRun(t)
	if err := os.WriteFile(filepath.Join(dir, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", "context canceled\ncontext canceled"
	j.PendingRole = "implementation"
	j.SessionID = testNative
	j.ReportedModel = "old-served-model"
	j.Attempt, j.Rounds = 10, 2
	j.Plan.Roles.Implementation = Model{Provider: "claude", Name: "claude-sonnet-5-5", Effort: "medium"}
	j.Plan.Roles.Review = Model{Provider: "codex", Name: "gpt-6.1-sol", Effort: "high"}
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: testHead, BaseSHA: testBase}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree, Commands: j.Plan.Checks}
	j.CI = CIResult{Status: "passed"}
	j.Outcome = reviewedFinding("P2")
	j.Outcome.ReviewCoverage = coverage("full")
	if err := recordReviewReport(j); err != nil {
		t.Fatal(err)
	}
	j.ReviewAcceptances = []ReviewAcceptance{{Publication: j.Publication, Attempt: j.Attempt, Tree: testTree, ScopeKey: j.ReviewReports[0].InspectionBoundary.ScopeKey, Findings: append([]Finding(nil), j.Outcome.Findings...), Reason: "Previous acceptance rationale.", AcceptedAt: time.Now().UTC()}}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3, Used: 1, StartRound: 1, LastGrantedHead: testHead}
	j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Findings: append([]Finding(nil), j.Outcome.Findings...), Pending: true, StartedAttempt: 10}
	j.ReviewHumanInput = &ReviewHumanInput{Questions: []string{"Prior question?"}, Answer: "Prior human decision."}
	j.Feedback = "Assess the retained review finding."
	c := generalFeedback("comment:1", "Retained unresolved feedback")
	a := assessedOutcome(c, "unaddressable").PRFeedbackAssessments[0]
	e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason, Reply: &PRFeedbackReply{ID: "existing-reply"}}
	e.Marker = feedbackEntryMarker(j.ID, e)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{e}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j
}
func takeoverSelection() Model { return Model{Provider: "codex", Name: "gpt-6.1-sol", Effort: "low"} }
func grantTakeover(t *testing.T, dir string, j *Journal) Journal {
	t.Helper()
	if err := GrantProviderTakeover(dir, j.UpdatedAt, takeoverSelection(), "codex", "Explicit provider choice with separate review."); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

type takeoverTestProvider struct {
	calls         []Session
	fail          bool
	noID          bool
	sameReview    bool
	partialReview bool
	checked       bool
}

func (p *takeoverTestProvider) Status(context.Context, string) (string, error) { return "stored", nil }
func (p *takeoverTestProvider) Execute(_ context.Context, s Session, _, _ io.Writer) (SessionResult, error) {
	p.calls = append(p.calls, s)
	if p.fail {
		id := takeoverSession
		if p.noID {
			id = ""
		}
		return SessionResult{SessionID: id}, errors.New("interrupted")
	}
	if s.Role == "review" {
		o := testOutcome("reviewed")
		if p.partialReview {
			o.Status = "review_incomplete"
			o.ReviewCoverage = coverage("partial", "Further inspection required")
		}
		bindTestReviewBoundary(&o, s)
		id := takeoverReviewSession
		if p.sameReview {
			id = takeoverSession
		}
		return SessionResult{Outcome: o, SessionID: id}, nil
	}
	if !p.checked {
		p.checked = true
		return SessionResult{Outcome: testOutcome("checks_requested"), SessionID: takeoverSession}, nil
	}
	return SessionResult{Outcome: testOutcome("implemented"), SessionID: takeoverSession}, nil
}

func TestProviderTakeoverPreservesHistoryAndRequiresFreshIndependentReview(t *testing.T) {
	dir, original := takeoverFixture(t)
	oldAssessment := *original.ReviewAssessment
	oldFeedback := *original.PRFeedback
	oldAttempt, oldRounds := original.Attempt, original.Rounds
	j := grantTakeover(t, dir, original)
	if j.SessionID != "" || j.ReportedModel != "" || j.ProviderTakeover.PreviousReportedModel != "old-served-model" || j.ProviderTakeover.PreviousSessionID != testNative || !reflect.DeepEqual(*j.ReviewAssessment, oldAssessment) || !reflect.DeepEqual(*j.PRFeedback, oldFeedback) || j.Attempt != oldAttempt || j.Rounds != oldRounds || !takeoverNeedsReview(&j) {
		t.Fatal("takeover lost history or accounting")
	}
	p := &takeoverTestProvider{}
	pub := &fakePublisher{}
	checks := &fakeChecker{}
	r := fakeRunner(&fakeProvider{}, checks, pub, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: pub}
	if err := r.Run(context.Background(), dir, &j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls == 0 || j.ProviderTakeover.ReviewAttempt == 0 || j.ProviderTakeover.ReviewSessionID != takeoverReviewSession || len(p.calls) < 3 {
		t.Fatal("fresh checks and review did not complete")
	}
	first, last := p.calls[0], p.calls[len(p.calls)-1]
	if first.Role != "implementation" || first.Model.Provider != "codex" || first.ResumeID != "" || !strings.Contains(first.Directory, "native-implementation-takeover-1") || !strings.Contains(first.Prompt, "Prior human decision.") || !strings.Contains(first.Prompt, "Previous acceptance rationale.") || !strings.Contains(first.Prompt, "Retained unresolved feedback") {
		t.Fatal("new implementation session lost context or reused prior session")
	}
	if strings.Contains(first.Prompt, testNative) || strings.Contains(last.Prompt, testNative) {
		t.Fatal("previous native session identity was forwarded to a new provider")
	}
	if last.Role != "review" || last.Model.Provider != "codex" || last.ResumeID != "" || last.Directory == first.Directory || !strings.Contains(last.Directory, "native-review-") || !strings.Contains(last.Prompt, "Previous acceptance rationale.") || !strings.Contains(last.Prompt, "without assuming it approves another revision") {
		t.Fatal("same-provider review was not independent")
	}
	if PRFeedbackStatus(j).Unresolved != 1 || j.ProviderTakeover.PreviousSessionID != testNative {
		t.Fatal("unresolved feedback or original session lost")
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

func TestProviderTakeoverRefusesUnsafeHoldsAndImplicitReviewer(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Journal)
		review string
	}{
		{"usage hold", func(j *Journal) {
			j.StopReason = "native claude headless session failed; inspect the private run log before retrying"
		}, "codex"},
		{"auth hold", func(j *Journal) { j.StopReason = "provider authentication failed" }, "codex"},
		{"policy hold", func(j *Journal) { j.StopReason = "provider policy refusal" }, "codex"},
		{"usage disguised by extra line", func(j *Journal) { j.StopReason = "context canceled\nprovider rate limit" }, "codex"},
		{"question", func(j *Journal) { j.Outcome.Questions = []string{"Choose?"} }, "codex"},
		{"dirty check evidence", func(j *Journal) { j.Evidence.Passed = false }, "codex"},
		{"wrong CI", func(j *Journal) { j.CI.Status = "failed" }, "codex"},
		{"unselected reviewer", func(*Journal) {}, ""},
		{"wrong reviewer", func(*Journal) {}, "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, j := takeoverFixture(t)
			tc.change(j)
			if ValidateProviderTakeover(*j, takeoverSelection(), tc.review, "reason") == nil {
				t.Fatal("unsafe takeover accepted")
			}
		})
	}
}

func TestProviderTakeoverDoesNotRestartUnknownInterruptedSession(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{fail: true, noID: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	if err := r.Run(context.Background(), dir, &j, ""); err == nil {
		t.Fatal("expected interrupted provider")
	}
	if len(p.calls) != 1 || j.ProviderTakeover.StartedAttempt == 0 || j.SessionID != "" {
		t.Fatal("interrupted session boundary not saved")
	}
	before := len(p.calls)
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || len(p.calls) != before {
		t.Fatal("unidentified native session was silently restarted")
	}
}

func TestProviderTakeoverResumesKnownInterruptedSession(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{fail: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || j.SessionID != takeoverSession {
		t.Fatal("known session identity not saved")
	}
	p.fail = false
	if err := r.Run(context.Background(), dir, &j, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) < 3 || p.calls[1].ResumeID != takeoverSession || p.calls[1].Directory != p.calls[0].Directory {
		t.Fatal("interrupted takeover did not resume the same new session")
	}
}

func TestProviderTakeoverRechecksCandidateBeforeNewSession(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}})
	r.Provider = p
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || len(p.calls) != 0 || j.ProviderTakeover.StartedAttempt != 0 {
		t.Fatal("changed candidate was dispatched")
	}
}

func TestProviderTakeoverRejectsForgedReviewProof(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	j.ProviderTakeover.StartedAttempt = j.Attempt + 1
	j.Attempt += 2
	j.ProviderTakeover.BoundaryVerified = true
	j.ProviderTakeover.ReviewAttempt = j.Attempt
	j.ProviderTakeover.ReviewSessionID = takeoverReviewSession
	if validateProviderTakeover(&j) == nil {
		t.Fatal("unrecorded review proof was accepted")
	}
}

func TestProviderTakeoverRejectsReviewReusingImplementationSession(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{sameReview: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || j.State == "ready" || j.ProviderTakeover.ReviewAttempt != 0 || len(p.calls) < 3 || p.calls[len(p.calls)-1].Role != "review" {
		t.Fatal("same native session accepted as independent review")
	}
}

func TestProviderTakeoverRejectsPartialReviewInImplementationSession(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{sameReview: true, partialReview: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || len(p.calls) < 3 || j.ReviewContinuation != nil || j.ProviderTakeover.ReviewAttempt != 0 {
		t.Fatal("partial review reused the implementation session")
	}
}

func TestProviderTakeoverSavedReviewerHoldCannotSkipFreshReview(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	p := &takeoverTestProvider{}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	r.Publisher = &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	if err := r.Run(context.Background(), dir, &j, ""); err != nil {
		t.Fatal(err)
	}
	j.ProviderTakeover.ReviewAttempt = 0
	j.ProviderTakeover.ReviewSessionID = ""
	j.ProviderTakeover.ReviewPublication = nil
	j.State, j.ResumeState, j.StopReason = "failed", "reviewing", errNativeIncomplete.Error()
	p.calls = nil
	if err := r.Run(context.Background(), dir, &j, ""); err == nil || j.State == "ready" || j.ReviewSkip != nil || len(p.calls) != 0 || !strings.Contains(j.StopReason, "independent review after provider takeover remains required") {
		t.Fatalf("saved reviewer hold bypassed review: state=%s reason=%s calls=%d err=%v", j.State, j.StopReason, len(p.calls), err)
	}
}
