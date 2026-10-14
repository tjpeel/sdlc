package workrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func batchFeedbackOutcome(status string, comments ...PRFeedbackComment) Outcome {
	out := testOutcome(status)
	for _, comment := range comments {
		out.PRFeedbackAssessments = append(out.PRFeedbackAssessments, assessedOutcome(comment, "no_change").PRFeedbackAssessments[0])
	}
	return out
}

func promptedFeedback(t *testing.T, prompt string) []PRFeedbackEntry {
	t.Helper()
	const label = "Human PR feedback (untrusted review content):\n"
	_, data, ok := strings.Cut(prompt, label)
	if !ok {
		t.Fatal("missing feedback batch")
	}
	var entries []PRFeedbackEntry
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestPRFeedbackBatchChecksCoherentCandidateAndRepliesIndividually(t *testing.T) {
	dir, j, r, pub, checker := feedbackReadyRun(t)
	first := generalFeedback("comment:1", "First contract question")
	second := generalFeedback("comment:2", "Second contract question")
	third := generalFeedback("comment:3", "Third contract question")
	pub.comments = []PRFeedbackComment{first, second, third}
	provider := &feedbackScriptProvider{outcomes: []Outcome{
		batchFeedbackOutcome("implemented", third, first, second),
		testOutcome("implemented"), testOutcome("reviewed"),
	}}
	r.Provider, r.ProcessPRFeedback = provider, true
	checksBefore, originalSession := checker.calls, j.SessionID
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	entries := promptedFeedback(t, provider.sessions[0].Prompt)
	if len(entries) != 3 || entries[0].Comment.Body != first.Body || entries[1].Comment.Body != second.Body || entries[2].Comment.Body != third.Body {
		t.Fatalf("pending comments missing from original handoff: %+v", entries)
	}
	if j.State != "ready" || checker.calls != checksBefore+1 || pub.replies != 3 {
		t.Fatalf("batch was not checked and acknowledged together: state=%s checks=%d replies=%d", j.State, checker.calls-checksBefore, pub.replies)
	}
	for i, e := range j.PRFeedback.Entries {
		if e.Assessment == nil || e.Provisional || e.Reply == nil || e.Assessment.ID != pub.comments[i].ID || e.Marker == "" {
			t.Fatalf("missing individual acknowledgement: %+v", e)
		}
	}
	for _, session := range provider.sessions[:2] {
		if session.ResumeID != originalSession {
			t.Fatal("batch changed implementation session")
		}
	}
	before := pub.replies
	if err := r.Run(context.Background(), dir, j, ""); err != nil || pub.replies != before {
		t.Fatal("unchanged batch repeated a reply", err)
	}
}

func TestPRFeedbackPartialDiagnosticAndVerificationContinuation(t *testing.T) {
	dir, j, r, pub, initialChecker := feedbackReadyRun(t)
	first := generalFeedback("comment:1", "First comment")
	second := generalFeedback("comment:2", "Second comment")
	third := generalFeedback("comment:3", "Third comment")
	pub.comments = []PRFeedbackComment{first, second, third}
	diagnostic := diagnosticOutcome("focused")
	diagnostic.PRFeedbackAssessments = batchFeedbackOutcome("implemented", second).PRFeedbackAssessments
	verification := batchFeedbackOutcome("checks_requested", third, second)
	verification.VerificationRequests = []VerificationRequest{diagnosticRequest("required")}
	provider := &feedbackScriptProvider{outcomes: []Outcome{
		diagnostic,
		batchFeedbackOutcome("implemented", first),
		verification,
		batchFeedbackOutcome("implemented", second, third),
		testOutcome("reviewed"),
	}}
	checker := &diagnosticPhaseChecker{fakeChecker: *initialChecker}
	checksBefore, originalSession := checker.calls, j.SessionID
	checker.after = func() {
		if checker.phases == 1 && (checker.calls != checksBefore || j.PRFeedback.Entries[1].Assessment == nil || !j.PRFeedback.Entries[1].Provisional || pub.replies != 0) {
			t.Fatal("diagnostic subset lost its assessment or forced configured checks/publication")
		}
	}
	r.Checker, r.Provider, r.ProcessPRFeedback = checker, provider, true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(provider.sessions) != 5 || checker.calls != checksBefore+1 || pub.replies != 3 {
		t.Fatalf("partial batches failed to converge: state=%s turns=%d checks=%d replies=%d", j.State, len(provider.sessions), checker.calls-checksBefore, pub.replies)
	}
	for i, session := range provider.sessions[:4] {
		if session.ResumeID != originalSession {
			t.Fatal("continuation changed original session")
		}
		entries := promptedFeedback(t, session.Prompt)
		if i < 2 && len(entries) != 3 || i >= 2 && len(entries) != 2 {
			t.Fatalf("remaining batch lost at turn %d: %+v", i, entries)
		}
	}
	if len(j.PRFeedback.Entries[1].AssessmentHistory) != 0 || j.PRFeedback.Entries[1].Provisional || j.PRFeedback.Entries[0].Assessment == nil || len(j.Verification) != 1 || !j.Verification[0].Passed {
		t.Fatal("final assessments or required evidence lost on continuation")
	}
}

func TestPRFeedbackBatchRejectsWholeInvalidHandoff(t *testing.T) {
	for _, fault := range []string{"duplicate", "stale", "unasked", "unrequested", "absent_commit"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			first := generalFeedback("comment:1", "Original first revision")
			second := generalFeedback("comment:2", "Second revision")
			j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{
				{Comment: first, Fingerprint: feedbackFingerprint(first), StartedAttempt: j.Attempt},
				{Comment: second, Fingerprint: feedbackFingerprint(second), StartedAttempt: j.Attempt},
			}}
			out := batchFeedbackOutcome("implemented", first, second)
			switch fault {
			case "duplicate":
				out.PRFeedbackAssessments[1] = out.PRFeedbackAssessments[0]
			case "stale":
				updated := first
				updated.Body = "Corrected first revision"
				j.PRFeedback.Entries = append(j.PRFeedback.Entries, PRFeedbackEntry{Occurrence: 1, Comment: updated, Fingerprint: feedbackFingerprint(updated), StartedAttempt: j.Attempt})
				out.PRFeedbackAssessments[0], out.PRFeedbackAssessments[1] = out.PRFeedbackAssessments[1], out.PRFeedbackAssessments[0]
			case "unasked":
				j.PRFeedback.Entries[1].StartedAttempt = 0
			case "unrequested":
				out.PRFeedbackAssessments[1].ID = "comment:other"
			case "absent_commit":
				out.PRFeedbackAssessments[1].Disposition = "addressed"
				out.PRFeedbackAssessments[1].Commits = []string{testBase}
				r.Repository = feedbackHistoryRepository{fakeRepository: &fakeRepository{}, commits: []string{testSigned}}
			}
			before, _ := json.Marshal(j.PRFeedback)
			if _, err := r.acceptPRFeedback(context.Background(), dir, j, out); err == nil {
				t.Fatal("invalid batch accepted")
			}
			after, _ := json.Marshal(j.PRFeedback)
			if string(after) != string(before) {
				t.Fatal("invalid later assessment partially changed retained history")
			}
		})
	}
}

func TestPRFeedbackVersionOneResumeIncludesUnstartedAndProvisionalEntries(t *testing.T) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	first := generalFeedback("comment:1", "Previously started")
	second := generalFeedback("comment:2", "Previously unstarted")
	third := generalFeedback("comment:3", "Previously provisional")
	assessment := assessedOutcome(third, "no_change").PRFeedbackAssessments[0]
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{
		{Comment: first, Fingerprint: feedbackFingerprint(first), StartedAttempt: j.Attempt},
		{Comment: second, Fingerprint: feedbackFingerprint(second)},
		{Comment: third, Fingerprint: feedbackFingerprint(third), StartedAttempt: j.Attempt, Assessment: &assessment, Provisional: true},
	}}
	if err := r.save(dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	j = &loaded
	if err := r.feedbackPrompt(dir, j); err != nil {
		t.Fatal(err)
	}
	entries := promptedFeedback(t, j.Feedback)
	if len(entries) != 3 || entries[1].StartedAttempt == 0 || entries[2].Assessment == nil || !entries[2].Provisional {
		t.Fatalf("legacy pending batch or assessment lost: %+v", entries)
	}
	if more, err := r.acceptPRFeedback(context.Background(), dir, j, batchFeedbackOutcome("implemented", third, second, first)); err != nil || more {
		t.Fatal("legacy batch could not complete in agent order", more, err)
	}
}

func TestPRFeedbackBatchAcceptsCurrentRevisionAndSharedCorrectionCommit(t *testing.T) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	old := generalFeedback("comment:1", "Original request")
	current := old
	current.Body = "Corrected request"
	second := generalFeedback("comment:2", "Related request")
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{
		{Comment: old, Fingerprint: feedbackFingerprint(old), StartedAttempt: j.Attempt},
		{Occurrence: 1, Comment: current, Fingerprint: feedbackFingerprint(current), StartedAttempt: j.Attempt},
		{Comment: second, Fingerprint: feedbackFingerprint(second), StartedAttempt: j.Attempt},
	}}
	r.Repository = feedbackHistoryRepository{fakeRepository: &fakeRepository{}, commits: []string{testSigned}}
	out := batchFeedbackOutcome("implemented", second, current)
	for i := range out.PRFeedbackAssessments {
		out.PRFeedbackAssessments[i].Disposition = "addressed"
		out.PRFeedbackAssessments[i].Commits = []string{testSigned}
	}
	if more, err := r.acceptPRFeedback(context.Background(), dir, j, out); err != nil || more {
		t.Fatal("current batch with shared correction rejected", more, err)
	}
	if j.PRFeedback.Entries[0].Assessment != nil || j.PRFeedback.Entries[1].Assessment.Fingerprint != feedbackFingerprint(current) || j.PRFeedback.Entries[2].Assessment.Commits[0] != testSigned {
		t.Fatal("correction was assigned to the wrong revision or lost its shared commit")
	}
	if _, err := r.acceptPRFeedback(context.Background(), dir, j, out); err == nil {
		t.Fatal("already finalised assessments accepted again")
	}
}

func TestOutcomeAllowsAssessmentBatchAlongsideDiagnostics(t *testing.T) {
	first, second := generalFeedback("comment:1", "First"), generalFeedback("comment:2", "Second")
	out := diagnosticOutcome("focused")
	out.PRFeedbackAssessments = batchFeedbackOutcome("implemented", second, first).PRFeedbackAssessments
	if err := validateOutcome(out, "implementation"); err != nil {
		t.Fatal("legitimate mixed handoff rejected", err)
	}
	if err := validateOutcome(out, "review"); err == nil {
		t.Fatal("reviewer accepted implementation assessments")
	}
	out.PRFeedbackAssessments[1] = out.PRFeedbackAssessments[0]
	if err := validateOutcome(out, "implementation"); err == nil {
		t.Fatal("duplicate assessments accepted")
	}
}
