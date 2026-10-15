package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type localFeedbackPublisher struct {
	*feedbackPublisherFake
	boundaries  int
	boundaryErr error
}

func (p *localFeedbackPublisher) ValidateFeedbackBoundary(context.Context, Plan, Publication) error {
	p.boundaries++
	return p.boundaryErr
}

type localAssessmentProvider struct {
	j        *Journal
	sessions []Session
}

func (p *localAssessmentProvider) Status(context.Context, string) (string, error) {
	return "stored", nil
}
func (p *localAssessmentProvider) Execute(_ context.Context, s Session, _, _ io.Writer) (SessionResult, error) {
	p.sessions = append(p.sessions, s)
	out := testOutcome("reviewed")
	if s.Role != "review" {
		out = testOutcome("implemented")
		if entry := pendingFeedback(p.j); entry != nil {
			out = assessedOutcome(entry.Comment, "no_change")
		}
	}
	return SessionResult{Outcome: out, SessionID: testNative, ReportedModel: s.Model.Name}, nil
}

func TestLocalFeedbackUsesOriginalSessionAndChecksWithoutGitHubReplies(t *testing.T) {
	dir, j, r, pub, checks := feedbackReadyRun(t)
	original := j.SessionID
	p := &localFeedbackPublisher{feedbackPublisherFake: pub}
	provider := &localAssessmentProvider{j: j}
	r.Publisher, r.Provider, r.ProcessPRFeedback = p, provider, true
	r.LocalPRFeedback = strings.Repeat("Please check the edge case.\n", 400) // larger than a GitHub feedback packet's per-comment bound
	text := r.LocalPRFeedback
	before := j.Attempt
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.PRFeedback.Active || checks.calls != 2 || len(provider.sessions) != 3 || j.Attempt != before+3 {
		t.Fatal("feedback bypassed checks or fresh review", j.State, checks.calls, len(provider.sessions))
	}
	if p.boundaries != 1 || pub.reads != 0 || pub.replies != 0 {
		t.Fatal("local feedback read or posted comments", p.boundaries, pub.reads, pub.replies)
	}
	if provider.sessions[0].ResumeID != original || provider.sessions[1].ResumeID != original || provider.sessions[2].Role != "review" || provider.sessions[2].ResumeID != "" {
		t.Fatal("lost session or independent review")
	}
	entry := j.PRFeedback.Entries[0]
	if entry.Comment.Kind != "local" || entry.Comment.Body != text || entry.Assessment == nil || entry.Reply == nil || entry.Reply.URL != "" {
		t.Fatal("private feedback or assessment missing")
	}
	files, err := filepath.Glob(filepath.Join(dir, "feedback", "*.txt"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != text {
		t.Fatal("source copy differs", err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.PRFeedback.Entries[0].Comment.Body != text {
		t.Fatal("feedback history did not survive load", err)
	}
	// A subsequent GitHub pass must not tombstone the local source or repeat work.
	r.LocalPRFeedback = ""
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(j.PRFeedback.Entries) != 1 || j.PRFeedback.Entries[0].Comment.Deleted || len(provider.sessions) != 3 {
		t.Fatal("GitHub refresh invalidated local feedback")
	}
}

func TestLocalFeedbackBoundaryFailureRetainsInputForOrdinaryResume(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	p := &localFeedbackPublisher{feedbackPublisherFake: pub, boundaryErr: errors.New("PR head changed")}
	provider := &localAssessmentProvider{j: j}
	r.Publisher, r.Provider, r.ProcessPRFeedback, r.LocalPRFeedback = p, provider, true, "Check the return value."
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(provider.sessions) != 0 || j.PRFeedback.LocalPending == nil || !j.PRFeedback.FetchPending {
		t.Fatal("dispatched without boundary or lost input")
	}
	var err error
	loaded, loadErr := Load(dir)
	err = loadErr
	j = &loaded
	if err != nil {
		t.Fatal(err)
	}
	provider.j = j
	r.ProcessPRFeedback, r.LocalPRFeedback, p.boundaryErr = false, "", nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(j.PRFeedback.Entries) != 1 || len(provider.sessions) != 3 || pub.replies != 0 {
		t.Fatal("ordinary resume lost or duplicated feedback")
	}
}

func TestLocalFeedbackStorageRejectsUnsafeOrChangedFiles(t *testing.T) {
	dir := t.TempDir()
	path, err := StoreLocalPRFeedback(dir, "Private feedback\n")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	parent, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || parent.Mode().Perm() != 0700 {
		t.Fatal("feedback is not private")
	}
	again, err := StoreLocalPRFeedback(dir, "Private feedback\n")
	if err != nil || again != path {
		t.Fatal("idempotent storage failed", err)
	}
	if err := os.WriteFile(path, []byte("Changed feedback\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreLocalPRFeedback(dir, "Private feedback\n"); err == nil {
		t.Fatal("changed feedback accepted")
	}
	other := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(other, "feedback")); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreLocalPRFeedback(other, "Private feedback\n"); err == nil {
		t.Fatal("symlink history accepted")
	}
}

func TestLocalFeedbackCannotReplaceSelectedFetchInput(t *testing.T) {
	for _, pendingLocal := range []bool{false, true} {
		t.Run(map[bool]string{false: "GitHub", true: "local"}[pendingLocal], func(t *testing.T) {
			dir, j, r, pub, _ := feedbackReadyRun(t)
			p := &localFeedbackPublisher{feedbackPublisherFake: pub, boundaryErr: errors.New("temporary boundary failure")}
			pub.readErr = errors.New("temporary read failure")
			r.Publisher, r.ProcessPRFeedback = p, true
			if pendingLocal {
				r.LocalPRFeedback = "First local selection"
			}
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			r.LocalPRFeedback = "Replacement selection"
			p.boundaryErr, pub.readErr = nil, nil
			before := j.Attempt
			if err := r.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("pending feedback was replaced")
			}
			if j.Attempt != before || !j.PRFeedback.FetchPending {
				t.Fatal("replacement started work or lost pending fetch")
			}
			if pendingLocal && j.PRFeedback.LocalPending.Body != "First local selection" {
				t.Fatal("selected input changed")
			}
		})
	}
}
