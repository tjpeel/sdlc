package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type monitoringRepository struct {
	resumeTestRepository
	equivalent bool
	bundles    int
}

func (r *monitoringRepository) RebaseEquivalence(context.Context, string, string, string) (RebaseProof, error) {
	return RebaseProof{SourceHead: testHead, Head: testHead, Tree: testTree, Equivalent: r.equivalent}, nil
}
func (r *monitoringRepository) FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error) {
	return nil, nil
}
func (r *monitoringRepository) Bundle(context.Context, string, string) error { r.bundles++; return nil }

type monitoringPublisher struct {
	signedHead string
	resumeTestPublisher
	publishes, checks int
	statuses          []string
	publishErr        error
}

func (p *monitoringPublisher) Publish(_ context.Context, plan Plan, _, _ string, prior Publication, _ io.Writer) (Publication, error) {
	p.publishes++
	if p.publishErr != nil {
		return Publication{}, p.publishErr
	}
	head := p.signedHead
	if head == "" {
		head = testHead
	}
	return Publication{Number: prior.Number, URL: prior.URL, HeadSHA: head, BaseSHA: plan.BaseSHA}, nil
}
func (p *monitoringPublisher) Checks(context.Context, Plan, Publication) (CIResult, error) {
	p.checks++
	status := "passed"
	if len(p.statuses) > 0 {
		status = p.statuses[0]
		if len(p.statuses) > 1 {
			p.statuses = p.statuses[1:]
		}
	}
	return CIResult{Status: status}, nil
}

func monitoringFixture(t *testing.T) (string, *Journal, *monitoringRepository, *monitoringPublisher, *fakeProvider, Runner) {
	t.Helper()
	dir, j := testRun(t)
	j.State = "ready"
	j.Plan.Base = "parent"
	j.Plan.PRTitle = "TASK-1 selected work"
	j.Plan.PRBody = "Implements TASK-1."
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
	j.Plan.Roles.AutoAcceptRecommendations = true
	repo := &monitoringRepository{equivalent: true}
	pub := &monitoringPublisher{resumeTestPublisher: resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testHead}}}
	provider := &fakeProvider{}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	runner.Repository = repo
	runner.Publisher = pub
	runner.Rebase = true
	return dir, j, repo, pub, provider, runner
}

func TestEquivalentRebaseRunsLocalChecksAndParksWithoutRemoteWait(t *testing.T) {
	dir, j, repo, pub, provider, r := monitoringFixture(t)
	pub.statuses = []string{"pending", "passed"}
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.calls) != 0 || j.Attempt != 0 || pub.publishes != 1 || pub.checks != 0 || repo.bundles != 1 || j.RebaseMonitoring == nil || !j.Evidence.Passed || j.CI.Status != "" || j.SelfReview != nil || j.Outcome.LocalReview || len(j.ReviewReports) != 0 {
		t.Fatalf("unexpected work or fabricated evidence: state=%s turns=%d attempts=%d publishes=%d CI=%d bundles=%d", j.State, len(provider.calls), j.Attempt, pub.publishes, pub.checks, repo.bundles)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	r.Rebase = false
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 0 || pub.publishes != 1 {
		t.Fatal("ready resume repeated work")
	}
}

func TestEquivalentRebasePublicationRetryNeedsNoAgent(t *testing.T) {
	dir, j, _, pub, provider, r := monitoringFixture(t)
	pub.publishErr = errors.New("signing unavailable")
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.ResumeState != "publishing" || len(provider.calls) != 0 {
		t.Fatal("publication failure dispatched agent")
	}
	pub.publishErr = nil
	r.Rebase = false
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || len(provider.calls) != 0 {
		t.Fatal("publication retry repeated agent work")
	}
}

func TestRebaseChangedPatchAndConflictRetainNormalReview(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			dir, j, repo, _, _, r := monitoringFixture(t)
			repo.equivalent = false
			repo.conflict = conflict
			if changed, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil || !changed {
				t.Fatal(changed, err)
			}
			if j.State != "implementing" || j.RebaseMonitoring != nil || !j.RestackNeedsChecks {
				t.Fatal("substantive change bypassed review")
			}
		})
	}
}

func TestEquivalentRebaseCIFailureRetiresBypassBeforeRepair(t *testing.T) {
	dir, j, _, pub, _, r := monitoringFixture(t)
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.Publication = Publication{Number: 1, URL: j.Publication.URL, HeadSHA: testHead, BaseSHA: testTree}
	j.RebaseMonitoring.Published = j.Publication
	j.State = "ci"
	pub.statuses = []string{"failed"}
	_ = r.checkCI(context.Background(), dir, j)
	if j.RebaseMonitoring != nil {
		t.Fatal("failed CI retained review bypass")
	}
}

func TestRebaseMonitoringRejectsChangedCandidateAndPublication(t *testing.T) {
	dir, j, _, _, _, r := monitoringFixture(t)
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if rebaseCandidateCurrent(j, Revision{Head: testBase, Tree: testTree, Clean: true}) {
		t.Fatal("changed head admitted")
	}
	if rebaseCandidateCurrent(j, Revision{Head: testHead, Tree: testBase, Clean: true}) {
		t.Fatal("changed tree admitted")
	}
	if rebaseCandidateCurrent(j, Revision{Head: testHead, Tree: testTree}) {
		t.Fatal("dirty tree admitted")
	}
	j.RebaseMonitoring.Published = j.Publication
	j.RebaseMonitoring.Published.BaseSHA = testTree
	if validateRebaseMonitoring(j) == nil {
		t.Fatal("wrong publication admitted")
	}
}

func runRebaseProof(t *testing.T, root, old, new string) RebaseProof {
	t.Helper()
	helper := strings.ReplaceAll(rebaseEquivalenceHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
	output, err := exec.Command("python3", "-c", helper, old, new).CombinedOutput()
	if err != nil {
		t.Fatalf("proof failed: %v %s", err, output)
	}
	var p RebaseProof
	if err := json.Unmarshal(output, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRebaseEquivalenceUsesExactChangesIncludingWhitespace(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateWrite(t, root, "code.txt", "one\ntwo\nthree\n")
	old := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "-b", "child")
	aggregateWrite(t, root, "code.txt", "one\n  two\nthree\n")
	source := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "main")
	aggregateWrite(t, root, "upstream.txt", "new base\n")
	target := aggregateGit(t, root, "rev-parse", "HEAD")
	aggregateGit(t, root, "checkout", "child")
	aggregateGit(t, root, "update-ref", "refs/sdlc/rebase/"+old+"/"+target, source)
	aggregateGit(t, root, "rebase", "--onto", target, old)
	p := runRebaseProof(t, root, old, target)
	if !p.Equivalent || p.SourceHead != source {
		t.Fatal("clean replay not equivalent", p)
	}
	aggregateWrite(t, root, "code.txt", "one\n    two\nthree\n")
	if runRebaseProof(t, root, old, target).Equivalent {
		t.Fatal("whitespace-sensitive behavioural change accepted")
	}
}

func (r *monitoringRepository) Rebase(_ context.Context, _, _, _, _ string) (RebaseResult, error) {
	r.calls++
	return RebaseResult{Conflict: r.conflict}, nil
}

func TestEquivalentRebaseAfterSigningCanRebaseAgainWithoutAgents(t *testing.T) {
	dir, j, repo, pub, provider, r := monitoringFixture(t)
	pub.signedHead = strings.Repeat("4", 40)
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.Publication.HeadSHA == j.RebaseMonitoring.Proof.Head {
		t.Fatal("fixture did not exercise publication signing")
	}
	pub.remote.HeadSHA = pub.signedHead
	pub.remote.BaseSHA = testBase
	pub.branchSHA = testBase
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if repo.calls != 2 || pub.publishes != 2 || len(provider.calls) != 0 || j.State != "awaiting_feedback" {
		t.Fatal("signed prior rebase repeated agents or rejected source")
	}
}

type monitoringInspectionDocker struct {
	pairingRaceDocker
	revision Revision
}

func (d *monitoringInspectionDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "run" && args[len(args)-1] == "inspect" {
		return json.Marshal(d.revision)
	}
	return d.pairingRaceDocker.Output(ctx, args...)
}

func TestConcretePublisherAcceptsEquivalentRebaseWithoutPassingTestEvidence(t *testing.T) {
	dir, j, _, _, _, r := monitoringFixture(t)
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	// Retain admission for a legacy saved CI-only rebase boundary. New rebases require local evidence.
	j.RebaseMonitoring.LocalChecks = false
	// The controller is itself a supported product; avoid a source Git fixture
	// because this test exercises the concrete publication admission boundary.
	j.Plan.Repository = "example/sdlc"
	j.Publication.URL = "https://github.com/example/sdlc/pull/1"
	j.RebaseMonitoring.Source = j.Publication
	j.Plan.PublicationIdentity = frozenTestIdentity()
	j.Plan.PublicationIdentity.RepositoryName = j.Plan.Repository
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	docker := &monitoringInspectionDocker{pairingRaceDocker: pairingRaceDocker{image: "sha256:" + strings.Repeat("a", 64)}, revision: Revision{Head: testHead, Tree: testTree, Clean: true}}
	state, _ := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: docker.image})
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	reached := errors.New("publication preflight reached")
	publisher := DockerPublisher{Runtime: runtimeimage.Manager{Directory: runtimeDir, Docker: docker}, ImageID: docker.image, ValidatePair: func() error { return reached }}
	_, err := publisher.Publish(context.Background(), j.Plan, j.Workspace, dir, j.Publication, io.Discard)
	if !errors.Is(err, reached) {
		t.Fatalf("concrete publication rejected valid rebase proof before preflight: %v", err)
	}
	if j.Evidence.Passed {
		t.Fatal("rebase invented passing test evidence")
	}
	docker.revision.Tree = testBase
	_, err = publisher.Publish(context.Background(), j.Plan, j.Workspace, dir, j.Publication, io.Discard)
	if err == nil || errors.Is(err, reached) {
		t.Fatal("changed source reached publication preflight", err)
	}
}

type localRebaseFeedbackPublisher struct {
	*monitoringPublisher
	comments []PRFeedbackComment
	replies  int
}

func (p *localRebaseFeedbackPublisher) Feedback(context.Context, Plan, Publication) ([]PRFeedbackComment, error) {
	return p.comments, nil
}
func (p *localRebaseFeedbackPublisher) ReplyFeedback(_ context.Context, _ Plan, _ Publication, _ PRFeedbackComment, marker, body string) (PRFeedbackReply, error) {
	p.replies++
	return PRFeedbackReply{ID: marker, URL: body}, nil
}

func TestLocalRebaseFeedbackContinuesWithoutRemoteCI(t *testing.T) {
	dir, j, _, pub, _, r := monitoringFixture(t)
	j.SessionID = testNative
	oldComment := generalFeedback("comment:old", "Already handled.")
	oldAssessment := assessedOutcome(oldComment, "no_change").PRFeedbackAssessments[0]
	oldEntry := PRFeedbackEntry{Comment: oldComment, Fingerprint: feedbackFingerprint(oldComment), Assessment: &oldAssessment, ReplyBody: oldAssessment.Reason, Reply: &PRFeedbackReply{ID: "existing-reply"}}
	oldEntry.Marker = feedbackEntryMarker(j.ID, oldEntry)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: testHead, Entries: []PRFeedbackEntry{oldEntry}}
	pub.signedHead = strings.Repeat("4", 40)
	fp := &localRebaseFeedbackPublisher{monitoringPublisher: pub}
	r.Publisher = fp
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "awaiting_feedback" || j.CI.Status != "" || !j.Evidence.Passed || pub.checks != 0 {
		t.Fatalf("bad local checkpoint: %+v", j)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePRFeedbackResume(saved); err != nil {
		t.Fatal(err)
	}
	comment := generalFeedback("comment:1", "Is this already covered?")
	fp.comments = []PRFeedbackComment{oldComment, comment}
	provider := &feedbackScriptProvider{outcomes: []Outcome{assessedOutcome(comment, "no_change")}}
	r.Provider, r.Rebase, r.ProcessPRFeedback = provider, false, true
	if err := r.Run(context.Background(), dir, &saved, ""); err != nil {
		t.Fatal(err)
	}
	if saved.State != "awaiting_feedback" || saved.CI.Status != "" || pub.checks != 0 || fp.replies != 1 || len(provider.sessions) != 1 {
		t.Fatalf("feedback boundary: state=%s ci=%q calls=%d replies=%d turns=%d", saved.State, saved.CI.Status, pub.checks, fp.replies, len(provider.sessions))
	}
}

func TestLocalRebaseCheckFailureDoesNotPublish(t *testing.T) {
	dir, j, _, pub, _, r := monitoringFixture(t)
	checker := r.Checker.(*fakeChecker)
	checker.err = errors.New("local check unavailable")
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected stopped run: %v", err)
	}
	if pub.publishes != 0 || pub.checks != 0 || j.Evidence.Passed || j.State != "blocked" {
		t.Fatalf("unsafe result: %+v", j)
	}
}

func TestLocalRebaseRejectsMissingChecksBeforePublication(t *testing.T) {
	dir, j, _, pub, _, r := monitoringFixture(t)
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.State = "publishing"
	r.Rebase = false
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected refused publication: %v", err)
	}
	if pub.publishes != 0 {
		t.Fatal("published without local checks")
	}
}

func TestLocalRebaseFeedbackRejectsInvalidatedCheckEvidence(t *testing.T) {
	dir, j, _, _, _, r := monitoringFixture(t)
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	j.Evidence.Passed = false
	if err := ValidatePRFeedbackResume(*j); err == nil {
		t.Fatal("feedback admitted without retained local checks")
	}
}

type changedRebaseRepository struct {
	*monitoringRepository
	revision Revision
}

func (r *changedRebaseRepository) Inspect(context.Context, string) (Revision, error) {
	return r.revision, nil
}

func TestLocalRebaseChangedCandidateLosesFastPath(t *testing.T) {
	dir, j, repo, pub, _, r := monitoringFixture(t)
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	r.Rebase = false
	r.Repository = &changedRebaseRepository{monitoringRepository: repo, revision: Revision{Head: strings.Repeat("5", 40), Tree: strings.Repeat("6", 40), Clean: true}}
	r.Checker.(*fakeChecker).err = errors.New("check unavailable")
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("expected check hold: %v", err)
	}
	if j.RebaseMonitoring != nil || pub.publishes != 0 {
		t.Fatal("changed candidate retained equivalent-rebase shortcut")
	}
}
