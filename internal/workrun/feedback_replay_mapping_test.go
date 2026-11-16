package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackReplayMappingChecksIndividualChanges(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateGit(t, root, "config", "user.name", "Test")
	aggregateGit(t, root, "config", "user.email", "test@example.invalid")
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		aggregateGit(t, root, "add", name)
		aggregateGit(t, root, "commit", "-m", name)
	}
	write("base", "base\n")
	old := aggregateGit(t, root, "rev-parse", "HEAD")
	write("correction", "fixed\n")
	correction := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "switch", "-c", "upstream", old)
	write("upstream", "new\n")
	target := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "switch", "main")
	aggregateGit(t, root, "update-ref", "refs/sdlc/rebase/"+old+"/"+target, correction)
	aggregateGit(t, root, "rebase", "--onto", target, old)
	replay := aggregateGit(t, root, "rev-parse", "HEAD")
	helper := strings.ReplaceAll(repositoryHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
	run := func(ref string) ([]byte, error) {
		return exec.Command("python3", "-c", helper, "feedback_replay_map:"+old+":"+target+":"+ref).CombinedOutput()
	}
	data, err := run(correction)
	if err != nil {
		t.Fatalf("mapping: %v %s", err, data)
	}
	var mapping map[string]string
	if json.Unmarshal(data, &mapping) != nil || mapping[correction] != replay {
		t.Fatalf("wrong mapping %s", data)
	}
	if data, err = run(old); err == nil {
		t.Fatalf("accepted commit outside source range: %s", data)
	}
	// A changed replay must not be acknowledged as the original correction.
	if err := os.WriteFile(filepath.Join(root, "correction"), []byte("different\n"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "add", "correction")
	aggregateGit(t, root, "commit", "--amend", "--no-edit")
	if data, err = run(correction); err == nil {
		t.Fatalf("accepted different replay: %s", data)
	}
}

func TestRebasePendingFeedbackRejectsUnresolvedAndUnverified(t *testing.T) {
	_, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.Evidence = CheckEvidence{Passed: true}
	c := generalFeedback("comment:1", "example")
	a := assessedOutcome(c, "addressed").PRFeedbackAssessments[0]
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, Entries: []PRFeedbackEntry{{Comment: c, Assessment: &a}}}
	if !rebasePendingFeedbackPublication(j) {
		t.Fatal("checked assessed feedback should be eligible")
	}
	j.Evidence.Passed = false
	if rebasePendingFeedbackPublication(j) {
		t.Fatal("unverified candidate accepted")
	}
	j.Evidence.Passed = true
	j.PRFeedback.Entries[0].Assessment = nil
	if rebasePendingFeedbackPublication(j) {
		t.Fatal("unassessed feedback accepted")
	}
}

func TestFeedbackReplyUsesReplayedSignedCommit(t *testing.T) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	r.Repository = replayFeedbackRepository{fakeRepository: &fakeRepository{}}
	comment := generalFeedback("comment:1", "Correction")
	assessment := PRFeedbackAssessment{ID: comment.ID, Fingerprint: feedbackFingerprint(comment), Disposition: "addressed", Reason: "Corrected.", Commits: []string{testBase}}
	entry := PRFeedbackEntry{Comment: comment, Fingerprint: feedbackFingerprint(comment), Assessment: &assessment}
	j.Evidence.Head = testTree
	j.PRFeedback = &PRFeedbackHistory{Active: true, ReplayedCommits: map[string]string{testBase: testSigned}}
	if err := os.Mkdir(filepath.Join(dir, "publisher"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(filepath.Join(dir, "publisher", "signed-commits.json"), map[string]string{testSigned: testHead}); err != nil {
		t.Fatal(err)
	}
	if err := r.prepareFeedbackReply(context.Background(), dir, j, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.SignedCommits[testBase] != testHead {
		t.Fatal("replayed correction lost its signed publication mapping")
	}
	j.Evidence.Head = testBase
	if err := r.prepareFeedbackReply(context.Background(), dir, j, &entry); err == nil {
		t.Fatal("missing correction was acknowledged")
	}
}

// The published starting revision and checked candidate are different
// boundaries: only the latter contains the replayed correction.
type replayFeedbackRepository struct{ *fakeRepository }

func (r replayFeedbackRepository) FeedbackCommits(_ context.Context, _, _, head string) ([]string, error) {
	if head == testTree {
		return []string{testSigned}, nil
	}
	return []string{}, nil
}
