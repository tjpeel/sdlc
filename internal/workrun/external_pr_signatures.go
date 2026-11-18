package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ExternalPRSignatureRepair struct {
	Original      Revision `json:"original"`
	PublishedHead string   `json:"published_head"`
	CandidateHead string   `json:"candidate_head,omitempty"`
	Completed     bool     `json:"completed,omitempty"`
}

type externalPRSignaturePublisher interface {
	UnverifiedPRCommits(context.Context, Plan, Publication) ([]string, error)
	PublishedPRTree(context.Context, Plan, Publication) (string, error)
}
type externalPRSignatureRepository interface {
	NormalizeExternalPRHistory(context.Context, string, Revision, string, string) (Revision, error)
}

func externalSignatureCandidateCurrent(j *Journal, revision Revision) bool {
	if j.ExternalPR == nil || j.ExternalPR.SignatureRepair == nil {
		return false
	}
	s := j.ExternalPR.SignatureRepair
	return s.Completed && s.PublishedHead == j.Publication.HeadSHA && revision.Clean && revision.Head == s.CandidateHead && revision.Tree == s.Original.Tree
}

const normalizeExternalPRHelper = `import json,os,pathlib,stat,subprocess,sys
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
env={'PATH':'/usr/local/bin:/usr/bin:/bin','HOME':'/tmp','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_TERMINAL_PROMPT':'0','GIT_NO_REPLACE_OBJECTS':'1'}
def git(*args,check=True):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat','-c','commit.gpgSign=false','-c','user.name=SDLC','-c','user.email=sdlc@example.invalid',*args],cwd=root,env=env,check=check,capture_output=True)
old,tree,parent,branch=sys.argv[1:]
def text(*args): return git(*args).stdout.decode().strip()
if text('symbolic-ref','HEAD') != 'refs/heads/'+branch: raise ValueError('workspace branch changed')
if text('status','--porcelain','--untracked-files=normal','--ignore-submodules=all'): raise ValueError('workspace changed')
marker='refs/sdlc/external-pr-normalized/'+old+'/'+parent
existing=git('rev-parse','--verify',marker,check=False)
head=text('rev-parse','HEAD')
if existing.returncode == 0:
 candidate=existing.stdout.decode().strip()
 if text('rev-parse',candidate+'^{tree}') != tree or text('show','-s','--format=%P',candidate) != parent: raise ValueError('candidate marker changed')
else:
 if head != old or text('rev-parse','HEAD^{tree}') != tree: raise ValueError('workspace revision changed')
 git('merge-base','--is-ancestor',parent,old)
 candidate=text('commit-tree',tree,'-p',parent,'-m','Integrate incoming pull request changes')
 git('update-ref',marker,candidate)
if head == old: git('update-ref','refs/heads/'+branch,candidate,old)
elif head != candidate: raise ValueError('workspace revision changed')
if text('rev-parse','HEAD^{tree}') != tree or text('status','--porcelain','--untracked-files=normal','--ignore-submodules=all'): raise ValueError('normalized tree changed')
print(json.dumps({'head':candidate,'tree':tree,'clean':True}))
`

func (repository DockerRepository) NormalizeExternalPRHistory(ctx context.Context, workspace string, original Revision, parent, branch string) (Revision, error) {
	if !original.Clean || !objectID.MatchString(original.Head) || !objectID.MatchString(original.Tree) || !objectID.MatchString(parent) || !validExternalBranch(branch) {
		return Revision{}, fmt.Errorf("invalid signature repair boundary")
	}
	if _, err := os.Lstat(workspace); err != nil {
		return Revision{}, err
	}
	if err := realDirectory(workspace); err != nil {
		return Revision{}, err
	}
	state, err := repository.Runtime.Status(ctx)
	if err != nil || repository.ImageID != "" && state.ImageID != repository.ImageID {
		return Revision{}, fmt.Errorf("runtime changed during signature repair")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", mount(workspace, "/workspace", false)}
	args = checkContainerEnvironment(args)
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", normalizeExternalPRHelper, original.Head, original.Tree, parent, branch)
	data, err := repository.Runtime.Docker.Output(ctx, args...)
	if err != nil {
		return Revision{}, fmt.Errorf("credential-free signature repair failed")
	}
	var result Revision
	if json.Unmarshal(data, &result) != nil || !result.Clean || !objectID.MatchString(result.Head) || result.Tree != original.Tree {
		return Revision{}, fmt.Errorf("invalid signature repair result")
	}
	return result, nil
}

// UnverifiedPRCommits checks every page and refuses incomplete or changing
// comparisons. GitHub's verification is authoritative for branch protection.
func (p GitHubPublisher) UnverifiedPRCommits(ctx context.Context, plan Plan, pub Publication) ([]string, error) {
	if p.Frozen == nil || !objectID.MatchString(pub.HeadSHA) || !objectID.MatchString(pub.BaseSHA) {
		return nil, fmt.Errorf("invalid signature comparison boundary")
	}
	if err := p.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := p.verifyRepositoryIdentity(ctx, plan); err != nil {
		return nil, err
	}
	var result []string
	seen := map[string]bool{}
	total, count, page := -1, 0, 1
	last := ""
	for {
		endpoint := fmt.Sprintf("repos/%s/compare/%s...%s?per_page=100&page=%d", plan.Repository, pub.BaseSHA, pub.HeadSHA, page)
		data, err := p.command(ctx, "gh", "api", endpoint, "--jq", `{total_commits, commits: [.commits[] | {sha, verified: .commit.verification.verified}]}`)
		if err != nil {
			return nil, err
		}
		var response struct {
			Total   int `json:"total_commits"`
			Commits []struct {
				SHA      string `json:"sha"`
				Verified *bool  `json:"verified"`
			} `json:"commits"`
		}
		if json.Unmarshal(data, &response) != nil || response.Total < 1 || total >= 0 && response.Total != total || len(response.Commits) == 0 {
			return nil, fmt.Errorf("incomplete PR signature comparison")
		}
		total = response.Total
		for _, c := range response.Commits {
			if !objectID.MatchString(c.SHA) || c.Verified == nil || seen[c.SHA] {
				return nil, fmt.Errorf("invalid PR signature comparison")
			}
			seen[c.SHA] = true
			count++
			last = c.SHA
			if !*c.Verified {
				result = append(result, c.SHA)
			}
		}
		if count == total {
			break
		}
		if count > total {
			return nil, fmt.Errorf("invalid PR signature count")
		}
		page++
	}
	if last != pub.HeadSHA {
		return nil, fmt.Errorf("PR signature comparison head changed")
	}
	return result, nil
}

func (p GitHubPublisher) PublishedPRTree(ctx context.Context, plan Plan, pub Publication) (string, error) {
	if !objectID.MatchString(pub.HeadSHA) {
		return "", fmt.Errorf("invalid published head")
	}
	data, err := p.command(ctx, "gh", "api", fmt.Sprintf("repos/%s/git/commits/%s", plan.Repository, pub.HeadSHA))
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if json.Unmarshal(data, &commit) != nil || commit.SHA != pub.HeadSHA || !objectID.MatchString(commit.Tree.SHA) {
		return "", fmt.Errorf("invalid published commit tree")
	}
	return commit.Tree.SHA, nil
}

func (p DockerPublisher) UnverifiedPRCommits(ctx context.Context, plan Plan, pub Publication) (heads []string, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { var e error; heads, e = g.UnverifiedPRCommits(ctx, plan, pub); return e })
	return
}
func (p DockerPublisher) PublishedPRTree(ctx context.Context, plan Plan, pub Publication) (tree string, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { var e error; tree, e = g.PublishedPRTree(ctx, plan, pub); return e })
	return
}

func (r Runner) repairExternalPRSignatures(ctx context.Context, dir string, j *Journal) error {
	m := j.ExternalPR
	if m == nil || !m.Completed || m.SignatureRepair != nil && m.SignatureRepair.Completed {
		return nil
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return err
	}
	if err := verifyInputs(filepath.Join(dir, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return err
	}
	p, ok := r.Publisher.(externalPRSignaturePublisher)
	if !ok {
		return fmt.Errorf("publisher cannot verify PR commit signatures")
	}
	repo, ok := r.Repository.(externalPRSignatureRepository)
	if !ok {
		return fmt.Errorf("repository cannot repair PR commit signatures")
	}
	up, ok := r.Publisher.(resumeUpstreamPublisher)
	if !ok {
		return fmt.Errorf("publisher cannot verify PR boundary")
	}
	remote, err := up.Observe(ctx, j.Plan, j.Publication)
	if err != nil {
		return err
	}
	if remote.State != "OPEN" || remote.HeadSHA != j.Publication.HeadSHA || remote.Base != j.Plan.Base {
		return fmt.Errorf("PR changed before signature verification")
	}
	if m.SignatureRepair == nil {
		unverified, err := p.UnverifiedPRCommits(ctx, j.Plan, j.Publication)
		if err != nil {
			return err
		}
		needsLinearRebase := false
		if m.ReconcileBase && r.CheckUpstream {
			target, err := up.Branch(ctx, j.Plan, j.Plan.Base)
			if err != nil {
				return err
			}
			if target.Branch != j.Plan.Base || !objectID.MatchString(target.SHA) {
				return fmt.Errorf("invalid upstream branch observation")
			}
			needsLinearRebase = target.SHA != j.Plan.BaseSHA
		}
		if len(unverified) == 0 && !needsLinearRebase {
			return nil
		}
		revision, err := r.Repository.Inspect(ctx, j.Workspace)
		if err != nil {
			return err
		}
		tree, err := p.PublishedPRTree(ctx, j.Plan, j.Publication)
		if err != nil {
			return err
		}
		if !externalPublishedWorkspace(j, revision, tree) {
			return fmt.Errorf("signature repair requires a clean workspace matching the published PR")
		}
		m.SignatureRepair = &ExternalPRSignatureRepair{Original: revision, PublishedHead: j.Publication.HeadSHA}
		m.ReconcileBase = true
		if err := r.save(dir, j); err != nil {
			return err
		}
		if r.Output != nil {
			fmt.Fprintf(r.Output, "Preparing a tree-preserving signed candidate (%d unverified commits; upstream rebase required: %t).\n", len(unverified), needsLinearRebase)
		}
	}
	s := m.SignatureRepair
	if j.Publication.HeadSHA != s.PublishedHead {
		return fmt.Errorf("published PR changed during signature repair")
	}
	revision, err := repo.NormalizeExternalPRHistory(ctx, j.Workspace, s.Original, m.Boundary.ParentHeadSHA, m.Boundary.ParentBranch)
	if err != nil {
		return err
	}
	if !revision.Clean || revision.Tree != s.Original.Tree || !objectID.MatchString(revision.Head) {
		return fmt.Errorf("signature repair changed the verified tree")
	}
	remote, err = up.Observe(ctx, j.Plan, j.Publication)
	if err != nil || remote.State != "OPEN" || remote.HeadSHA != s.PublishedHead || remote.Base != j.Plan.Base {
		return fmt.Errorf("PR changed during signature repair")
	}
	s.CandidateHead, s.Completed = revision.Head, true
	j.Plan.SourceSHA, j.Plan.StartingSHA = m.Boundary.ParentHeadSHA, m.Boundary.ParentHeadSHA
	j.Evidence, j.CI, j.Outcome = CheckEvidence{}, CIResult{}, Outcome{}
	j.MissingChecksSince = time.Time{}
	j.FormatterPreparation, j.LiveDiagnostics = nil, nil
	j.DiagnosticFeedbackKey, j.VerificationFeedbackKey = "", ""
	j.ReviewAssessment, j.ReviewContinuation, j.ReviewHumanInput, j.ReviewSkip = nil, nil, nil, nil
	j.SelfReview, j.ReviewReuse, j.RebaseMonitoring, j.ReviewPlan = nil, nil, nil, nil
	j.CandidatePhasePending = false
	j.RestackNeedsChecks = true
	j.State, j.ResumeState, j.StopReason, j.PendingRole = "implementing", "", "", ""
	j.Feedback = "The controller preserved the complete published tree in a new linear candidate so it can sign all new changes. Preserve the incoming PR's behaviour and obtain fresh checks and local review before publication."
	return r.save(dir, j)
}
