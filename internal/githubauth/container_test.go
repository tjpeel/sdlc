package githubauth

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func TestVerificationRequiresExplicitConnectedMode(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, connected := range []bool{false, true} {
		docker.result = "stored"
		if connected {
			docker.result = "verified"
		}
		// Fake the helper response without running any connected command.
		if _, err := manager.Status(context.Background(), connected); err != nil {
			t.Fatal(err)
		}
		var args []string
		for _, call := range docker.calls {
			if call[0] == "run" {
				args = call
			}
		}
		want := "status"
		network := "none"
		if connected {
			want = "verify"
			network = "bridge"
		}
		if args[len(args)-1] != want || !has(args, network) || !has(args, "1000:1000") {
			t.Fatal("incorrect verification mode")
		}
	}
}

func TestAcquireHoldsGithubLeaseUntilClose(t *testing.T) {
	manager, _ := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	session, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lock, err := filelock.AcquireContext(ctx, filepath.Join(manager.Runtime.Directory, "github-auth.lock"), filelock.Exclusive, nil)
	if err == nil {
		lock.Close()
		t.Fatal("session did not retain auth lease")
	}
	if session.ImageID != testImage || !strings.HasSuffix(session.Volume, "-github") {
		t.Fatal("incorrect session storage")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = filelock.AcquireContext(context.Background(), filepath.Join(manager.Runtime.Directory, "github-auth.lock"), filelock.Exclusive, nil)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}

func TestHelperRejectsUnsafeStorageAndEmptyNativeLogout(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script := helper + `
import tempfile
from unittest.mock import patch
from types import SimpleNamespace
import contextlib
import io
with tempfile.TemporaryDirectory() as tmp:
 directory=Path(tmp)
 directory.chmod(0o700)
 assert storage(directory) is False
 target=directory/'hosts.yml'
 target.write_text('offline disposable data')
 target.chmod(0o600)
 assert storage(directory) is True
 with patch('__main__.login_process', side_effect=AssertionError('configured cache must not start native login')):
  assert run('login', directory) == 1
 for empty in ('', '{}\n', ' \n\t', '  {} \n'):
  target.write_text(empty)
  assert storage(directory) is False
 target.write_text('offline disposable data')
 response=SimpleNamespace(returncode=0, stdout=b'{"id":99,"name":"Example/Project","push":true}')
 with patch('subprocess.run', return_value=response) as native, contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('repository', directory, 'example/project') == 0
  assert json.loads(output.getvalue()) == {'id':99,'name':'Example/Project','push':True}
  assert native.call_args.args[0] == ['gh','api','--hostname','github.com','repos/example/project','--jq','{id:.id,name:.full_name,push:.permissions.push}']
  assert 'GH_TOKEN' not in native.call_args.kwargs['env']
 with patch('subprocess.run', side_effect=AssertionError('invalid repository must not call gh')):
  for invalid in ('../repo','owner/../repo','owner/repo?redirect','owner/repo\n','owner/.','--owner/repo'):
   assert run('repository', directory, invalid) == 1
 target.chmod(0o644)
 try: storage(directory)
 except ValueError: pass
 else: raise AssertionError('permissive file accepted')
 target.unlink()
 extra=directory/'aliases.yml'
 extra.write_text('offline config')
 extra.chmod(0o600)
 try: storage(directory)
 except ValueError: pass
 else: raise AssertionError('arbitrary gh configuration accepted')
 extra.unlink()
 target.symlink_to('/dev/null')
 try: storage(directory)
 except ValueError: pass
 else: raise AssertionError('symlink accepted')
 for login in (False,True):
  env=environment(login)
  assert not any(key in env for key in ('GH_TOKEN','GITHUB_TOKEN','GH_ENTERPRISE_TOKEN','GITHUB_ENTERPRISE_TOKEN','HTTP_PROXY'))
  assert env['GH_CONFIG_DIR']=='/github-auth'
  assert ('GH_PROMPT_DISABLED' in env) != login
`
	// Avoid executing the helper main guard in this offline harness.
	script = strings.Replace(script, "if __name__ == '__main__':", "if False:", 1)
	command := exec.Command(python, "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline storage checks failed: %v\n%s", err, output)
	}
}

func TestFixedStatusResponse(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"verified", "failed", "missing", "invalid"} {
		docker.result = value
		result, err := manager.Status(context.Background(), true)
		if err != nil || result != value {
			t.Fatal(result, err)
		}
	}
	encoded, _ := json.Marshal(labels(testID, "github"))
	if strings.Contains(string(encoded), "provider-auth") {
		t.Fatal("GitHub volume shares provider kind")
	}
}

func TestIdentityUsesReadonlySelectedVolumeAndRedactsFailures(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	session, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	docker.result = `{"id":123,"login":"example-user"}`
	identity, err := session.Identity(context.Background())
	if err != nil || identity.ID != 123 || identity.Login != "example-user" {
		t.Fatal(identity, err)
	}
	for _, args := range docker.calls {
		if args[0] != "run" || args[len(args)-1] != "identity" {
			continue
		}
		if !has(args, "bridge") || !has(args, "1000:1000") {
			t.Fatal("identity did not use bounded connected container")
		}
		for index, arg := range args {
			if arg == "--mount" && !strings.HasSuffix(args[index+1], ",readonly") {
				t.Fatal("identity credential volume is writable")
			}
		}
	}
	docker.result = `{"id":123,"login":"private-diagnostic\n"}`
	if _, err := session.Identity(context.Background()); err == nil || strings.Contains(err.Error(), "private-diagnostic") {
		t.Fatal("invalid identity accepted or leaked")
	}
}

func TestRepositoryIdentityUsesBoundedReadonlyContainerAndCleansUp(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	session, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	docker.result = `{"id":99,"name":"Example/Project","push":true}`
	docker.leftover = true
	repository, err := session.Repository(context.Background(), "example/project")
	if err != nil || repository.ID != 99 || repository.Name != "Example/Project" || !repository.Push {
		t.Fatal(repository, err)
	}
	if docker.leftover {
		t.Fatal("repository container cleanup was omitted")
	}
	found := false
	for _, args := range docker.calls {
		if args[0] != "run" || len(args) < 2 || args[len(args)-2] != "repository" {
			continue
		}
		found = true
		if args[len(args)-1] != "example/project" || !has(args, "bridge") || !has(args, "1000:1000") {
			t.Fatal("unsafe repository operation")
		}
		for index, arg := range args {
			if arg == "--mount" && !strings.HasSuffix(args[index+1], ",readonly") {
				t.Fatal("repository credential mount writable")
			}
		}
	}
	if !found {
		t.Fatal("native repository helper was not used")
	}
	for _, response := range []string{`{"id":0,"name":"example/project","push":true}`, `{"id":99,"name":"other/project","push":true}`, `{"id":99,"name":"example/project","push":false}`, `{"id":99,"name":"example/project","push":"true"}`, `{"id":99,"name":"example/project/extra","push":true}`, `private-diagnostic`, strings.Repeat("x", 1025)} {
		docker.result = response
		if _, err := session.Repository(context.Background(), "example/project"); err == nil || strings.Contains(err.Error(), "private-diagnostic") {
			t.Fatal("invalid repository response accepted or leaked", err)
		}
	}
	for _, invalid := range []string{"../repo", "owner/../repo", "owner/repo?redirect", "owner/repo\n", "owner/.", "--owner/repo"} {
		before := len(docker.calls)
		if _, err := session.Repository(context.Background(), invalid); err == nil {
			t.Fatal("invalid repository accepted")
		}
		if len(docker.calls) != before {
			t.Fatal("invalid repository reached Docker")
		}
	}
	docker.result = `{"id":99,"name":"example/project","push":true}`
	docker.cleanupFailed = true
	if _, err := session.Repository(context.Background(), "example/project"); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("cleanup failure ignored or leaked", err)
	}
}
