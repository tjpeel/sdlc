package workrun

import (
	"context"
	"fmt"
	"os"
)

const externalPRImportHelper = `import os,pathlib,stat,subprocess,sys
root=pathlib.Path('/workspace')
if not stat.S_ISDIR((root/'.git').lstat().st_mode): raise ValueError('invalid Git directory')
env={'PATH':'/usr/local/bin:/usr/bin:/bin','HOME':'/tmp','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_TERMINAL_PROMPT':'0','GIT_NO_REPLACE_OBJECTS':'1'}
def git(*args):
 return subprocess.run(['git','-c','safe.directory=/workspace','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','core.pager=cat',*args],cwd=root,env=env,check=True,capture_output=True).stdout.decode().strip()
old,tree,parent,incoming,merged,branch=sys.argv[1:]
ref='refs/heads/'+branch
head=git('rev-parse','HEAD')
if git('status','--porcelain','--untracked-files=normal','--ignore-submodules=all'): raise ValueError('workspace is dirty')
if head == merged:
 symbolic=subprocess.run(['git','-c','safe.directory=/workspace','symbolic-ref','-q','HEAD'],cwd=root,env=env,capture_output=True)
 if symbolic.returncode == 0 and symbolic.stdout.decode().strip() != ref: raise ValueError('workspace branch changed')
 if symbolic.returncode not in (0,1): raise ValueError('cannot inspect workspace branch')
 current=git('rev-parse','--verify',ref)
 if current == old: git('update-ref',ref,merged,old)
 elif current != merged: raise ValueError('workspace branch changed')
 git('symbolic-ref','HEAD',ref)
 sys.exit(0)
if head != old or git('rev-parse','HEAD^{tree}') != tree: raise ValueError('workspace changed')
if git('symbolic-ref','HEAD') != ref: raise ValueError('workspace branch changed')
git('bundle','verify','/newbase.bundle')
git('-c','protocol.file.allow=always','fetch','--no-tags','/newbase.bundle','+refs/sdlc/snapshot:refs/sdlc/external-pr/'+merged)
if git('rev-parse','refs/sdlc/external-pr/'+merged+'^{commit}') != merged: raise ValueError('snapshot mismatch')
if git('show','-s','--format=%P',merged).split() != [parent,incoming]: raise ValueError('merge parents differ')
for entry in git('ls-tree','-r','-z',merged).split('\0'):
 if not entry: continue
 metadata,path=entry.split('\t',1)
 mode,kind,oid=metadata.split()
 parts=path.lower().split('/')
 checked=parts[:-1] if parts[-1] == '.env.example' else parts
 if mode not in ('100644','100755') or kind != 'blob' or path.startswith('/') or '..' in path: raise ValueError('unsupported snapshot path')
 if any(p.startswith('.env.') or p in ('.git','.secrets','.ssh','.aws','.claude','.t3','profiles.local.json','credentials','credentials.json','.env') for p in checked) or '.codex/auth.json' in path.lower() or path.lower().startswith('.sdlc/work/'): raise ValueError('private inputs are tracked')
git('checkout','--no-overwrite-ignore','--detach',merged)
if git('rev-parse','HEAD') != merged or git('status','--porcelain','--untracked-files=normal','--ignore-submodules=all'): raise ValueError('checkout changed')
git('update-ref',ref,merged,old)
git('symbolic-ref','HEAD',ref)
`

func (repository DockerRepository) ImportExternalPR(ctx context.Context, workspace, bundle string, old Revision, b ExternalPRBoundary, merged string) error {
	if !old.Clean || !objectID.MatchString(old.Head) || !objectID.MatchString(old.Tree) || b.validate() != nil || !objectID.MatchString(merged) {
		return fmt.Errorf("invalid external PR import boundary")
	}
	info, err := os.Lstat(bundle)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBundleBytes {
		return fmt.Errorf("invalid external PR snapshot bundle")
	}
	state, err := repository.Runtime.Status(ctx)
	if err != nil || repository.ImageID != "" && state.ImageID != repository.ImageID {
		return fmt.Errorf("runtime changed since launch")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", mount(workspace, "/workspace", false), "--mount", mount(bundle, "/newbase.bundle", true)}
	args = checkContainerEnvironment(args)
	args = append(args, "--entrypoint", "python3", state.ImageID, "-c", externalPRImportHelper, old.Head, old.Tree, b.ParentHeadSHA, b.IncomingHeadSHA, merged, b.ParentBranch)
	if _, err := repository.Runtime.Docker.Output(ctx, args...); err != nil {
		return fmt.Errorf("credential-free external PR import failed: %w", err)
	}
	return nil
}
