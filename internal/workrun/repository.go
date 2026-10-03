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
