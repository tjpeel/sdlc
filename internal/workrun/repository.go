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

const repositoryHelper = `import json,os,pathlib,stat,subprocess,sys
env={"PATH":"/usr/local/bin:/usr/bin:/bin","HOME":"/tmp","GIT_CONFIG_NOSYSTEM":"1","GIT_CONFIG_GLOBAL":"/dev/null","GIT_TERMINAL_PROMPT":"0","GIT_OPTIONAL_LOCKS":"0"}
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
def git(*args):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat',*args],cwd=root,env=env,check=True,capture_output=True).stdout
if sys.argv[1]=='inspect':
 head=git('rev-parse','--verify','HEAD').decode().strip(); tree=git('rev-parse','HEAD^{tree}').decode().strip()
 status=git('status','--porcelain','--untracked-files=normal','--ignore-submodules=all')
 paths=git('ls-tree','-r','--name-only','-z','HEAD').decode().split('\0')
 for path in paths:
  parts=path.lower().split('/')
  if path.lower().startswith('.sdlc/work/') or '.secrets' in parts or 'profiles.local.json' in parts: raise ValueError('private inputs are tracked')
 print(json.dumps({'head':head,'tree':tree,'clean':not bool(status)}))
elif sys.argv[1]=='bundle':
 git('bundle','create','/export/source.bundle','HEAD')
 os.chmod('/export/source.bundle',0o644)
else: raise ValueError('unknown action')
`

const rebaseHelper = `import json,os,pathlib,stat,subprocess,sys
env={"PATH":"/usr/local/bin:/usr/bin:/bin","HOME":"/tmp","GIT_CONFIG_NOSYSTEM":"1","GIT_CONFIG_GLOBAL":"/dev/null","GIT_TERMINAL_PROMPT":"0","GIT_OPTIONAL_LOCKS":"0","GIT_NO_REPLACE_OBJECTS":"1"}
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
def git(*args, check=True):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat','-c','commit.gpgSign=false','-c','user.name=SDLC Restack','-c','user.email=sdlc-restack@example.invalid',*args],cwd=root,env=env,check=check,capture_output=True)
old=sys.argv[1]
expected=sys.argv[2]
marker='refs/sdlc/rebase/'+old+'/'+expected
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
if git('status','--porcelain','--untracked-files=no').stdout: raise ValueError('workspace has tracked changes')
prior=git('rev-parse','--verify',marker,check=False)
if prior.returncode == 0:
 original=prior.stdout.decode().strip()
 # This proves a previously started controller rebase finished on the
 # recorded new base with the same implementation patch. The resulting tree
 # may differ because the new base contains upstream changes.
 current=git('rev-parse','--verify','HEAD').stdout.decode().strip()
 if current == original:
  # The marker was durably written but the process died before Git began.
  # Continue through the normal validation and rebase path below.
  pass
 else:
  original_patch=git('diff','--binary',old,original).stdout
  replayed_patch=git('diff','--binary',expected,'HEAD').stdout
  if git('merge-base','--is-ancestor',expected,'HEAD',check=False).returncode == 0 and original_patch == replayed_patch:
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
git('update-ref',marker,'HEAD')
result=git('rebase','--onto',new,old,check=False)
if result.returncode:
 paths=git('diff','--name-only','--diff-filter=U','-z',check=False).stdout.decode().split('\0')
 paths=[p for p in paths if p and not p.startswith('/') and '..' not in pathlib.PurePosixPath(p).parts][:256]
 if paths:
  print(json.dumps({'conflict':True,'paths':paths})); sys.exit(0)
 raise RuntimeError('rebase failed')
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
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", repositoryHelper, action)
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
