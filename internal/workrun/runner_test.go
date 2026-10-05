package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBase = "1111111111111111111111111111111111111111"
const testHead = "2222222222222222222222222222222222222222"
const testTree = "3333333333333333333333333333333333333333"
const testNative = "00000000-0000-4000-8000-000000000001"

func testOutcome(status string) Outcome {
	return Outcome{Status: status, Summary: "Selected work complete", Questions: []string{}, Findings: []Finding{}, Limitations: []string{}, LocalReview: true, PRTitle: "TASK-1 selected work", PRBody: "Implements TASK-1."}
}
func testRun(t *testing.T) (string, *Journal) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "run-one")
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(directory, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return directory, &Journal{Version: 1, ID: "run-one", State: "prepared", Workspace: workspace, Plan: Plan{StartingSHA: testBase, SourceSHA: testBase, BaseSHA: testBase, Reference: "TASK-1", Repository: "example/project", Branch: "TASK-1", Base: "main", Roles: DefaultModels().Codex, Checks: [][]string{{"go", "test", "./..."}}}}
}

type fakeProvider struct {
	status          map[string]string
	statusErr       error
	calls           []Session
	outcomes        []Outcome
	executeErr      error
	resultSessionID string
}

func (p *fakeProvider) Status(_ context.Context, name string) (string, error) {
	if p.statusErr != nil {
		return "", p.statusErr
	}
	if v, ok := p.status[name]; ok {
		return v, nil
	}
	return "stored", nil
}
func (p *fakeProvider) Execute(_ context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	p.calls = append(p.calls, s)
	id := p.resultSessionID
	if id == "" {
		id = testNative
	}
	if p.executeErr != nil {
		return SessionResult{SessionID: id}, p.executeErr
	}
	if len(p.outcomes) == 0 {
		return SessionResult{}, errors.New("unexpected session")
	}
	o := p.outcomes[0]
	p.outcomes = p.outcomes[1:]
	io.WriteString(out, "fake streamed event\n")
	return SessionResult{Outcome: o, SessionID: id, ReportedModel: s.Model.Name}, nil
}

type fakeChecker struct {
	calls    int
	failures int
	err      error
}

func (c *fakeChecker) Check(_ context.Context, _ string, _ [][]string, out io.Writer) error {
	c.calls++
	io.WriteString(out, "offline check\n")
	if c.err != nil {
		return c.err
	}
	if c.calls <= c.failures {
		return ErrCheckFailed
	}
	return nil
}

type fakeRepository struct {
	calls     int
	revisions []Revision
	bundles   int
}

func (r *fakeRepository) Inspect(context.Context, string) (Revision, error) {
	r.calls++
	if len(r.revisions) > 0 {
		v := r.revisions[0]
		if len(r.revisions) > 1 {
			r.revisions = r.revisions[1:]
		}
		return v, nil
	}
	return Revision{Head: testHead, Tree: testTree, Clean: true}, nil
}
func (r *fakeRepository) Bundle(context.Context, string, string) error { r.bundles++; return nil }

type fakePublisher struct {
	statuses []string
	calls    int
	ci       int
	failures int
	err      error
	ciErr    error
	previous []Publication
}

func (p *fakePublisher) Publish(_ context.Context, _ Plan, _, _ string, previous Publication, _ io.Writer) (Publication, error) {
	p.calls++
	p.previous = append(p.previous, previous)
	if p.err != nil {
		return Publication{}, p.err
	}
	return Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}, nil
}
func (p *fakePublisher) Checks(context.Context, Plan, Publication) (CIResult, error) {
	p.ci++
	if p.ciErr != nil {
		return CIResult{}, p.ciErr
	}
	if len(p.statuses) > 0 {
		status := p.statuses[0]
		if len(p.statuses) > 1 {
			p.statuses = p.statuses[1:]
		}
		return CIResult{Status: status}, nil
	}
	if p.ci <= p.failures {
		return CIResult{Status: "failed", Details: "required test failed"}, nil
	}
	return CIResult{Status: "passed"}, nil
}
func fakeRunner(p *fakeProvider, c *fakeChecker, pub *fakePublisher, repo *fakeRepository) Runner {
	return Runner{Provider: p, Checker: c, Publisher: pub, Repository: repo, Output: io.Discard, PollInterval: time.Millisecond, ReviewWorkspace: func(_ context.Context, j Journal, _ string) (string, error) {
		return filepath.Join(j.Workspace, "fresh-review"), nil
	}}
}
func TestRunnerLifecycleAndNativeSession(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	c := &fakeChecker{}
	pub := &fakePublisher{}
	repo := &fakeRepository{}
	r := fakeRunner(p, c, pub, repo)
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || c.calls != 1 || pub.calls != 1 || len(p.calls) != 3 {
		t.Fatalf("unexpected lifecycle: %+v checks=%d publish=%d sessions=%d", j, c.calls, pub.calls, len(p.calls))
	}
	if p.calls[0].ResumeID != "" || p.calls[1].ResumeID != testNative || p.calls[2].ResumeID != "" {
		t.Fatal("native implementation was not resumed or review inherited its session")
	}
	if p.calls[0].Directory != p.calls[1].Directory || p.calls[2].Directory == p.calls[0].Directory || p.calls[2].Workspace == j.Workspace {
		t.Fatal("session storage/review checkout not isolated")
	}
	if !strings.Contains(p.calls[1].Prompt, "Controller verification evidence") || p.calls[1].Model != j.Plan.Roles.Implementation || p.calls[2].Model != j.Plan.Roles.Review {
		t.Fatal("evidence or role settings lost")
	}
	loaded, err := Load(dir)
	if err != nil || loaded.State != "ready" || loaded.SessionID != testNative {
		t.Fatalf("checkpoint: %+v %v", loaded, err)
	}
	if loaded.Timings.Recorded() == nil {
		t.Fatal("controller timings were not saved at completion")
	}
}
func TestRunnerImplementerAuthenticationStopsBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		err     error
		message string
	}{{"missing", "missing", nil, "stored login"}, {"operational", "", errors.New("credential store unavailable"), "credential store unavailable"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, j := testRun(t)
			p := &fakeProvider{status: map[string]string{"codex": tc.status}, statusErr: tc.err}
			pub := &fakePublisher{}
			r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
			err := r.Run(context.Background(), dir, j, "")
			if !errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), tc.message) || len(p.calls) != 0 || pub.calls != 0 {
				t.Fatalf("authentication stop: %v", err)
			}
		})
	}
}
func TestRunnerMissingReviewerResumesWithoutImplementation(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{status: map[string]string{"claude": "missing"}, outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	pub := &fakePublisher{}
	c := &fakeChecker{}
	r := fakeRunner(p, c, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "awaiting_reviewer" {
		t.Fatalf("%s %v", j.State, err)
	}
	p.status["claude"] = "stored"
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 2 || pub.calls != 1 || c.calls != 1 {
		t.Fatal("resume repeated implementation/publication/checks")
	}
}
func TestRunnerReviewFindingsRepairSameSession(t *testing.T) {
	defaults := DefaultModels()
	for _, roles := range []Roles{defaults.Codex, defaults.Claude} {
		t.Run(roles.Implementation.Provider, func(t *testing.T) {
			dir, j := testRun(t)
			j.Plan.Roles = roles
			review := testOutcome("reviewed")
			review.Findings = []Finding{{Priority: "P1", Path: "app.go", Line: 1, Scenario: "nil input panics", Recommendation: "guard nil input"}}
			p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), review, testOutcome("implemented"), testOutcome("reviewed")}}
			pub := &fakePublisher{}
			checks := &fakeChecker{}
			r := fakeRunner(p, checks, pub, &fakeRepository{})
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			// This fixture keeps the tree unchanged, so its passing checks remain valid.
			if len(p.calls) != 4 || pub.calls != 2 || pub.ci < 2 || checks.calls != 1 {
				t.Fatalf("unexpected repair stages: sessions=%d publications=%d CI=%d checks=%d", len(p.calls), pub.calls, pub.ci, checks.calls)
			}
			for i, expected := range []Model{roles.Implementation, roles.Review, roles.Implementation, roles.Review} {
				if p.calls[i].Model != expected {
					t.Fatalf("stage %d used the wrong provider, model or reasoning: %+v", i, p.calls[i].Model)
				}
			}
			if p.calls[2].ResumeID != testNative || !strings.Contains(p.calls[2].Prompt, "nil input panics") || p.calls[1].ResumeID != "" || p.calls[3].ResumeID != "" || p.calls[1].Directory == p.calls[3].Directory || pub.previous[1].Number != 1 {
				t.Fatal("repair lost session, findings, fresh review, or existing PR")
			}
		})
	}
}
func TestRunnerHumanQuestionRequiresAnswer(t *testing.T) {
	dir, j := testRun(t)
	waiting := testOutcome("waiting_for_human")
	waiting.Questions = []string{"Which behavior is required?"}
	p := &fakeProvider{outcomes: []Outcome{waiting, testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 1 {
		t.Fatal("ran without answer")
	}
	if err := r.Run(context.Background(), dir, j, "Reject nil input"); err != nil {
		t.Fatal(err)
	}
	if p.calls[1].ResumeID != testNative || !strings.Contains(p.calls[1].Prompt, "Reject nil input") {
		t.Fatal("answer/session lost")
	}
}
func TestRunnerCheckRepairBound(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("checks_requested")}}
	pub := &fakePublisher{}
	c := &fakeChecker{failures: 10}
	r := fakeRunner(p, c, pub, &fakeRepository{})
	r.MaxRounds = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || c.calls != 2 || pub.calls != 0 {
		t.Fatalf("bound not enforced: %v %+v", err, j)
	}
}
func TestRunnerPublicationTreeMustMatchChecks(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented")}}
	pub := &fakePublisher{}
	repo := &fakeRepository{revisions: []Revision{{testHead, testTree, true}, {testHead, testBase, true}}}
	r := fakeRunner(p, &fakeChecker{}, pub, repo)
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || pub.calls != 0 || j.ResumeState != "publishing" {
		t.Fatalf("changed tree published: %v", err)
	}
}
func TestRunnerPublicationRetryPreservesImplementation(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	pub := &fakePublisher{err: errors.New("signing unavailable")}
	c := &fakeChecker{}
	r := fakeRunner(p, c, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	pub.err = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 2 || c.calls != 1 {
		t.Fatal("retry reimplemented or repeated checks")
	}
}

func TestRunnerOperationalCheckFailureResumesWithNewLog(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	c := &fakeChecker{err: errors.New("check worker unavailable")}
	pub := &fakePublisher{}
	r := fakeRunner(p, c, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.ResumeState != "checking" {
		t.Fatalf("operational failure: %v", err)
	}
	c.err = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatalf("resume could not create new check log: %v", err)
	}
	if c.calls != 2 || len(p.calls) != 2 || pub.calls != 1 {
		t.Fatal("operational retry repeated implementation or skipped checks")
	}
	for _, name := range []string{"checks-1.log", "checks-2.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRunnerChangedCommittedTreeInvalidatesPriorEvidence(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	c := &fakeChecker{}
	repo := &fakeRepository{revisions: []Revision{{testHead, testTree, true}, {testHead, testBase, true}, {testHead, testBase, true}}}
	r := fakeRunner(p, c, &fakePublisher{}, repo)
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 || j.Evidence.Tree != testBase {
		t.Fatalf("stale tree reused: %+v", j.Evidence)
	}
}
func TestRunnerCommitAmendRequiresFreshChecksEvenWithSameTree(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	c := &fakeChecker{}
	repo := &fakeRepository{revisions: []Revision{{testHead, testTree, true}, {testSigned, testTree, true}, {testSigned, testTree, true}}}
	r := fakeRunner(p, c, &fakePublisher{}, repo)
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 || j.Evidence.Head != testSigned {
		t.Fatalf("amended commit reused stale evidence: %+v", j.Evidence)
	}
}
func TestRunnerClaudeImplementationUsesCodexReview(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.Roles = DefaultModels().Claude
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if p.calls[0].Model.Provider != "claude" || p.calls[1].Model.Provider != "codex" {
		t.Fatal("opposite provider not used for Claude implementation")
	}
}

func TestRunnerProviderFailureRetainsNativeSessionForResume(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{executeErr: errors.New("native client usage limit")}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "failed" || j.SessionID != testNative || pub.calls != 0 {
		t.Fatalf("failure lost resumable session: %v %+v", err, j)
	}
	p.executeErr = nil
	p.outcomes = []Outcome{testOutcome("implemented"), testOutcome("reviewed")}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if p.calls[1].ResumeID != testNative {
		t.Fatal("provider restart created a new implementation session")
	}
}
func TestRunnerCIFailureBoundStopsRepair(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("implemented")}}
	pub := &fakePublisher{failures: 100}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	r.MaxRounds = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ci" || pub.calls != 2 || len(p.calls) != 2 {
		t.Fatalf("CI repair not bounded: %v %+v", err, j)
	}
}
func TestRunnerReviewFailureBoundStopsRepair(t *testing.T) {
	dir, j := testRun(t)
	finding := testOutcome("reviewed")
	finding.Findings = []Finding{{Priority: "P1", Path: "app.go", Line: 1, Scenario: "bad input crashes", Recommendation: "handle bad input"}}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), finding, testOutcome("implemented"), finding}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.MaxRounds = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || j.Rounds != 2 || len(p.calls) != 4 {
		t.Fatalf("review repair not bounded: %v %+v", err, j)
	}
}
func TestRunnerUncommittedCandidateCannotCheckOrPublish(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested")}}
	c := &fakeChecker{}
	pub := &fakePublisher{}
	repo := &fakeRepository{revisions: []Revision{{testHead, testTree, false}}}
	r := fakeRunner(p, c, pub, repo)
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || c.calls != 0 || pub.calls != 0 {
		t.Fatal("dirty candidate reached verification/publication")
	}
}

func TestRunnerReadyResumeRevalidatesPublishedBoundary(t *testing.T) {
	for _, kind := range []string{"unchanged", "failed CI", "changed PR", "unexpected answer", "nil output"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := testRun(t)
			j.State = "ready"
			j.SessionID = testNative
			j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
			p := &fakeProvider{}
			pub := &fakePublisher{}
			repo := &fakeRepository{}
			r := fakeRunner(p, &fakeChecker{}, pub, repo)
			answer := ""
			switch kind {
			case "failed CI":
				pub.failures = 1
			case "changed PR":
				pub.ciErr = errors.New("PR head changed")
			case "unexpected answer":
				answer = "unsolicited answer"
			case "nil output":
				r.Output = nil
			}
			err := r.Run(context.Background(), dir, j, answer)
			switch kind {
			case "unchanged", "nil output":
				if err != nil || j.State != "ready" || pub.ci != 1 {
					t.Fatalf("ready boundary not validated: %v %+v", err, j)
				}
			case "unexpected answer":
				if err == nil || pub.ci != 0 || j.State != "ready" {
					t.Fatal("ready run accepted answer with no pending question")
				}
			default:
				if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ready" {
					t.Fatalf("stale ready state approved: %v %+v", err, j)
				}
			}
			if len(p.calls) != 0 || pub.calls != 0 || repo.calls != 0 {
				t.Fatal("ready resume repeated execution/publication")
			}
		})
	}
}
func TestRunnerReadyResumeRejectsChangedCapturedInput(t *testing.T) {
	dir, j := testRun(t)
	j.State = "ready"
	original := []byte("Selected requirements\n")
	hash := sha256.Sum256(original)
	j.Plan.Inputs = []Input{{Path: "requirements.md", SHA256: hex.EncodeToString(hash[:])}}
	if err := os.WriteFile(filepath.Join(j.Workspace, "requirements.md"), []byte("Changed requirements\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || pub.ci != 0 || len(p.calls) != 0 {
		t.Fatalf("changed requirements approved: %v %+v", err, j)
	}
}
func TestPrepareReviewRetryUsesFreshSnapshotAtSameAttempt(t *testing.T) {
	dir, j := testRun(t)
	ctx := context.Background()
	local := filepath.Join(dir, "publication.git")
	git := func(directory string, args ...string) string {
		t.Helper()
		output, err := isolatedGit(ctx, directory, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output)
	}
	git(dir, "init", "--bare", "--template=", local)
	tree := git(local, "mktree")
	head := git(local, "-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit-tree", tree, "-m", "Create disposable review fixture")
	git(local, "update-ref", "refs/heads/publication", head)
	j.Publication.HeadSHA = head
	j.Attempt = 3
	data := []byte("Selected requirements\n")
	if err := os.WriteFile(filepath.Join(j.Workspace, "requirements.md"), data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	j.Plan.Inputs = []Input{{Path: "requirements.md", SHA256: hex.EncodeToString(hash[:])}}
	for path, content := range map[string]string{
		filepath.Join(j.Workspace, ".env"): "fake-private-check-input",
		filepath.Join(dir, "checks-1.log"): "fake-private-check-diagnostic",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j.Plan.CheckInputs = []Input{{Path: ".env", SHA256: strings.Repeat("a", 64)}}
	j.Evidence = CheckEvidence{Head: head, Tree: tree, Passed: true, Log: "checks-1.log"}
	first, err := PrepareReview(ctx, *j, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareReview(ctx, *j, dir)
	if err != nil {
		t.Fatalf("retry collided with interrupted review snapshot: %v", err)
	}
	if first == second || j.Attempt != 3 {
		t.Fatal("retry reused checkout or changed session attempt")
	}
	for _, workspace := range []string{first, second} {
		if !strings.HasPrefix(filepath.Base(workspace), "review-source-4-") || git(workspace, "rev-parse", "HEAD") != head {
			t.Fatal("snapshot lost exact published head")
		}
		captured, err := os.ReadFile(filepath.Join(workspace, "requirements.md"))
		if err != nil || string(captured) != string(data) {
			t.Fatal("snapshot lost requirements")
		}
		for _, private := range []string{".env", "checks-1.log"} {
			if _, err := os.Lstat(filepath.Join(workspace, private)); !os.IsNotExist(err) {
				t.Fatal("review snapshot exposed private check inputs or diagnostics")
			}
		}
	}
}

func TestRunnerReadyBoundaryFailureResumesWithoutProviderExecution(t *testing.T) {
	dir, j := testRun(t)
	j.State = "ready"
	j.SessionID = testNative
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	p := &fakeProvider{}
	c := &fakeChecker{}
	pub := &fakePublisher{ciErr: errors.New("GitHub status temporarily unavailable")}
	repo := &fakeRepository{}
	r := fakeRunner(p, c, pub, repo)
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ResumeState != "ready" {
		t.Fatalf("ready failure not checkpointed: %v %+v", err, j)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub.ciErr = nil
	if err := r.Run(context.Background(), dir, &loaded, ""); err != nil {
		t.Fatalf("ready checkpoint could not resume: %v", err)
	}
	saved, err := Load(dir)
	if err != nil || saved.State != "ready" || saved.ResumeState != "" {
		t.Fatalf("ready recovery not saved: %+v %v", saved, err)
	}
	if loaded.State != "ready" || loaded.ResumeState != "" || pub.ci != 2 || len(p.calls) != 0 || c.calls != 0 || pub.calls != 0 || repo.calls != 0 {
		t.Fatalf("recovery repeated work or lost ready boundary: %+v provider=%d checks=%d ci=%d", loaded, len(p.calls), c.calls, pub.ci)
	}
}

func TestRunnerResumedImplementationRejectsDifferentNativeSession(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful handoff", true: "transport failure"}[transportFailure], func(t *testing.T) {
			dir, j := testRun(t)
			j.State = "implementing"
			j.SessionID = testNative
			p := &fakeProvider{resultSessionID: "00000000-0000-4000-8000-000000000002", outcomes: []Outcome{testOutcome("implemented")}}
			if transportFailure {
				p.executeErr = errors.New("provider stream failed")
			}
			c := &fakeChecker{}
			pub := &fakePublisher{}
			repo := &fakeRepository{}
			r := fakeRunner(p, c, pub, repo)
			err := r.Run(context.Background(), dir, j, "")
			if !errors.Is(err, ErrStopped) || j.State != "failed" || j.SessionID != testNative || !strings.Contains(err.Error(), "different implementation session") {
				t.Fatalf("native session replaced: error=%v journal=%+v", err, j)
			}
			if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || c.calls != 0 || pub.calls != 0 || repo.calls != 0 {
				t.Fatal("different session reached checks/publication")
			}
			loaded, err := Load(dir)
			if err != nil || loaded.SessionID != testNative || loaded.ResumeState != "implementing" {
				t.Fatalf("original resume identity not checkpointed: %+v %v", loaded, err)
			}
		})
	}
}

func TestRunnerWaitsForCIStartupWithoutRepeatingImplementation(t *testing.T) {
	dir, j := testRun(t)
	// Successful publication starts a fresh grace even after an earlier wait.
	j.MissingChecksSince = time.Now().Add(-time.Hour)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	c, pub, repo := &fakeChecker{}, &fakePublisher{statuses: []string{"missing", "missing", "passed"}}, &fakeRepository{}
	r := fakeRunner(p, c, pub, repo)
	r.MissingCheckGrace = time.Second
	var output strings.Builder
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || len(p.calls) != 2 || c.calls != 1 || pub.calls != 1 || j.Rounds != 0 || !j.MissingChecksSince.IsZero() || strings.Count(output.String(), "Waiting for CI checks") != 1 {
		t.Fatalf("unexpected wait lifecycle: %+v sessions=%d checks=%d publish=%d output=%s", j, len(p.calls), c.calls, pub.calls, output.String())
	}
}

func TestRunnerMissingCIGracePersistsAcrossResume(t *testing.T) {
	dir, j := testRun(t)
	j.State = "ci"
	p, c, pub, repo := &fakeProvider{}, &fakeChecker{}, &fakePublisher{statuses: []string{"missing"}}, &fakeRepository{}
	r := fakeRunner(p, c, pub, repo)
	r.MissingCheckGrace = 10 * time.Millisecond
	var output strings.Builder
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "configure CI") {
		t.Fatalf("missing CI did not block: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "blocked" || loaded.ResumeState != "ci" || loaded.MissingChecksSince.IsZero() || len(p.calls) != 0 || c.calls != 0 || pub.calls != 0 || loaded.Rounds != 0 {
		t.Fatalf("missing CI checkpoint/effects: %+v sessions=%d checks=%d publish=%d", loaded, len(p.calls), c.calls, pub.calls)
	}
	started, polls := loaded.MissingChecksSince, pub.ci
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("resume restarted grace: %v", err)
	}
	if pub.ci != polls+1 || !loaded.MissingChecksSince.Equal(started) || strings.Count(output.String(), "Waiting for CI checks") != 1 {
		t.Fatal("resume discarded the existing startup deadline")
	}
	pub.statuses = []string{"passed"}
	p.outcomes = []Outcome{testOutcome("reviewed")}
	if err := r.Run(context.Background(), dir, &loaded, ""); err != nil || loaded.State != "ready" || !loaded.MissingChecksSince.IsZero() || len(p.calls) != 1 {
		t.Fatalf("CI evidence did not resume review: state=%s error=%v sessions=%d", loaded.State, err, len(p.calls))
	}
}

func TestRunnerMissingChecksCannotCertifyReview(t *testing.T) {
	for _, stage := range []string{"ready", "final_review", "awaiting_reviewer"} {
		t.Run(stage, func(t *testing.T) {
			dir, j := testRun(t)
			j.State = stage
			statuses := []string{"missing"}
			outcomes := []Outcome{}
			if stage == "final_review" {
				j.State = "awaiting_reviewer"
				statuses = []string{"passed", "missing"}
				outcomes = []Outcome{testOutcome("reviewed")}
			}
			p, c, pub, repo := &fakeProvider{outcomes: outcomes}, &fakeChecker{}, &fakePublisher{statuses: statuses}, &fakeRepository{}
			r := fakeRunner(p, c, pub, repo)
			r.MissingCheckGrace = time.Millisecond
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" {
				t.Fatalf("missing CI certified review: state=%s error=%v", j.State, err)
			}
			if stage == "awaiting_reviewer" && (j.ResumeState != "ci" || j.MissingChecksSince.IsZero() || len(p.calls) != 0) {
				t.Fatal("awaiting reviewer did not return to CI startup wait")
			}
			if stage != "awaiting_reviewer" && !j.MissingChecksSince.IsZero() {
				t.Fatal("completed review received a startup grace")
			}
		})
	}
}

func TestRunnerResumedPromptKeepsNewFeedbackWithoutDuplicatingContext(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.Ticket = ".sdlc/work/TASK-1/tickets/01-selected.md"
	oldBody := strings.Repeat("previous-publication-body-marker ", 4096)
	shared := strings.Repeat("native-global-instructions-marker ", 4096)
	j.Plan.PRBody = oldBody
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Instructions = shared
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	first, resumed, review := p.calls[0], p.calls[1], p.calls[2]
	for _, session := range []Session{first, resumed, review} {
		if strings.Contains(session.Prompt, "native-global-instructions-marker") || session.Instructions != shared {
			t.Fatal("shared instructions duplicated into prompt or lost from native configuration")
		}
	}
	if !strings.Contains(first.Prompt, "previous-publication-body-marker") || strings.Contains(resumed.Prompt, "previous-publication-body-marker") {
		t.Fatal("resumed implementation repeated prior publication context")
	}
	if len(resumed.Prompt) >= len(first.Prompt)/2 {
		t.Fatalf("resume did not reduce repeated context: first=%d resumed=%d", len(first.Prompt), len(resumed.Prompt))
	}
	for _, required := range []string{j.Plan.Reference, j.Plan.Ticket, "Controller verification evidence", testTree, "checks_requested", "JSON schema", "isolated checks", "signing", "publication", "local review"} {
		if !strings.Contains(resumed.Prompt, required) {
			t.Fatalf("compact resume lost %q", required)
		}
	}
	if resumed.ResumeID != testNative || resumed.Model != first.Model || resumed.Schema != outcomeSchema {
		t.Fatal("compact prompt changed native session, model, or structured handoff")
	}
	for _, session := range []Session{first, review} {
		if !strings.Contains(session.Prompt, "Selected launch context") || !strings.Contains(session.Prompt, j.Plan.Ticket) || !strings.Contains(session.Prompt, "delegate narrow investigations") {
			t.Fatal("fresh provider session lost launch context or focused delegation guidance")
		}
	}
}
func TestRunnerCompactRepairPromptContainsOnlyNewFeedbackAndIdentity(t *testing.T) {
	_, j := testRun(t)
	j.SessionID = testNative
	j.Plan.Ticket = ".sdlc/work/TASK-1/tickets/01-selected.md"
	j.Plan.PRBody = strings.Repeat("outdated-PR-body-marker ", 4096)
	j.Feedback = "New finding: app.go:12 rejects valid empty input; preserve the selected behavior."
	r := Runner{Instructions: strings.Repeat("native-shared-body-marker ", 4096)}
	prompt := r.prompt(*j, "implementation")
	if !strings.Contains(prompt, j.Feedback) || !strings.Contains(prompt, j.Plan.Reference) || !strings.Contains(prompt, j.Plan.Ticket) {
		t.Fatal("new repair feedback or selected identity absent")
	}
	for _, old := range []string{"outdated-PR-body-marker", "native-shared-body-marker", "Selected launch context"} {
		if strings.Contains(prompt, old) {
			t.Fatalf("compact repair repeated %q", old)
		}
	}
	if !strings.Contains(prompt, "unchanged tree") || !strings.Contains(prompt, "human questions") || !strings.Contains(prompt, "provider access/usage/policy limits") {
		t.Fatal("compact repair omitted verification or hard-stop gates")
	}
}
