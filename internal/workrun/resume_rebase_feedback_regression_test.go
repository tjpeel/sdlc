package workrun

import (
	"context"
	"fmt"
	"testing"
)

// Publishing cannot finish after the base moves, so a checked feedback repair
// must be replayable without discarding its pending acknowledgement.
func TestResumeRebasePreservesPendingFeedbackPublication(t *testing.T) {
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.Plan.Base = "parent"
	j.Publication = Publication{Number: 40, HeadSHA: testHead, BaseSHA: testBase}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	comment := generalFeedback("comment:1", "Correction")
	assessment := assessedOutcome(comment, "addressed").PRFeedbackAssessments[0]
	assessment.Commits = []string{testHead}
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), Assessment: &assessment, ReplyBody: assessment.Reason}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	pub := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	repo := &monitoringRepository{equivalent: true}
	changed, err := (Runner{Publisher: pub, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("checked feedback repair cannot be rebased: %v", err)
	}
	if j.RebaseMonitoring != nil || j.State != "implementing" {
		t.Fatal("pending repair bypassed fresh review")
	}
	if j.PRFeedback == nil || !j.PRFeedback.Active || j.PRFeedback.Entries[0].Reply != nil {
		t.Fatal("feedback acknowledgement was discarded")
	}
}

func (r *resumeTestRepository) FeedbackReplayMapping(_ context.Context, _ string, _, _ string, commits []string) (map[string]string, error) {
	mapping := map[string]string{}
	for _, sha := range commits {
		mapping[sha] = testHead
	}
	return mapping, nil
}

func TestResumeRebaseFeedbackConflictRetainsCheckpoint(t *testing.T) {
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.Plan.Base = "parent"
	j.Publication = Publication{Number: 40, HeadSHA: testHead, BaseSHA: testBase}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	c := generalFeedback("comment:1", "Correction")
	a := assessedOutcome(c, "addressed").PRFeedbackAssessments[0]
	a.Commits = []string{testHead}
	entry := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason}
	entry.Marker = feedbackEntryMarker(j.ID, entry)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{entry}}
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	repo := &resumeTestRepository{conflict: true}
	_, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err == nil {
		t.Fatal("conflicted correction replay was accepted")
	}
	if j.Reconciliation == nil || j.Reconciliation.State != "conflict" || !j.PRFeedback.Active || !j.Evidence.Passed {
		t.Fatalf("conflict did not preserve candidate and feedback: %v reconciliation=%+v", err, j.Reconciliation)
	}
}

func TestFeedbackCorrectionCommitsFollowPreviousReplay(t *testing.T) {
	c := generalFeedback("comment:1", "Correction")
	a := assessedOutcome(c, "addressed").PRFeedbackAssessments[0]
	a.Commits = []string{testBase}
	j := &Journal{PRFeedback: &PRFeedbackHistory{Active: true, ReplayedCommits: map[string]string{testBase: testHead}, Entries: []PRFeedbackEntry{{Comment: c, Assessment: &a}}}}
	commits := feedbackCorrectionCommits(j)
	if len(commits) != 1 || commits[0] != testHead {
		t.Fatal("subsequent rebase lost prior correction mapping")
	}
}

type failingFeedbackProofRepository struct{ resumeTestRepository }

func (*failingFeedbackProofRepository) RebaseEquivalence(context.Context, string, string, string) (RebaseProof, error) {
	return RebaseProof{}, fmt.Errorf("proof unavailable")
}

func TestResumeRebaseFeedbackProofFailureRetainsPreviousMapping(t *testing.T) {
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.Plan.Base = "parent"
	j.Publication = Publication{Number: 40, HeadSHA: testHead, BaseSHA: testBase}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	c := generalFeedback("comment:1", "Correction")
	a := assessedOutcome(c, "addressed").PRFeedbackAssessments[0]
	a.Commits = []string{testBase}
	e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason}
	e.Marker = feedbackEntryMarker(j.ID, e)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, ReplayedCommits: map[string]string{testBase: testHead}, Entries: []PRFeedbackEntry{e}}
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	_, err := (Runner{Publisher: p, Repository: &failingFeedbackProofRepository{}, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err == nil || j.PRFeedback.ReplayedCommits[testBase] != testHead {
		t.Fatal("failed proof replaced retained feedback mapping")
	}
}

func (*failingFeedbackProofRepository) FeedbackReplayMapping(_ context.Context, _, _, _ string, commits []string) (map[string]string, error) {
	mapping := map[string]string{}
	for _, sha := range commits {
		mapping[sha] = testSigned
	}
	return mapping, nil
}
