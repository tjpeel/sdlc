package workrun

import (
	"context"
	"encoding/json"
	"fmt"
)

// RebaseMonitoring records an operational replay, not a new review or test pass.
// Source retains the prior publication; Published pins the signed replay.
type RebaseMonitoring struct {
	Source    Publication `json:"source"`
	Published Publication `json:"published"`
	OldBase   string      `json:"old_base"`
	Base      string      `json:"base"`
	Proof     RebaseProof `json:"proof"`
}

type RebaseProof struct {
	SourceHead string `json:"source_head"`
	Head       string `json:"head"`
	Tree       string `json:"tree"`
	Equivalent bool   `json:"equivalent"`
}

type rebaseEquivalenceReader interface {
	RebaseEquivalence(context.Context, string, string, string) (RebaseProof, error)
}

// Compare exact changes, retaining whitespace, modes and binary payloads. Only
// blob IDs and hunk positions may differ as a consequence of the new base.
// Failure to prove equivalence falls back to normal checks and agent review.
const rebaseEquivalenceHelper = `import json,pathlib,re,subprocess,sys
root=pathlib.Path('/workspace')
env={"PATH":"/usr/local/bin:/usr/bin:/bin","HOME":"/tmp","GIT_CONFIG_NOSYSTEM":"1","GIT_CONFIG_GLOBAL":"/dev/null","GIT_TERMINAL_PROMPT":"0","GIT_OPTIONAL_LOCKS":"0","GIT_NO_REPLACE_OBJECTS":"1"}
def git(*args):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat',*args],cwd=root,env=env,check=True,capture_output=True).stdout
def revision(ref):
 return git('rev-parse','--verify',ref).decode().strip()
def patch(base,head):
 data=git('diff','--no-ext-diff','--no-textconv','--no-renames','--no-color','--binary','--full-index','--unified=0',base,head,'--')
 result=[]
 for line in data.splitlines(keepends=True):
  if line.startswith(b'index '): continue
  if line.startswith(b'@@ '):
   match=re.fullmatch(rb'@@ -\d+(,\d+)? \+\d+(,\d+)? @@[^\n]*\n?',line)
   if match is None: raise ValueError('invalid patch hunk')
   line=b'@@ -'+(match[1] or b',1')+b' +'+(match[2] or b',1')+b' @@\n'
  result.append(line)
 return b''.join(result)
old,new=sys.argv[1:]
original=revision('refs/sdlc/rebase/'+old+'/'+new+'^{commit}')
head=revision('HEAD^{commit}')
if git('status','--porcelain','--untracked-files=no'): raise ValueError('dirty rebase candidate')
if (root/'.git'/'rebase-merge').exists() or (root/'.git'/'rebase-apply').exists(): raise ValueError('unfinished rebase')
git('merge-base','--is-ancestor',new,head)
print(json.dumps({'source_head':original,'head':head,'tree':revision('HEAD^{tree}'),'equivalent':patch(old,original)==patch(new,head)}))
`

func (repository DockerRepository) RebaseEquivalence(ctx context.Context, workspace, oldBase, newBase string) (RebaseProof, error) {
	if !objectID.MatchString(oldBase) || !objectID.MatchString(newBase) {
		return RebaseProof{}, fmt.Errorf("invalid rebase proof boundary")
	}
	state, err := repository.Runtime.Status(ctx)
	if err != nil || repository.ImageID != "" && state.ImageID != repository.ImageID {
		return RebaseProof{}, fmt.Errorf("runtime changed before rebase equivalence check")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", mount(workspace, "/workspace", true)}
	args = checkContainerEnvironment(args)
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", rebaseEquivalenceHelper, oldBase, newBase)
	data, err := repository.Runtime.Docker.Output(ctx, args...)
	var proof RebaseProof
	if err != nil || json.Unmarshal(data, &proof) != nil {
		return proof, fmt.Errorf("credential-free rebase equivalence check failed")
	}
	return proof, validateRebaseProof(proof)
}

func validateRebaseProof(p RebaseProof) error {
	if !objectID.MatchString(p.SourceHead) || !objectID.MatchString(p.Head) || !objectID.MatchString(p.Tree) {
		return fmt.Errorf("invalid rebase equivalence proof")
	}
	return nil
}

func validateRebaseMonitoring(j *Journal) error {
	m := j.RebaseMonitoring
	if m == nil {
		return nil
	}
	if !m.Proof.Equivalent || validateRebaseProof(m.Proof) != nil || !objectID.MatchString(m.OldBase) || m.Base != j.Plan.BaseSHA || !objectID.MatchString(m.Base) || !objectID.MatchString(m.Source.HeadSHA) || !objectID.MatchString(m.Source.BaseSHA) || m.Source.Number < 1 || m.Source.Number != j.Publication.Number || m.Source.URL != j.Publication.URL {
		return fmt.Errorf("invalid controller-only rebase boundary")
	}
	if m.Published.Number != 0 && (m.Published != j.Publication || m.Published.BaseSHA != m.Base) {
		return fmt.Errorf("published rebase boundary changed")
	}
	return nil
}

func rebaseCandidateCurrent(j *Journal, revision Revision) bool {
	m := j.RebaseMonitoring
	return m != nil && validateRebaseMonitoring(j) == nil && revision.Clean && revision.Head == m.Proof.Head && revision.Tree == m.Proof.Tree && (m.Published.Number != 0 || j.Publication == m.Source)
}

func (r Runner) completeRebaseMonitoring(ctx context.Context, dir string, j *Journal) error {
	m := j.RebaseMonitoring
	revision, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil || !rebaseCandidateCurrent(j, revision) || m.Published != j.Publication || j.CI.Status != "passed" {
		return r.stop(dir, j, "blocked", "rebase monitoring boundary changed; refusing to reuse equivalence proof")
	}
	j.Feedback = ""
	if err := r.transition(dir, j, "ready"); err != nil {
		return err
	}
	if r.Output != nil {
		fmt.Fprintln(r.Output, "Equivalent rebase published and CI passed; no implementation, self-review or independent-review agent was run.")
	}
	return nil
}

// Feedback may follow a CI-only rebase without inventing fresh local-check or
// review evidence. The exact candidate and publication must remain unchanged.
func rebaseFeedbackEligible(j *Journal) bool {
	m := j.RebaseMonitoring
	return m != nil && validateRebaseMonitoring(j) == nil && m.Published == j.Publication && !j.CandidatePhasePending && j.PendingInputs == nil && len(j.Outcome.Findings) == 0 && len(j.Outcome.Questions) == 0 && len(j.Outcome.Limitations) == 0 && j.CI.Status == "passed"
}

func rebaseFeedbackCandidateCurrent(j *Journal, b *ReviewedFeedbackCandidate) bool {
	m := j.RebaseMonitoring
	return m != nil && validateRebaseMonitoring(j) == nil && m.Published == j.Publication && b.Publication == j.Publication && b.Head == m.Proof.Head && b.Tree == m.Proof.Tree && verificationCurrent(j, b.Head, b.Tree)
}
