package workrun

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// Rebase a captured implementation onto an offline bundle. The new base
	// changes README while the implementation contributes the selected ticket,
	// so the resulting tree must differ from the old candidate tree.
	if _, err := SafeGit(ctx, launch.Root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launch.Root, "README.md"), []byte("upstream change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, launch.Root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, launch.Root, "commit", "--no-verify", "-m", "Upstream change"); err != nil {
		t.Fatal(err)
	}
	newBase, err := SafeGit(ctx, launch.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	newBase = strings.TrimSpace(newBase)
	if _, err := SafeGit(ctx, launch.Root, "update-ref", "refs/sdlc/snapshot", newBase); err != nil {
		t.Fatal(err)
	}
	baseBundle := filepath.Join(filepath.Dir(workspace), "newbase.bundle")
	if _, err := SafeGit(ctx, launch.Root, "bundle", "create", baseBundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(baseBundle, 0600); err != nil {
		t.Fatal(err)
	}
	oldCandidate := plan.StartingSHA
	// Simulate a controller dying after it durably recorded the rebase marker
	// but before it could invoke `git rebase`.
	if _, err := SafeGit(ctx, workspace, "update-ref", "refs/sdlc/rebase/"+launch.Head+"/"+newBase, oldCandidate); err != nil {
		t.Fatal(err)
	}
	result, err := repository.Rebase(ctx, workspace, baseBundle, launch.Head, newBase)
	if err != nil || result.Conflict {
		t.Fatalf("offline rebase: %+v %v", result, err)
	}
	updated, err := repository.Inspect(ctx, workspace)
	if err != nil || !updated.Clean || updated.Head == oldCandidate {
		t.Fatalf("rebase did not create a clean new candidate: %+v %v", updated, err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "README.md")); err != nil || string(data) != "upstream change\n" {
		t.Fatalf("upstream base change lost: %v %q", err, data)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, launch.Ticket)); err != nil || string(data) != "disposable ticket\n" {
		t.Fatalf("ticket change lost: %v %q", err, data)
	}
	// A controller crash after the container completed but before Save replays
	// the marker proof and does not re-run Git rebase.
	if replay, err := repository.Rebase(ctx, workspace, baseBundle, launch.Head, newBase); err != nil || replay.Conflict {
		t.Fatalf("offline rebase replay: %+v %v", replay, err)
	}
	// A conflicting restack reports only relative paths and leaves the original
	// workspace for the implementation session to resolve and continue.
	conflictRoot, conflictLaunch := sourceFixture(t)
	conflictWorkspace := filepath.Join(realTemp(t), "workspace")
	conflictPlan, err := Capture(ctx, conflictLaunch, conflictWorkspace, "offline-conflict", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflictWorkspace, "README.md"), []byte("ticket change\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictWorkspace, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictWorkspace, "commit", "--no-verify", "-m", "Ticket README change"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflictRoot, "README.md"), []byte("upstream conflict\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictRoot, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictRoot, "commit", "--no-verify", "-m", "Upstream conflicting change"); err != nil {
		t.Fatal(err)
	}
	conflictBase, err := SafeGit(ctx, conflictRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	conflictBase = strings.TrimSpace(conflictBase)
	if _, err := SafeGit(ctx, conflictRoot, "update-ref", "refs/sdlc/snapshot", conflictBase); err != nil {
		t.Fatal(err)
	}
	conflictBundle := filepath.Join(filepath.Dir(conflictWorkspace), "conflict.bundle")
	if _, err := SafeGit(ctx, conflictRoot, "bundle", "create", conflictBundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(conflictBundle, 0600); err != nil {
		t.Fatal(err)
	}
	conflict, err := repository.Rebase(ctx, conflictWorkspace, conflictBundle, conflictLaunch.Head, conflictBase)
	if err != nil || !conflict.Conflict || len(conflict.Paths) != 1 || conflict.Paths[0] != "README.md" {
		t.Fatalf("expected bounded conflict: %+v %v", conflict, err)
	}
	if replay, err := repository.Rebase(ctx, conflictWorkspace, conflictBundle, conflictLaunch.Head, conflictBase); err != nil || !replay.Conflict || len(replay.Paths) != 1 || replay.Paths[0] != "README.md" {
		t.Fatalf("unfinished recorded rebase was not safely replayed: %+v %v", replay, err)
	}
	if err := os.WriteFile(filepath.Join(conflictWorkspace, "README.md"), []byte("resolved\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictWorkspace, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, conflictWorkspace, "-c", "core.editor=true", "rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	if head, err := SafeGit(ctx, conflictWorkspace, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(head) == conflictPlan.StartingSHA {
		status, _ := SafeGit(ctx, conflictWorkspace, "status", "--porcelain", "--untracked-files=normal")
		t.Fatalf("conflict recovery failed: %q %v %q", head, err, status)
	}
	// Rebase only the child ticket range after its parent was squash-merged into
	// main. This is the stack shape used when a parent PR lands independently.
	squashRoot, squashLaunch := sourceFixture(t)
	if _, err := SafeGit(ctx, squashRoot, "checkout", "-b", "parent", squashLaunch.Head); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, squashRoot, "parent.txt", "parent work\n")
	if _, err := SafeGit(ctx, squashRoot, "add", "parent.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, squashRoot, "commit", "--no-verify", "-m", "Parent work"); err != nil {
		t.Fatal(err)
	}
	oldParent, err := SafeGit(ctx, squashRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	oldParent = strings.TrimSpace(oldParent)
	if _, err := SafeGit(ctx, squashRoot, "checkout", "-b", "child", oldParent); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, squashRoot, "child.txt", "child-only work\n")
	if _, err := SafeGit(ctx, squashRoot, "add", "child.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, squashRoot, "commit", "--no-verify", "-m", "Child work"); err != nil {
		t.Fatal(err)
	}
	squashWorkspace := filepath.Join(realTemp(t), "workspace")
	if _, err := isolatedGit(ctx, filepath.Dir(squashWorkspace), "clone", "--no-local", "--no-hardlinks", squashRoot, squashWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedGit(ctx, squashWorkspace, "checkout", "child"); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedGit(ctx, squashWorkspace, "remote", "remove", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, squashRoot, "checkout", "main"); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, squashRoot, "parent.txt", "parent work\n")
	if _, err := SafeGit(ctx, squashRoot, "add", "parent.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, squashRoot, "commit", "--no-verify", "-m", "Squash parent work"); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, squashRoot, "upstream.txt", "new upstream\n")
	if _, err := SafeGit(ctx, squashRoot, "add", "upstream.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(ctx, squashRoot, "commit", "--no-verify", "-m", "Independent upstream"); err != nil {
		t.Fatal(err)
	}
	newMain, err := SafeGit(ctx, squashRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	newMain = strings.TrimSpace(newMain)
	if _, err := SafeGit(ctx, squashRoot, "update-ref", "refs/sdlc/snapshot", newMain); err != nil {
		t.Fatal(err)
	}
	squashBundle := filepath.Join(filepath.Dir(squashWorkspace), "squash-main.bundle")
	if _, err := SafeGit(ctx, squashRoot, "bundle", "create", squashBundle, "refs/sdlc/snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(squashBundle, 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := repository.Rebase(ctx, squashWorkspace, squashBundle, oldParent, newMain); err != nil || result.Conflict {
		t.Fatalf("squash-parent rebase: %+v %v", result, err)
	}
	if _, err := isolatedGit(ctx, squashWorkspace, "merge-base", "--is-ancestor", newMain, "HEAD"); err != nil {
		t.Fatal("new main not ancestor")
	}
	if _, err := isolatedGit(ctx, squashWorkspace, "merge-base", "--is-ancestor", oldParent, "HEAD"); err == nil {
		t.Fatal("old parent still in rebased history")
	}
	rangeCommits, err := isolatedGit(ctx, squashWorkspace, "rev-list", newMain+"..HEAD")
	if err != nil || len(strings.Fields(rangeCommits)) != 1 {
		t.Fatalf("rebased range includes parent duplication: %q %v", rangeCommits, err)
	}
	for _, file := range []struct{ path, want string }{{"parent.txt", "parent work\n"}, {"upstream.txt", "new upstream\n"}, {"child.txt", "child-only work\n"}} {
		data, err := os.ReadFile(filepath.Join(squashWorkspace, file.path))
		if err != nil || string(data) != file.want {
			t.Fatalf("squash rebase lost %s: %v %q", file.path, err, data)
		}
	}
	if result, err := repository.Rebase(ctx, squashWorkspace, squashBundle, oldParent, newMain); err != nil || result.Conflict {
		t.Fatalf("squash-parent replay: %+v %v", result, err)
	}
	check := `import os,pathlib
assert os.getuid()==1000
assert pathlib.Path('/workspace/README.md').read_text()=='upstream change\n'
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
