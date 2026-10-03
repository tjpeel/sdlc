package workrun

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// Explicit opt-in: these checks use a local engine and a privileged, disposable
// test daemon. They never mount authentication state or connect provider clients.
func TestOfflineDockerExecutionBoundaries(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_DOCKER_TESTS=1 to exercise the local Docker runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	flags := `import subprocess
cases=[(['codex','exec','--help'],['--output-schema','--json','--ignore-rules']),(['codex','exec','resume','--help'],['--output-schema','--json','--ignore-rules']),(['claude','--help'],['--forward-subagent-text','--effort','--json-schema','--setting-sources'])]
for command,required in cases:
 output=subprocess.check_output(command,text=True)
 assert all(flag in output for flag in required), command
print('Native CLI flags verified without credentials or network')`
	if _, err := runtime.Docker.Output(ctx, "run", "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,mode=1777", "--env", "HOME=/tmp", "--entrypoint", "python3", state.ImageID, "-c", flags); err != nil {
		t.Fatal(err)
	}
	_, launch := sourceFixture(t)
	workspace := filepath.Join(realTemp(t), "workspace")
	plan, err := Capture(ctx, launch, workspace, "offline-smoke", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	repository := DockerRepository{Runtime: runtime, ImageID: state.ImageID}
	revision, err := repository.Inspect(ctx, workspace)
	if err != nil || !revision.Clean || revision.Head != plan.StartingSHA {
		t.Fatalf("source inspection: %+v %v", revision, err)
	}
	bundle := filepath.Join(filepath.Dir(workspace), "export.bundle")
	if err := repository.Bundle(ctx, workspace, bundle); err != nil {
		t.Fatal(err)
	}
	if heads, err := isolatedGit(ctx, filepath.Dir(workspace), "bundle", "list-heads", bundle); err != nil || !bytes.Contains([]byte(heads), []byte(plan.StartingSHA)) {
		t.Fatalf("bundle export: %q %v", heads, err)
	}
	directory := filepath.Dir(workspace)
	if _, err := isolatedGit(ctx, directory, "clone", "--bare", "--template=", bundle, filepath.Join(directory, "publication.git")); err != nil {
		t.Fatal(err)
	}
	review, err := PrepareReview(ctx, Journal{Plan: plan, Workspace: workspace, Publication: Publication{HeadSHA: plan.StartingSHA}}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if revision, err := repository.Inspect(ctx, review); err != nil || revision.Head != plan.StartingSHA {
		t.Fatalf("reviewer snapshot is unreadable from UID 1000: %+v %v", revision, err)
	}
	check := `import os,pathlib
assert os.getuid()==1000
assert pathlib.Path('/workspace/README.md').read_text()=='original\n'
assert not pathlib.Path('/workspace/.git').exists()
assert not pathlib.Path('/provider-auth').exists()
assert not pathlib.Path('/var/run/docker.sock').exists()
assert not pathlib.Path('/run/sdlc/docker.sock').exists()
assert not pathlib.Path(os.environ['CODEX_HOME']).exists()
assert not pathlib.Path(os.environ['CLAUDE_CONFIG_DIR']).exists()
print('Credential-free unit worker passed')`
	var output bytes.Buffer
	checker := DockerChecker{Runtime: runtime, ImageID: state.ImageID}
	if err := checker.Check(ctx, workspace, [][]string{{"python3", "-c", check}}, &output); err != nil {
		t.Fatalf("unit worker: %v\n%s", err, &output)
	}
	// Build a shell/Python image from the check worker's existing files. No image
	// pull or package restore is needed inside the disposable integration daemon.
	integration := `import _socket,io,os,pathlib,re,subprocess,sys,sysconfig,tarfile
assert os.environ['DOCKER_HOST']=='unix:///run/sdlc/docker.sock'
assert os.environ['TESTCONTAINERS_HOST_OVERRIDE']=='localhost'
assert not pathlib.Path('/provider-auth').exists()
assert subprocess.check_output(['docker','info','--format','{{.Driver}}'],text=True).strip()=='overlay2'
files={'/bin/sh',sys.executable}
if getattr(_socket,'__file__',None): files.add(_socket.__file__)
for executable in list(files):
 files.update(re.findall(r'/[^\s()]+',subprocess.check_output(['ldd',executable],text=True)))
archive=io.BytesIO()
with tarfile.open(fileobj=archive,mode='w',dereference=True) as tar:
 for path in sorted(files): tar.add(path,arcname=path.lstrip('/'),recursive=False)
 encodings=pathlib.Path(sysconfig.get_path('stdlib'))/'encodings'
 tar.add(encodings,arcname=str(encodings).lstrip('/'))
subprocess.run(['docker','import','-','sdlc-offline-smoke:local'],input=archive.getvalue(),check=True)
build=pathlib.Path('/workspace/offline-build')
build.mkdir()
(build/'Dockerfile').write_text('FROM sdlc-offline-smoke:local\nRUN '+sys.executable+' -S -c "import _socket; s=_socket.socket(_socket.AF_UNIX); s.bind(\'/build-service.sock\')"\nRUN test -S /build-service.sock && echo second > /second\n')
subprocess.run(['docker','build','--network','none','--tag','sdlc-offline-built:local',str(build)],check=True)
subprocess.run(['docker','run','--rm','--pull','never','--network','none','--entrypoint','/bin/sh','sdlc-offline-built:local','-c','test -S /build-service.sock && test -f /second && echo Nested socket-layer image build passed'],check=True)
subprocess.run(['docker','run','--rm','--pull','never','--network','none','--mount','type=bind,src=/workspace,dst=/data,readonly','--entrypoint','/bin/sh','sdlc-offline-smoke:local','-c','test -r /data/README.md && echo Nested Docker service and workspace bind passed'],check=True)
print('Disposable Docker integration worker passed')`
	checker.DockerTests = true
	output.Reset()
	if err := checker.Check(ctx, workspace, [][]string{{"python3", "-c", integration}}, &output); err != nil {
		t.Fatalf("integration worker: %v\n%s", err, &output)
	}
	t.Log(output.String())
}
