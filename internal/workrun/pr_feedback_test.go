package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type feedbackPublisherFake struct {
	*fakePublisher
	comments          []PRFeedbackComment
	reads, replies    int
	readErr, replyErr error
}

func (p *feedbackPublisherFake) Feedback(context.Context, Plan, Publication) ([]PRFeedbackComment, error) {
	p.reads++
	return p.comments, p.readErr
}
func (p *feedbackPublisherFake) ReplyFeedback(_ context.Context, _ Plan, _ Publication, _ PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	p.replies++
	if p.replyErr != nil {
		return PRFeedbackReply{}, p.replyErr
	}
	return PRFeedbackReply{ID: marker, URL: body}, nil
}

type feedbackScriptProvider struct {
	sessions []Session
	outcomes []Outcome
}

func (p *feedbackScriptProvider) Status(context.Context, string) (string, error) {
	return "stored", nil
}
func (p *feedbackScriptProvider) Execute(_ context.Context, s Session, _, _ io.Writer) (SessionResult, error) {
	p.sessions = append(p.sessions, s)
	if len(p.outcomes) == 0 {
		return SessionResult{}, errors.New("unexpected native turn")
	}
	out := p.outcomes[0]
	p.outcomes = p.outcomes[1:]
	return SessionResult{Outcome: out, SessionID: testNative, ReportedModel: s.Model.Name}, nil
}
func feedbackReadyRun(t *testing.T) (string, *Journal, Runner, *feedbackPublisherFake, *fakeChecker) {
	t.Helper()
	dir, j := testRun(t)
	c := &fakeChecker{}
	pub := &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
	r := fakeRunner(&fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}, c, pub.fakePublisher, &fakeRepository{})
	r.Publisher = pub
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	return dir, j, r, pub, c
}
func generalFeedback(id, body string) PRFeedbackComment {
	return PRFeedbackComment{ID: id, Kind: "comment", Author: "reviewer", Body: body, UpdatedAt: "2026-10-13T12:00:00Z"}
}
func assessedOutcome(c PRFeedbackComment, disposition string) Outcome {
	out := testOutcome("implemented")
	out.PRFeedbackAssessments = []PRFeedbackAssessment{{ID: c.ID, Fingerprint: feedbackFingerprint(c), Disposition: disposition, Reason: "The API contract requires a product decision.", Commits: []string{}}}
	return out
}

func TestPRFeedbackUnchangedHeadRunsChecksAndFreshIndependentReview(t *testing.T) {
	dir, j, r, pub, c := feedbackReadyRun(t)
	originalSession, roles, attempt, rounds := j.SessionID, j.Plan.Roles, j.Attempt, j.Rounds
	reviewBudget, ciBudget := *j.ReviewRepairBudget, *j.CIRepairBudget
	human := &ReviewHumanInput{Questions: []string{"Prior reviewer question"}, Answer: "Recorded decision"}
	j.ReviewHumanInput = human
	comment := generalFeedback("comment:1", "Clarify the response contract")
	pub.comments = []PRFeedbackComment{comment}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "unaddressable"), testOutcome("implemented"), testOutcome("reviewed")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.Publication.HeadSHA != testHead || j.PRFeedback.Active || j.PRFeedback.CompletedHead != testHead {
		t.Fatalf("fresh readiness missing: %+v", j.PRFeedback)
	}
	if c.calls != 2 || pub.replies != 1 || len(provider.sessions) != 3 || provider.sessions[2].Role != "review" || provider.sessions[2].ResumeID != "" {
		t.Fatalf("checks=%d replies=%d sessions=%+v", c.calls, pub.replies, provider.sessions)
	}
	for _, s := range provider.sessions[:2] {
		if s.ResumeID != originalSession {
			t.Fatal("implementation session changed")
		}
	}
	if j.Attempt != attempt+3 || j.Rounds != rounds || *j.ReviewRepairBudget != reviewBudget || *j.CIRepairBudget != ciBudget || j.Plan.Roles != roles || j.ReviewHumanInput.Answer != human.Answer {
		t.Fatal("feedback reset saved identity, history or budgets")
	}
	packet := provider.sessions[2].Prompt
	if !strings.Contains(packet, "human_pr_feedback") || !strings.Contains(packet, comment.Body) || !strings.Contains(packet, "unaddressable") || !strings.Contains(packet, "sdlc-pr-feedback") {
		t.Fatal("fresh review omitted human comments, assessment or reply evidence")
	}
	summary := PRFeedbackStatus(*j)
	if summary.Unresolved != 1 || summary.OutstandingReplies != 0 || !strings.Contains(summary.Summary, "product decision") {
		t.Fatalf("unresolved handoff hidden: %+v", summary)
	}
	// Identical feedback does not reopen the model or duplicate replies.
	provider.outcomes = nil
	before := j.Attempt
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.Attempt != before || pub.replies != 1 || c.calls != 2 {
		t.Fatal("unchanged feedback duplicated work")
	}
}

func TestPRFeedbackReadFailureRetryRetainsOriginalReadyBoundary(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	pub.readErr = errors.New("disposable API failure")
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("read error=%v", err)
	}
	if j.ResumeState != "pr_feedback" || !j.PRFeedback.FetchPending || ValidatePRFeedbackResume(*j) != nil {
		t.Fatal("feedback read retry lost eligibility")
	}
	pub.readErr = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.PRFeedback.Active {
		t.Fatal("empty feedback left cycle active")
	}
}

func TestPRFeedbackResumeCannotBypassFailures(t *testing.T) {
	_, j, _, _, _ := feedbackReadyRun(t)
	for _, state := range []string{"failed", "blocked", "waiting_for_human", "awaiting_reviewer"} {
		copy := *j
		copy.State = state
		copy.ResumeState = "implementing"
		if ValidatePRFeedbackResume(copy) == nil {
			t.Fatalf("%s bypassed", state)
		}
	}
	copy := *j
	copy.Outcome.Findings = []Finding{{Priority: "P1", Path: "file.go", Line: 1, Scenario: "failure", Recommendation: "repair"}}
	if ValidatePRFeedbackResume(copy) == nil {
		t.Fatal("independent findings bypassed")
	}
}

func TestPRFeedbackUpdatesRetainVersionsAndDeletion(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	old := generalFeedback("comment:1", "Old comment")
	changed := old
	changed.Body = "Edited comment"
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, CompletedHead: testHead, Entries: []PRFeedbackEntry{{Comment: old, Fingerprint: feedbackFingerprint(old), Assessment: &PRFeedbackAssessment{ID: old.ID, Fingerprint: feedbackFingerprint(old), Disposition: "no_change", Reason: "already handled", Commits: []string{}}}}}
	pub.comments = []PRFeedbackComment{changed}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if len(j.PRFeedback.Entries) != 2 || pendingFeedback(j).Comment.Body != changed.Body || j.PRFeedback.Entries[0].Comment.Body != old.Body {
		t.Fatal("edit overwrote original review context")
	}
	// A vanished pending comment is retained as a tombstone, never corrected.
	j.State = "ready"
	j.Outcome = testOutcome("reviewed")
	j.CI = CIResult{Status: "passed"}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	j.PRFeedback.Active = false
	pub.comments = nil
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if len(j.PRFeedback.Entries) != 3 || !j.PRFeedback.Entries[2].Comment.Deleted || pendingFeedback(j) != nil || j.State != "ready" {
		t.Fatal("deleted feedback lost or reprocessed")
	}
}

type feedbackHistoryRepository struct {
	*fakeRepository
	commits []string
}

func (r feedbackHistoryRepository) FeedbackCommits(context.Context, string, string, string) ([]string, error) {
	return r.commits, nil
}
func TestPRFeedbackRejectsInventedCorrectionCommit(t *testing.T) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Fix regression")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{{StartedAttempt: j.Attempt, Comment: comment, Fingerprint: feedbackFingerprint(comment)}}}
	r.Repository = feedbackHistoryRepository{fakeRepository: &fakeRepository{}, commits: []string{testSigned}}
	out := assessedOutcome(comment, "addressed")
	out.PRFeedbackAssessments[0].Commits = []string{testBase}
	if _, err := r.acceptPRFeedback(context.Background(), dir, j, out); err == nil || j.PRFeedback.Entries[0].Assessment != nil {
		t.Fatal("invented correction entered durable history")
	}
	out.PRFeedbackAssessments[0].Commits = []string{testSigned}
	if _, err := r.acceptPRFeedback(context.Background(), dir, j, out); err != nil {
		t.Fatal(err)
	}
}

func TestPRFeedbackReopenedCommentRetainsEarlierIdenticalFingerprint(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	open := generalFeedback("comment:1", "Original unresolved question")
	resolved := open
	resolved.Resolved = true
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, CompletedHead: testHead, Entries: []PRFeedbackEntry{{Occurrence: 0, Comment: open, Fingerprint: feedbackFingerprint(open)}, {Occurrence: 1, Comment: resolved, Fingerprint: feedbackFingerprint(resolved)}}}
	pub.comments = []PRFeedbackComment{open}
	if err := r.beginPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	pending := pendingFeedback(j)
	if len(j.PRFeedback.Entries) != 3 || pending == nil || pending.Occurrence != 2 || pending.Fingerprint != j.PRFeedback.Entries[0].Fingerprint {
		t.Fatal("reopened identical revision skipped")
	}
	if err := validatePRFeedbackJournal(j); err != nil {
		t.Fatal(err)
	}
	if feedbackEntryMarker(j.ID, j.PRFeedback.Entries[0]) == feedbackEntryMarker(j.ID, j.PRFeedback.Entries[2]) {
		t.Fatal("reopened reply reused earlier occurrence marker")
	}
}

func TestPRFeedbackReplyFailureResumesWithoutReassessing(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Please explain behavior")
	pub.comments = []PRFeedbackComment{comment}
	pub.replyErr = errors.New("unconfirmed reply")
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change"), testOutcome("implemented"), testOutcome("reviewed")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("reply failure=%v", err)
	}
	if j.State != "blocked" || j.ResumeState != "publishing" || len(provider.sessions) != 2 || j.PRFeedback.Entries[0].Marker == "" || j.PRFeedback.Entries[0].Reply != nil {
		t.Fatal("reply intent or completed assessment lost")
	}
	// Ordinary resume continues publication rather than admitting a new feedback action.
	pub.replyErr = nil
	r.ProcessPRFeedback = false
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(provider.sessions) != 3 || provider.sessions[2].Role != "review" || j.PRFeedback.Entries[0].Reply == nil {
		t.Fatal("reply retry repeated correction or missed fresh review")
	}
}

func TestPRFeedbackAssessmentDoesNotDropCompanionSourceRequest(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	first := generalFeedback("comment:1", "Inspect related source")
	second := generalFeedback("comment:2", "Explain contract")
	pub.comments = []PRFeedbackComment{first, second}
	requested := assessedOutcome(first, "no_change")
	requested.Status = "checks_requested"
	requested.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example/companion", Purpose: "inspect contract"}}
	provider := &feedbackScriptProvider{outcomes: []Outcome{requested, assessedOutcome(first, "no_change"), assessedOutcome(second, "no_change"), testOutcome("reviewed")}}
	cloner := &fakeCompanionCloner{revision: testSigned}
	r.CompanionCloner = cloner
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(cloner.calls) != 1 || cloner.calls[0] != "example/companion" || len(j.CompanionRepositories) != 1 || !strings.Contains(provider.sessions[1].Prompt, "example/companion") {
		t.Fatal("assessment progression discarded source request or its evidence")
	}
	if pub.replies != 2 || j.State != "ready" {
		t.Fatal("feedback source request lost remaining comments")
	}
}

func TestPRFeedbackRepliesUseRetainedSignedCorrectionIDs(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Correct response")
	assessment := PRFeedbackAssessment{ID: comment.ID, Fingerprint: feedbackFingerprint(comment), Disposition: "addressed", Reason: "Corrected the response.", Commits: []string{testSigned}}
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), Assessment: &assessment, ReplyBody: assessment.Reason}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	if err := r.replyPRFeedback(context.Background(), dir, j); err == nil || pub.replies != 0 {
		t.Fatal("unsigned correction was acknowledged")
	}
	if err := os.Mkdir(filepath.Join(dir, "publisher"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(filepath.Join(dir, "publisher", "signed-commits.json"), map[string]string{testSigned: testHead}); err != nil {
		t.Fatal(err)
	}
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	recorded := j.PRFeedback.Entries[0]
	if recorded.SignedCommits[testSigned] != testHead || !strings.Contains(recorded.ReplyBody, testHead) || strings.Contains(recorded.ReplyBody, testSigned) || recorded.Reply == nil {
		t.Fatal("original-to-signed correction linkage missing")
	}
	if err := r.replyPRFeedback(context.Background(), dir, j); err != nil || pub.replies != 1 {
		t.Fatal("acknowledged correction reply duplicated")
	}
}

type feedbackUnavailableReview struct {
	feedbackScriptProvider
	reviewProvider string
}

func (p *feedbackUnavailableReview) Status(_ context.Context, name string) (string, error) {
	if name == p.reviewProvider {
		return "missing", nil
	}
	return "stored", nil
}
func TestPRFeedbackUnavailableIndependentReviewKeepsGateOutstanding(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Explain unchanged behavior")
	pub.comments = []PRFeedbackComment{comment}
	provider := &feedbackUnavailableReview{feedbackScriptProvider: feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change"), testOutcome("implemented")}}, reviewProvider: j.Plan.Roles.Review.Provider}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("review hold=%v", err)
	}
	if j.State != "awaiting_reviewer" || j.Outcome.Status == "reviewed" || !j.PRFeedback.Active || j.PRFeedback.CompletedHead != "" || len(provider.sessions) != 2 || pub.replies != 1 {
		t.Fatal("unavailable fresh reviewer reused earlier approval")
	}
}

func TestPRFeedbackPersistedFetchCheckpointRecoversAfterProcessExit(t *testing.T) {
	for _, origin := range []string{"ready", "publishing"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%v", origin, explicit), func(t *testing.T) {
				dir, j, r, pub, _ := feedbackReadyRun(t)
				history := &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, Active: true, FetchPending: true, FetchResumeState: origin, Entries: []PRFeedbackEntry{}}
				if origin == "publishing" {
					comment := generalFeedback("comment:1", "Question deleted after assessment")
					assessment := assessedOutcome(comment, "no_change").PRFeedbackAssessments[0]
					entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), Assessment: &assessment, ReplyBody: assessment.Reason}
					entry.Marker = feedbackEntryMarker(j.ID, entry)
					history.Entries = []PRFeedbackEntry{entry}
					j.Outcome = testOutcome("implemented")
					j.CI = CIResult{}
					r.Provider = &feedbackScriptProvider{outcomes: []Outcome{testOutcome("reviewed")}}
				}
				j.PRFeedback = history
				j.State = "pr_feedback"
				j.ResumeState = ""
				if err := Save(dir, j); err != nil {
					t.Fatal(err)
				}
				loaded, err := Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				*j = loaded
				if err := ValidatePRFeedbackResume(*j); err != nil {
					t.Fatal(err)
				}
				r.ProcessPRFeedback = explicit
				pub.comments = nil
				before := j.Attempt
				if err := r.Run(context.Background(), dir, j, ""); err != nil {
					t.Fatal(err)
				}
				if j.State != "ready" || j.PRFeedback.FetchPending || j.PRFeedback.Active {
					t.Fatal("persisted pre-fetch checkpoint could not recover")
				}
				if origin == "ready" && j.Attempt != before {
					t.Fatal("initial empty fetch crash consumed a native turn")
				}
				if origin == "publishing" && (j.Attempt != before+1 || j.PRFeedback.CompletedHead != "" && j.PRFeedback.CompletedHead != j.Publication.HeadSHA || pub.replies != 0) {
					t.Fatal("reply refetch recovered by reusing earlier review or posting deleted feedback")
				}
			})
		}
	}
}

func TestPRFeedbackChangedCommentReplyHoldRefetchKeepsFreshReviewCycle(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	comment := generalFeedback("comment:1", "Comment deleted before reply")
	pub.comments = []PRFeedbackComment{comment}
	pub.replyErr = &FeedbackRevisionChangedError{}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change"), testOutcome("implemented"), testOutcome("reviewed")}}
	r.Provider = provider
	r.ProcessPRFeedback = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("stale reply hold=%v", err)
	}
	if !strings.HasPrefix(j.StopReason, prFeedbackReplyHold) || ValidatePRFeedbackResume(*j) != nil {
		t.Fatal("specific comment reconciliation hold cannot resume")
	}
	// A signing/identity/head failure cannot acquire this recovery permission.
	copy := *j
	copy.StopReason = "publication signing failed"
	if ValidatePRFeedbackResume(copy) == nil {
		t.Fatal("unrelated publication failure admitted")
	}
	pub.comments = nil
	pub.replyErr = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.PRFeedback.Active || j.PRFeedback.CompletedHead != j.Publication.HeadSHA || len(provider.sessions) != 3 || provider.sessions[2].Role != "review" || pub.replies != 1 {
		t.Fatal("deleted comment recovery reused old review or attempted another deleted-comment reply")
	}
	if len(j.PRFeedback.Entries) != 2 || !j.PRFeedback.Entries[1].Comment.Deleted {
		t.Fatal("deleted comment recovery discarded history")
	}
}
