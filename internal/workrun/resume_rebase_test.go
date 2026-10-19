package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type resumeTestPublisher struct {
	branchSHA string
	Publisher
	remote                 RemotePR
	snapshots, validations int
	fail                   bool
}

func (p *resumeTestPublisher) Observe(context.Context, Plan, Publication) (RemotePR, error) {
	return p.remote, nil
}
func (p *resumeTestPublisher) Branch(_ context.Context, _ Plan, branch string) (BranchSnapshot, error) {
	sha := p.branchSHA
	if sha == "" {
		sha = p.remote.BaseSHA
	}
	return BranchSnapshot{Branch: branch, SHA: sha}, nil
}
func (p *resumeTestPublisher) ValidateResumeTarget(context.Context, Plan, RemotePR) error {
	p.validations++
	if p.fail {
		return fmt.Errorf("unmerged parent")
	}
	return nil
}
func (p *resumeTestPublisher) Snapshot(_ context.Context, _ Plan, branch, sha, dir string) (BranchSnapshot, error) {
	p.snapshots++
	path := filepath.Join(dir, "base.bundle")
	err := os.WriteFile(path, []byte("bundle fixture"), 0600)
	return BranchSnapshot{Branch: branch, SHA: sha, Bundle: path}, err
}

type resumeTestRepository struct {
	Repository
	calls                 int
	dirty, conflict, fail bool
}

func (r *resumeTestRepository) Inspect(context.Context, string) (Revision, error) {
	return Revision{Head: testHead, Tree: testTree, Clean: !r.dirty}, nil
}
func (r *resumeTestRepository) Rebase(_ context.Context, _ string, _ string, old, new string) (RebaseResult, error) {
	r.calls++
	if old != testBase || new != testTree {
		return RebaseResult{}, fmt.Errorf("wrong replay boundary")
	}
	if r.fail {
		return RebaseResult{}, fmt.Errorf("interrupted")
	}
	return RebaseResult{Conflict: r.conflict, Paths: []string{"source.cs"}}, nil
}
func TestResumeUpstreamDetectionHasNoWorkOrPolicySideEffects(t *testing.T) {
	dir, j := testRun(t)
	j.State = "ready"
	j.Publication = Publication{Number: 40, HeadSHA: testHead}
	j.Plan.Base = "parent"
	before := j.Plan.Roles
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testHead}}
	repo := &resumeTestRepository{}
	runner := Runner{Publisher: p, Repository: repo}
	if _, err := runner.checkResumeUpstream(context.Background(), dir, j); err == nil || !strings.Contains(err.Error(), "--rebase") {
		t.Fatalf("missing actionable drift: %v", err)
	}
	if repo.calls != 0 || p.snapshots != 0 || j.State != "ready" || !reflect.DeepEqual(before, j.Plan.Roles) {
		t.Fatal("drift detection changed run")
	}
	p.remote.BaseSHA = testBase
	if changed, err := runner.checkResumeUpstream(context.Background(), dir, j); err != nil || changed {
		t.Fatalf("unchanged: %v %v", changed, err)
	}
}
func TestResumeRebaseRetainsSessionPolicyAndInvalidatesEvidence(t *testing.T) {
	for _, target := range []string{"parent", "main"} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprint(target, conflict), func(t *testing.T) {
				dir, j := testRun(t)
				j.State = "ci"
				j.Plan.Base = "parent"
				j.SessionID = "original-session"
				j.Publication = Publication{Number: 40, HeadSHA: testHead}
				j.Plan.Roles.AutoAcceptRecommendations = true
				roles := j.Plan.Roles
				publication := j.Publication
				j.Evidence = CheckEvidence{Head: testHead}
				j.CI = CIResult{Status: "passed"}
				j.Outcome = testOutcome("implemented")
				p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: target, BaseSHA: testTree, HeadSHA: testHead}}
				repo := &resumeTestRepository{conflict: conflict}
				runner := Runner{Publisher: p, Repository: repo, Rebase: true}
				changed, err := runner.checkResumeUpstream(context.Background(), dir, j)
				if err != nil || !changed {
					t.Fatalf("%v %v", changed, err)
				}
				if j.Plan.SourceSHA != testTree || j.Plan.Base != target || j.State != "implementing" || !j.RestackNeedsChecks || j.Evidence.Head != "" || j.CI.Status != "" {
					t.Fatalf("bad reconciliation: %+v", j)
				}
				if j.SessionID != "original-session" || !reflect.DeepEqual(roles, j.Plan.Roles) || j.Publication != publication || j.PRFeedback != nil || repo.calls != 1 || p.validations != 1 {
					t.Fatal("session, policy, feedback or publication changed")
				}
				if strings.Contains(j.Feedback, "unfinished Git rebase") != conflict {
					t.Fatal("conflict instructions incorrect")
				}
				if j.Reconciliation != nil || j.ResumeRebase {
					t.Fatal("completed checkpoint retained")
				}
			})
		}
	}
}
func TestResumeRebaseRejectsUnsafeBoundaries(t *testing.T) {
	for _, scenario := range []string{"dirty", "head", "closed", "target", "questions"} {
		t.Run(scenario, func(t *testing.T) {
			dir, j := testRun(t)
			j.Publication = Publication{Number: 40, HeadSHA: testHead}
			j.Plan.Base = "parent"
			p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "main", BaseSHA: testTree, HeadSHA: testHead}}
			repo := &resumeTestRepository{}
			switch scenario {
			case "dirty":
				repo.dirty = true
			case "head":
				p.remote.HeadSHA = testBase
			case "closed":
				p.remote.State = "MERGED"
			case "target":
				p.fail = true
			case "questions":
				j.Outcome.Questions = []string{"choose scope"}
			}
			if _, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil {
				t.Fatal("unsafe rebase accepted")
			}
			if repo.calls != 0 || p.snapshots != 0 {
				t.Fatal("unsafe boundary mutated workspace")
			}
		})
	}
}
func TestResumeRebaseReplaysDurableIntentOnOrdinaryResume(t *testing.T) {
	dir, j := testRun(t)
	j.Publication = Publication{Number: 40, HeadSHA: testHead}
	j.Plan.Base = "parent"
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testHead}}
	repo := &resumeTestRepository{fail: true}
	runner := Runner{Publisher: p, Repository: repo, Rebase: true}
	if _, err := runner.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("expected interruption")
	}
	if !j.ResumeRebase || j.Reconciliation == nil {
		t.Fatal("lost durable intent")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	j = &saved
	repo.fail = false
	runner.Rebase = false
	if changed, err := runner.checkResumeUpstream(context.Background(), dir, j); err != nil || !changed {
		t.Fatalf("ordinary recovery: %v %v", changed, err)
	}
	if p.snapshots != 1 || p.validations != 1 {
		t.Fatal("recovery captured a different target")
	}
}
func TestMergedParentRetargetValidation(t *testing.T) {
	for _, scenario := range []string{"merged", "unmerged", "wrong-target", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			p := GitHubPublisher{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) > 1 && args[1] == "repos/example/project" {
					return []byte(`{"default_branch":"main"}`), nil
				}
				parent := `{"merged_at":"2026-10-19", "base":{"ref":"main"},"head":{"ref":"parent","repo":{"full_name":"example/project"}}}`
				if scenario == "unmerged" {
					return []byte(`[]`), nil
				}
				if scenario == "ambiguous" {
					return []byte("[" + parent + "," + parent + "]"), nil
				}
				return []byte("[" + parent + "]"), nil
			}}
			target := "main"
			if scenario == "wrong-target" {
				target = "unrelated"
			}
			err := p.ValidateResumeTarget(context.Background(), Plan{Repository: "example/project", Base: "parent"}, RemotePR{Base: target})
			if (err == nil) != (scenario == "merged") {
				t.Fatalf("unexpected validation: %v", err)
			}
		})
	}
}

// Exercise the actual credential-free rebaser, not an agent reconstruction.
func TestResumeRebasePreservesChildCommitsAcrossParentUpdatesAndSquashMerge(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprint(merged), func(t *testing.T) {
			root := t.TempDir()
			aggregateGit(t, root, "init", "-b", "main")
			aggregateWrite(t, root, "base.txt", "base")
			aggregateGit(t, root, "checkout", "-b", "parent")
			aggregateWrite(t, root, "parent.txt", "parent")
			old := aggregateGit(t, root, "rev-parse", "HEAD")
			aggregateGit(t, root, "checkout", "-b", "child")
			aggregateWrite(t, root, "child-one.txt", "one")
			aggregateWrite(t, root, "child-two.txt", "two")
			before := aggregateGit(t, root, "log", "--reverse", "--format=%s|%an|%ae|%aI", old+"..HEAD")
			if merged {
				aggregateGit(t, root, "checkout", "main")
				aggregateGit(t, root, "merge", "--squash", "parent")
				aggregateGit(t, root, "commit", "-m", "Merge parent by squash")
			} else {
				aggregateGit(t, root, "checkout", "parent")
			}
			aggregateWrite(t, root, "upstream.txt", "upstream")
			target := aggregateGit(t, root, "rev-parse", "HEAD")
			snapshot := aggregateSnapshot(t, root, aggregateGit(t, root, "branch", "--show-current"), t.TempDir())
			aggregateGit(t, root, "checkout", "child")
			helper := strings.ReplaceAll(rebaseHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
			helper = strings.ReplaceAll(helper, "'/newbase.bundle'", fmt.Sprintf("%q", snapshot.Bundle))
			cmd := exec.Command("python3", "-c", helper, old, target)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("rebase: %v %s", err, output)
			}
			var result RebaseResult
			if json.Unmarshal(output, &result) != nil || result.Conflict {
				t.Fatalf("unexpected result: %s", output)
			}
			after := aggregateGit(t, root, "log", "--reverse", "--format=%s|%an|%ae|%aI", target+"..HEAD")
			if before != after {
				t.Fatalf("commits recreated or metadata changed: before %s after %s", before, after)
			}
			if count := aggregateGit(t, root, "rev-list", "--count", target+"..HEAD"); count != "2" {
				t.Fatalf("parent replayed or child lost: %s", count)
			}
			if changed := aggregateGit(t, root, "diff", "--name-only", target+"..HEAD"); changed != "child-one.txt\nchild-two.txt" {
				t.Fatalf("unexpected child range: %s", changed)
			}
		})
	}
}

func TestResumeRebaseAcceptsVerifiedLocalPublicationSource(t *testing.T) {
	dir, j := testRun(t)
	j.Publication = Publication{Number: 40, HeadSHA: testBase}
	j.Plan.Base = "parent"
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true}
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testBase}}
	repo := &resumeTestRepository{}
	changed, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed || repo.calls != 1 {
		t.Fatalf("verified publication source rejected: %v", err)
	}
}
func TestResumeRebaseRejectsMovedTargetDuringRecovery(t *testing.T) {
	dir, j := testRun(t)
	j.Publication = Publication{Number: 40, HeadSHA: testHead}
	j.Plan.Base = "parent"
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testHead}}
	repo := &resumeTestRepository{fail: true}
	runner := Runner{Publisher: p, Repository: repo, Rebase: true}
	if _, err := runner.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("expected interruption")
	}
	p.remote.BaseSHA = testBase
	if _, err := runner.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("moved target accepted")
	}
	if repo.calls != 1 || p.snapshots != 1 {
		t.Fatal("recovery silently replayed a new target")
	}
}

func TestResumeRebaseRetiresAdoptionBoundaryWithoutLosingProvenance(t *testing.T) {
	dir, j := testRun(t)
	j.Publication = Publication{Number: 40, HeadSHA: testHead}
	j.Plan.Base = "parent"
	adoption := PRAdoption{Number: 40, URL: "https://github.com/example/project/pull/40", Branch: j.Plan.Branch, Base: j.Plan.Base, HeadSHA: testHead, BaseSHA: testBase}
	j.Plan.SourceSHA, j.Plan.StartingSHA = testHead, testHead
	j.Plan.Adoption = &adoption
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testTree, HeadSHA: testHead}}
	changed, err := (Runner{Publisher: p, Repository: &resumeTestRepository{}, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("adopted rebase: %v", err)
	}
	if j.Plan.Adoption != nil || len(j.RebasedAdoptions) != 1 || !reflect.DeepEqual(j.RebasedAdoptions[0], adoption) {
		t.Fatal("adoption boundary was neither retired nor retained in history")
	}
	if err := validateAdoptionPlan(j.Plan); err != nil {
		t.Fatalf("obsolete adoption invariant blocks publication: %v", err)
	}
}

func TestResumeRebaseUsesBranchTipWhenPRBaseIsStale(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			dir, j := testRun(t)
			j.Plan.Base = "parent"
			j.Publication = Publication{Number: 40, HeadSHA: testHead}
			p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
			repo := &resumeTestRepository{}
			runner := Runner{Publisher: p, Repository: repo, Rebase: explicit}
			changed, err := runner.checkResumeUpstream(context.Background(), dir, j)
			if !explicit {
				if err == nil || !strings.Contains(err.Error(), "--rebase") || repo.calls != 0 {
					t.Fatalf("stale PR base hid updated branch: %v", err)
				}
				return
			}
			if err != nil || !changed || repo.calls != 1 || j.Plan.BaseSHA != testTree {
				t.Fatalf("did not rebase onto live tip: changed %v err %v calls %d base %s", changed, err, repo.calls, j.Plan.BaseSHA)
			}
			if j.Plan.Restack == nil || j.Plan.Restack.BaseSHA != testBase {
				t.Fatal("stored PR base lease was replaced by branch tip")
			}
		})
	}
}
func TestResumeRebaseAllowsSettledFeedbackCycleWithoutProcessingComments(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.Base = "parent"
	j.Publication = Publication{URL: "https://github.com/example/project/pull/40", Number: 40, BaseSHA: testBase, HeadSHA: testHead}
	c := generalFeedback("comment:1", "Already handled.")
	a := assessedOutcome(c, "no_change").PRFeedbackAssessments[0]
	e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason, Reply: &PRFeedbackReply{ID: "existing-reply"}}
	e.Marker = feedbackEntryMarker(j.ID, e)
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{e}}
	roles := j.Plan.Roles
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	changed, err := (Runner{Publisher: p, Repository: &resumeTestRepository{}, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("settled feedback unnecessarily blocked explicit rebase: %v", err)
	}
	if j.PRFeedback.Active || len(j.PRFeedback.Entries) != 1 || j.PRFeedback.Entries[0].Reply.ID != "existing-reply" || !reflect.DeepEqual(j.Plan.Roles, roles) {
		t.Fatal("rebase changed feedback history or acceptance policy")
	}
}
func TestResumeRebaseAcceptsStoredPRBaseRefreshingDuringRecovery(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.Base = "parent"
	j.Publication = Publication{Number: 40, HeadSHA: testHead}
	p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
	repo := &resumeTestRepository{fail: true}
	runner := Runner{Publisher: p, Repository: repo, Rebase: true}
	if _, err := runner.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("expected interruption")
	}
	p.remote.BaseSHA = testTree
	repo.fail = false
	runner.Rebase = false
	if changed, err := runner.checkResumeUpstream(context.Background(), dir, j); err != nil || !changed || p.snapshots != 1 {
		t.Fatalf("PR metadata refresh broke durable recovery: %v", err)
	}
}

func TestResumeRebaseKeepsUnfinishedFeedbackBlocked(t *testing.T) {
	for _, kind := range []string{"pending", "reply-due", "unresolved"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := testRun(t)
			j.Plan.Base = "parent"
			j.Publication = Publication{URL: "https://github.com/example/project/pull/40", Number: 40, BaseSHA: testBase, HeadSHA: testHead}
			c := generalFeedback("comment:1", "Unfinished feedback.")
			a := assessedOutcome(c, "no_change").PRFeedbackAssessments[0]
			e := PRFeedbackEntry{Comment: c, Fingerprint: feedbackFingerprint(c), Assessment: &a, ReplyBody: a.Reason, Reply: &PRFeedbackReply{ID: "receipt"}}
			e.Marker = feedbackEntryMarker(j.ID, e)
			switch kind {
			case "pending":
				e.Assessment = nil
			case "reply-due":
				e.Reply = nil
			case "unresolved":
				a.Disposition = "unaddressable"
			}
			j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testHead, Entries: []PRFeedbackEntry{e}}
			p := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: "parent", BaseSHA: testBase, HeadSHA: testHead}, branchSHA: testTree}
			repo := &resumeTestRepository{}
			if _, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil || repo.calls != 0 || p.snapshots != 0 {
				t.Fatalf("unfinished feedback was bypassed: %v", err)
			}
		})
	}
}
