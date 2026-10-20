package workrun

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// DockerRepository inspects worker-controlled Git data only inside a
// credential-free container. Host Git never loads the worker's configuration.
type DockerRepository struct {
	Runtime runtimeimage.Manager
	ImageID string
}

var objectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func mount(source, destination string, readonly bool) string {
	var result strings.Builder
	writer := csv.NewWriter(&result)
	values := []string{"type=bind", "src=" + source, "dst=" + destination}
	if readonly {
		values = append(values, "readonly")
	}
	_ = writer.Write(values)
	writer.Flush()
	return strings.TrimSuffix(result.String(), "\n")
}

const repositoryHelper = `import json,os,pathlib,re,stat,subprocess,sys
env={"PATH":"/usr/local/bin:/usr/bin:/bin","HOME":"/tmp","GIT_CONFIG_NOSYSTEM":"1","GIT_CONFIG_GLOBAL":"/dev/null","GIT_TERMINAL_PROMPT":"0","GIT_OPTIONAL_LOCKS":"0"}
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
def git(*args):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat',*args],cwd=root,env=env,check=True,capture_output=True).stdout
if sys.argv[1].startswith('review_diff:'):
    _,base,head=sys.argv[1].split(':')
    env['GIT_NO_REPLACE_OBJECTS']='1'
    if git('rev-parse','HEAD').decode().strip()!=head: raise ValueError('review snapshot differs')
    args=('--no-ext-diff','--no-textconv','--no-renames',base,head,'--')
    stats=git('diff','--numstat','-z',*args).split(b'\0')
    files=[]
    for row in stats[:2000]:
        if not row: continue
        added,deleted,name=row.split(b'\t',2)
        files.append({'path':name.decode('utf-8','replace'),'added':int(added) if added!=b'-' else 0,'deleted':int(deleted) if deleted!=b'-' else 0,'binary':added==b'-'})
    generated={'package-lock.json','npm-shrinkwrap.json','pnpm-lock.yaml','yarn.lock','go.sum','cargo.lock','gemfile.lock','composer.lock','packages.lock.json','poetry.lock','uv.lock'}
    code_paths=[]
    for f in files:
        p=f['path'].lower(); name=pathlib.PurePosixPath(p).name
        suffix=pathlib.PurePosixPath(p).suffix
        doc=(suffix in ('.md','.rst') or suffix=='.txt' and (p.startswith('docs/') or '/docs/' in p or name in ('readme.txt','license.txt','changelog.txt'))) and name not in ('agents.md','claude.md','skill.md') and not p.startswith(('.codex/','.claude/','.agents/','.cursor/')) and p!='.github/copilot-instructions.md' and '/.claude/' not in p and '/.codex/' not in p
        if not doc and name not in generated and not p.endswith('.g.cs') and '.generated.' not in p: code_paths.append(f['path'])
    patch=b''; truncated=len(stats)>2001
    if sum(len(p.encode()) for p in code_paths)>65536:
        truncated=True
    elif code_paths:
        proc=subprocess.Popen(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','diff','--unified=0','--no-ext-diff','--no-textconv','--no-renames',base,head,'--',*code_paths],cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
        patch=proc.stdout.read(2097153)
        truncated=truncated or len(patch)>2097152
        if truncated: proc.terminate()
        try: code=proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill(); proc.wait(); raise ValueError('diff inspection did not finish')
        if code and not truncated: raise ValueError('diff inspection failed')
    # Fixed lexical signals are conservative; no source text leaves this inspector.
    risk=re.compile(rb'(?i)\b(authenticat\w*|authoriz\w*|password|credential\w*|permission\w*|secret\w*|encrypt\w*|decrypt\w*|mutex|semaphore|lock\s*\(|synchronized|interlocked|drop\s+table|alter\s+table|begin\s+transaction)')
    sensitive=any(risk.search(line[1:]) for line in patch[:2097152].splitlines() if line[:1] in (b'+',b'-') and not line.startswith((b'+++',b'---')))
    print(json.dumps({'files':files,'sensitive':sensitive,'truncated':truncated}))
elif sys.argv[1].startswith('review_tracked:'):
 env['GIT_NO_REPLACE_OBJECTS']='1'
 ref=sys.argv[1].split(':')[1]
 paths=git('ls-tree','-r','--name-only','-z',ref).decode().rstrip('\0')
 print(json.dumps(paths.split('\0') if paths else []))
elif sys.argv[1].startswith('review_tree:'):
 env['GIT_NO_REPLACE_OBJECTS']='1'
 ref=sys.argv[1].split(':')[1]
 print(json.dumps(git('rev-parse','--verify',ref+'^{tree}').decode().strip()))
elif sys.argv[1]=='inspect':
 head=git('rev-parse','--verify','HEAD').decode().strip(); tree=git('rev-parse','HEAD^{tree}').decode().strip()
 status=git('status','--porcelain','--untracked-files=normal','--ignore-submodules=all')
 paths=git('ls-tree','-r','--name-only','-z','HEAD').decode().split('\0')
 for path in paths:
  parts=path.lower().split('/')
  if path.lower().startswith('.sdlc/work/') or '.secrets' in parts or 'profiles.local.json' in parts: raise ValueError('private inputs are tracked')
 print(json.dumps({'head':head,'tree':tree,'clean':not bool(status)}))
elif sys.argv[1].startswith('review_files:'):
 _,base,head=sys.argv[1].split(':')
 env['GIT_NO_REPLACE_OBJECTS']='1'
 if git('rev-parse','HEAD').decode().strip()!=head: raise ValueError('review snapshot differs')
 paths=git('diff','--no-ext-diff','--no-textconv','--no-renames','--name-only','-z',base,head,'--').decode()
 print(json.dumps(paths.rstrip('\0').split('\0') if paths else []))
elif sys.argv[1].startswith('feedback_commits:'):
 _,base,head=sys.argv[1].split(':')
 git('merge-base','--is-ancestor',base,head)
 print(json.dumps(git('rev-list','--reverse','--first-parent',base+'..'+head).decode().split()))
elif sys.argv[1]=='bundle':
 refs=['HEAD']
 if subprocess.run(['git','-c','safe.directory=/workspace','rev-parse','--verify','refs/sdlc/adopted-base^{commit}'],cwd=root,env=env,capture_output=True).returncode==0: refs.append('refs/sdlc/adopted-base')
 git('bundle','create','/export/source.bundle',*refs)
 os.chmod('/export/source.bundle',0o644)
else: raise ValueError('unknown action')
`

const rebaseHelper = `import json,os,pathlib,stat,subprocess,sys,tempfile
env={"PATH":"/usr/local/bin:/usr/bin:/bin","HOME":"/tmp","GIT_CONFIG_NOSYSTEM":"1","GIT_CONFIG_GLOBAL":"/dev/null","GIT_TERMINAL_PROMPT":"0","GIT_OPTIONAL_LOCKS":"0","GIT_NO_REPLACE_OBJECTS":"1"}
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
def git(*args, check=True, input=None):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat','-c','commit.gpgSign=false','-c','user.name=SDLC Restack','-c','user.email=sdlc-restack@example.invalid',*args],cwd=root,env=env,check=check,input=input,capture_output=True)
def reconstructed_tree(original, old, expected):
 # Reconstruct the automatic replay from immutable objects in a fresh,
 # credential-free repository; never change the candidate while proving it.
 with tempfile.TemporaryDirectory(prefix='sdlc-replay-proof-') as temporary:
  def proof(*args):
   return subprocess.run(['git','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','commit.gpgSign=false','-c','user.name=SDLC Restack','-c','user.email=sdlc-restack@example.invalid',*args],cwd=temporary,env=env,check=True,capture_output=True).stdout
  proof('init','-b','replay')
  proof('-c','protocol.file.allow=always','fetch','--no-tags',str(root),original,expected)
  proof('reset','--hard',original)
  proof('rebase','--onto',expected,old)
  return proof('rev-parse','HEAD^{tree}').decode().strip()
old=sys.argv[1]
expected=sys.argv[2]
marker='refs/sdlc/rebase/'+old+'/'+expected
completed='refs/sdlc/rebase-result/'+old+'/'+expected
inprogress=(root/'.git'/'rebase-merge').exists() or (root/'.git'/'rebase-apply').exists()
if inprogress:
 prior=git('rev-parse','--verify',marker,check=False)
 original=prior.stdout.decode().strip() if prior.returncode == 0 else ''
 onto=(root/'.git'/'rebase-merge'/'onto') if (root/'.git'/'rebase-merge').exists() else (root/'.git'/'rebase-apply'/'onto')
 recorded_onto=onto.read_text().strip() if onto.exists() else ''
 orig_head=git('rev-parse','--verify','ORIG_HEAD',check=False).stdout.decode().strip()
 if not original or recorded_onto != expected or orig_head != original:
  raise ValueError('unfinished rebase is not the recorded reconciliation')
 paths=git('diff','--name-only','--diff-filter=U','-z',check=False).stdout.decode().split('\0')
 paths=[p for p in paths if p and not p.startswith('/') and '..' not in pathlib.PurePosixPath(p).parts][:256]
 if paths: print(json.dumps({'conflict':True,'paths':paths})); sys.exit(0)
 raise RuntimeError('unfinished rebase has no conflict paths')
dirty=bool(git('status','--porcelain','--untracked-files=no').stdout)
if dirty and git('rev-parse','--verify',marker,check=False).returncode == 0: raise ValueError('tracked work changed after reconciliation began')
prior=git('rev-parse','--verify',marker,check=False)
if prior.returncode == 0:
 original=prior.stdout.decode().strip()
 # This proves a previously started controller rebase finished on the
 # recorded new base with the same implementation patch. The resulting tree
 # may differ because the new base contains upstream changes.
 current=git('rev-parse','--verify','HEAD').stdout.decode().strip()
 done=git('rev-parse','--verify',completed,check=False)
 if done.returncode == 0:
  if done.stdout.decode().strip() != current or git('merge-base','--is-ancestor',expected,'HEAD',check=False).returncode: raise ValueError('completed rebase revision changed')
  print(json.dumps({'conflict':False,'paths':[]})); sys.exit(0)
 if current == original:
  # The marker was durably written but the process died before Git began.
  # Continue through the normal validation and rebase path below.
  pass
 else:
  if git('merge-base','--is-ancestor',expected,'HEAD',check=False).returncode == 0:
   expected_tree=reconstructed_tree(original,old,expected)
   if git('rev-parse','HEAD^{tree}').stdout.decode().strip() == expected_tree:
    git('update-ref',completed,'HEAD')
    print(json.dumps({'conflict':False,'paths':[]})); sys.exit(0)
  raise ValueError('prior rebase marker does not prove a safe replay')
if git('merge-base','--is-ancestor',old,'HEAD',check=False).returncode: raise ValueError('candidate does not descend from old source')
for commit in git('rev-list','--first-parent',old+'..HEAD').stdout.decode().split():
 parents=git('show','-s','--format=%P',commit).stdout.decode().split()
 if len(parents)!=1: raise ValueError('candidate history is not linear')
git('bundle','verify','/newbase.bundle')
git('-c','protocol.file.allow=always','fetch','--no-tags','/newbase.bundle','refs/sdlc/snapshot:refs/sdlc/new-base')
new=git('rev-parse','--verify','refs/sdlc/new-base^{commit}').stdout.decode().strip()
if new != expected: raise ValueError('new base bundle differs from expected revision')
if dirty:
 # Retain unfinished tracked native work as an unsigned own-range commit,
 # exclusively in this credential-free workspace. Untracked files stay intact.
 # Commit-before-marker interruption is safe: the next pass finds a clean HEAD.
 if git('rev-parse','--verify',marker,check=False).returncode == 0:
  raise ValueError('tracked work changed after reconciliation began')
 git('add','--update')
 git('commit','--no-verify','-m','Preserve tracked ticket work before restack')
git('update-ref',marker,'HEAD')
result=git('rebase','--onto',new,old,check=False)
if result.returncode:
 paths=git('diff','--name-only','--diff-filter=U','-z',check=False).stdout.decode().split('\0')
 paths=[p for p in paths if p and not p.startswith('/') and '..' not in pathlib.PurePosixPath(p).parts][:256]
 if paths:
  print(json.dumps({'conflict':True,'paths':paths})); sys.exit(0)
 raise RuntimeError('rebase failed')
git('update-ref',completed,'HEAD')
print(json.dumps({'conflict':False,'paths':[]}))
`

func (repository DockerRepository) run(ctx context.Context, workspace, action, export string) ([]byte, error) {
	state, err := repository.Runtime.Status(ctx)
	if err != nil {
		return nil, err
	}
	if repository.ImageID != "" && state.ImageID != repository.ImageID {
		return nil, fmt.Errorf("runtime changed since launch; resume with the recorded image")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", mount(workspace, "/workspace", true)}
	args = checkContainerEnvironment(args)
	if export != "" {
		args = append(args, "--mount", mount(export, "/export", false))
	}
	script := repositoryHelper
	if action == "format_snapshot" {
		script = formatterSnapshotHelper
	}
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", script, action)
	output, err := repository.Runtime.Docker.Output(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("credential-free source inspection failed")
	}
	return output, nil
}

func (repository DockerRepository) Inspect(ctx context.Context, workspace string) (Revision, error) {
	data, err := repository.run(ctx, workspace, "inspect", "")
	if err != nil {
		return Revision{}, err
	}
	var revision Revision
	if json.Unmarshal(data, &revision) != nil || !objectID.MatchString(revision.Head) || !objectID.MatchString(revision.Tree) {
		return Revision{}, fmt.Errorf("source inspection returned invalid revisions")
	}
	return revision, nil
}

func (repository DockerRepository) Bundle(ctx context.Context, workspace, destination string) error {
	export, err := os.MkdirTemp(filepath.Dir(destination), ".export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(export)
	if err := os.Chmod(export, 0777); err != nil {
		return err
	}
	if _, err := repository.run(ctx, workspace, "bundle", export); err != nil {
		return err
	}
	path := filepath.Join(export, "source.bundle")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024*1024 {
		return fmt.Errorf("source export is not a bounded regular Git bundle")
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("source export changed while opening")
	}
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, io.LimitReader(source, 2*1024*1024*1024+1))
	syncErr := target.Sync()
	closeErr := target.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot retain complete source bundle")
	}
	return nil
}

// Rebase moves only the linear implementation range from oldSource onto the
// controller-provided new base. It deliberately leaves a true conflict in the
// workspace so the original implementation session can resolve it.
func (repository DockerRepository) Rebase(ctx context.Context, workspace, bundle, oldSource, newBase string) (RebaseResult, error) {
	if !objectID.MatchString(oldSource) || !objectID.MatchString(newBase) {
		return RebaseResult{}, fmt.Errorf("invalid rebase revision")
	}
	info, err := os.Lstat(bundle)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBundleBytes {
		return RebaseResult{}, fmt.Errorf("invalid new base bundle")
	}
	state, err := repository.Runtime.Status(ctx)
	if err != nil || (repository.ImageID != "" && state.ImageID != repository.ImageID) {
		return RebaseResult{}, fmt.Errorf("runtime changed since launch; resume with the recorded image")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", mount(workspace, "/workspace", false), "--mount", mount(bundle, "/newbase.bundle", true)}
	args = checkContainerEnvironment(args)
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", rebaseHelper, oldSource, newBase)
	data, err := repository.Runtime.Docker.Output(ctx, args...)
	if err != nil {
		return RebaseResult{}, fmt.Errorf("credential-free rebase infrastructure failed")
	}
	var result RebaseResult
	if json.Unmarshal(data, &result) != nil || len(result.Paths) > 256 {
		return RebaseResult{}, fmt.Errorf("credential-free rebase returned invalid result")
	}
	for _, path := range result.Paths {
		if path == "" || filepath.IsAbs(path) || strings.Contains(path, "..") {
			return RebaseResult{}, fmt.Errorf("credential-free rebase returned unsafe conflict path")
		}
	}
	return result, nil
}

func (repository DockerRepository) ReviewDiff(ctx context.Context, workspace, base, head string) (ReviewDiff, error) {
	if !objectID.MatchString(base) || !objectID.MatchString(head) {
		return ReviewDiff{}, fmt.Errorf("invalid review diff boundary")
	}
	data, err := repository.run(ctx, workspace, "review_diff:"+base+":"+head, "")
	var result ReviewDiff
	if err != nil || len(data) > 1024*1024 || json.Unmarshal(data, &result) != nil || len(result.Files) > 2000 {
		return ReviewDiff{}, fmt.Errorf("review diff statistics unavailable")
	}
	for _, f := range result.Files {
		if !safeRelative(f.Path) || f.Added < 0 || f.Deleted < 0 || f.Added > 1000000000 || f.Deleted > 1000000000 {
			return ReviewDiff{}, fmt.Errorf("invalid review diff statistics")
		}
	}
	return result, nil
}
