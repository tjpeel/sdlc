package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These adapters exercise real disposable Git history offline. The publisher
// reconstructs commits with a distinct test identity to stand in for signing;
// cryptographic signing remains covered by the existing publication tests.
type feedbackGitRepository struct{ t *testing.T }

func (r feedbackGitRepository) git(root string, args ...string) string {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	data, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("disposable git %v: %s: %v", args, data, err)
	}
	return strings.TrimSpace(string(data))
}
func (r feedbackGitRepository) Inspect(_ context.Context, root string) (Revision, error) {
	return Revision{Head: r.git(root, "rev-parse", "HEAD"), Tree: r.git(root, "rev-parse", "HEAD^{tree}"), Clean: r.git(root, "status", "--porcelain") == ""}, nil
}
func (r feedbackGitRepository) Bundle(_ context.Context, root, path string) error {
	r.git(root, "bundle", "create", path, "HEAD")
	return nil
}
func (r feedbackGitRepository) FeedbackCommits(_ context.Context, root, start, head string) ([]string, error) {
	r.git(root, "merge-base", "--is-ancestor", start, head)
	return strings.Fields(r.git(root, "rev-list", "--reverse", "--first-parent", start+".."+head)), nil
}

type feedbackGitSigningPublisher struct {
	feedbackPublisherFake
	repo        feedbackGitRepository
	published   int
	replyBodies []string
}

func (p *feedbackGitSigningPublisher) Publish(_ context.Context, plan Plan, workspace, dir string, _ Publication, _ io.Writer) (Publication, error) {
	mapping := map[string]string{}
	parent := plan.SourceSHA
	for _, original := range strings.Fields(p.repo.git(workspace, "rev-list", "--reverse", "--first-parent", plan.SourceSHA+"..HEAD")) {
		tree := p.repo.git(workspace, "show", "-s", "--format=%T", original)
		message := p.repo.git(workspace, "show", "-s", "--format=%B", original)
		signed := p.repo.git(workspace, "-c", "user.name=Test Signing Controller", "-c", "user.email=controller@example.invalid", "commit-tree", tree, "-p", parent, "-m", message)
		mapping[original] = signed
		parent = signed
	}
	if err := os.MkdirAll(filepath.Join(dir, "publisher"), 0700); err != nil {
		return Publication{}, err
	}
	if err := saveJSON(filepath.Join(dir, "publisher", "signed-commits.json"), mapping); err != nil {
		return Publication{}, err
	}
	p.published++
	return Publication{URL: fmt.Sprintf("https://github.com/%s/pull/1", plan.Repository), Number: 1, BaseSHA: plan.BaseSHA, HeadSHA: parent}, nil
}
func (p *feedbackGitSigningPublisher) ReplyFeedback(_ context.Context, _ Plan, _ Publication, _ PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	p.replyBodies = append(p.replyBodies, body)
	return PRFeedbackReply{ID: marker, URL: "https://github.com/example/project/pull/1#issuecomment-99"}, nil
}

type feedbackCorrectionProvider struct {
	feedbackScriptProvider
	mutation  func()
	mutated   bool
	interrupt bool
}

func (p *feedbackCorrectionProvider) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	if s.Role == "implementation" && !p.mutated {
		p.mutated = true
		p.mutation()
		if p.interrupt {
			p.interrupt = false
			p.sessions = append(p.sessions, s)
			return SessionResult{SessionID: testNative}, errors.New("disposable native turn interrupted after commit")
		}
	}
	return p.feedbackScriptProvider.Execute(ctx, s, out, diag)
}

func TestPRFeedbackRealCorrectionAndInterruptedResumePreserveCommitAndSignedReply(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%v", interrupt), func(t *testing.T) {
			dir, j := testRun(t)
			repo := feedbackGitRepository{t: t}
			repo.git(j.Workspace, "init")
			repo.git(j.Workspace, "config", "user.name", "Test Candidate")
			repo.git(j.Workspace, "config", "user.email", "candidate@example.invalid")
			file := filepath.Join(j.Workspace, "api.go")
			if err := os.WriteFile(file, []byte("original\n"), 0600); err != nil {
				t.Fatal(err)
			}
			repo.git(j.Workspace, "add", "api.go")
			repo.git(j.Workspace, "commit", "-m", "Base fixture")
			j.Plan.SourceSHA = repo.git(j.Workspace, "rev-parse", "HEAD")
			j.Plan.StartingSHA = j.Plan.SourceSHA
			if err := os.WriteFile(file, []byte("ticket implementation\n"), 0600); err != nil {
				t.Fatal(err)
			}
			repo.git(j.Workspace, "add", "api.go")
			repo.git(j.Workspace, "commit", "-m", "Implement selected ticket")
			pub := &feedbackGitSigningPublisher{feedbackPublisherFake: feedbackPublisherFake{fakePublisher: &fakePublisher{}}, repo: repo}
			r := fakeRunner(&fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}, &fakeChecker{}, pub.fakePublisher, &fakeRepository{})
			r.Repository = repo
			r.Publisher = pub
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			originalCandidate := repo.git(j.Workspace, "rev-parse", "HEAD")
			originalSession := j.SessionID
			comment := generalFeedback("comment:1", "The response must include the requested correction")
			pub.comments = []PRFeedbackComment{comment}
			provider := &feedbackCorrectionProvider{interrupt: interrupt}
			provider.mutation = func() {
				if err := os.WriteFile(file, []byte("ticket implementation with correction\n"), 0600); err != nil {
					t.Fatal(err)
				}
				repo.git(j.Workspace, "add", "api.go")
				repo.git(j.Workspace, "commit", "-m", "Correct response for human feedback")
				correction := repo.git(j.Workspace, "rev-parse", "HEAD")
				assessed := assessedOutcome(comment, "addressed")
				assessed.PRFeedbackAssessments[0].Commits = []string{correction}
				assessed.PRFeedbackAssessments[0].Reason = "Added the requested correction."
				provider.outcomes = []Outcome{assessed, testOutcome("implemented"), testOutcome("reviewed")}
			}
			r.Provider = provider
			r.ProcessPRFeedback = true
			err := r.Run(context.Background(), dir, j, "")
			if interrupt {
				if !errors.Is(err, ErrStopped) || j.State != "failed" || j.ResumeState != "implementing" {
					t.Fatalf("interruption=%v state=%s resume=%s", err, j.State, j.ResumeState)
				}
				if ValidatePRFeedbackResume(*j) == nil {
					t.Fatal("feedback action bypassed failed implementation")
				}
				loaded, loadErr := Load(dir)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				*j = loaded
				r.ProcessPRFeedback = false
				err = r.Run(context.Background(), dir, j, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			correction := repo.git(j.Workspace, "rev-parse", "HEAD")
			if len(strings.Fields(repo.git(j.Workspace, "rev-list", originalCandidate+"..HEAD"))) != 1 {
				t.Fatal("interrupted correction was duplicated")
			}
			entry := j.PRFeedback.Entries[0]
			signed := entry.SignedCommits[correction]
			if signed == "" || signed == correction || signed != j.Publication.HeadSHA || entry.Reply == nil || len(pub.replyBodies) != 1 || !strings.Contains(pub.replyBodies[0], signed) || strings.Contains(pub.replyBodies[0], correction) {
				t.Fatalf("published correction linkage lost: %+v replies=%v", entry, pub.replyBodies)
			}
			if repo.git(j.Workspace, "show", "-s", "--format=%T", signed) != repo.git(j.Workspace, "show", "-s", "--format=%T", correction) {
				t.Fatal("mapped publication tree changed")
			}
			if j.State != "ready" || j.SessionID != originalSession || j.PRFeedback.Active {
				t.Fatal("correction did not complete the original cycle")
			}
			for _, session := range provider.sessions {
				if session.Role == "implementation" && session.ResumeID != originalSession {
					t.Fatal("correction forked implementation session")
				}
			}
			last := provider.sessions[len(provider.sessions)-1]
			if last.Role != "review" || last.ResumeID != "" || !strings.Contains(last.Prompt, signed) || !strings.Contains(last.Prompt, comment.Body) {
				t.Fatal("fresh independent review omitted signed correction/human feedback")
			}
		})
	}
}
