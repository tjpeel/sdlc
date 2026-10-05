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

type signingKeysDocker struct {
	*fakeDocker
	response string
	err      error
}

func (docker *signingKeysDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	output, err := docker.fakeDocker.Output(ctx, args...)
	if len(args) > 2 && args[0] == "run" && args[len(args)-2] == "signing-keys" {
		return []byte(docker.response), docker.err
	}
	return output, err
}

func TestSigningKeysUseSelectedReadonlyVolumeAndRejectInvalidResponses(t *testing.T) {
	manager, base := fixture(t)
	manager.Profile = "selected"
	docker := &signingKeysDocker{fakeDocker: base, response: `["ssh-ed25519 AAAA disposable comment"]`}
	manager.Docker = docker
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	session, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	docker.leftover = true
	keys, err := session.SigningKeys(context.Background(), "Example-User")
	if err != nil || len(keys) != 1 || keys[0] != "ssh-ed25519 AAAA disposable comment" || docker.leftover {
		t.Fatal("valid key lookup or cleanup failed", keys, err)
	}
	found := false
	for _, args := range docker.calls {
		if len(args) < 2 || args[0] != "run" || args[len(args)-2] != "signing-keys" {
			continue
		}
		found = true
		if args[len(args)-1] != "Example-User" || authVolume(args) != session.Volume {
			t.Fatal("lookup changed selected account or login argument")
		}
		for _, value := range []string{"bridge", "1000:1000", "--read-only", "ALL", "no-new-privileges", "HTTP_PROXY=", "--log-driver", "none", testImage} {
			if !has(args, value) {
				t.Fatalf("lookup missing isolation setting %s", value)
			}
		}
		for i, arg := range args {
			if arg == "--mount" && args[i+1] != "type=volume,src="+session.Volume+",dst=/github-auth,volume-nocopy,readonly" {
				t.Fatal("lookup did not mount only selected readonly cache")
			}
		}
	}
	if !found {
		t.Fatal("lookup did not invoke native helper")
	}
	for _, invalid := range []string{"", "-owner", "owner-", "owner--name", "../owner", "owner?x", "owner\n", "owner/name", strings.Repeat("a", 40)} {
		before := len(docker.calls)
		if _, err := session.SigningKeys(context.Background(), invalid); err == nil || len(docker.calls) != before {
			t.Fatal("invalid login accepted or reached Docker")
		}
	}
	responses := []string{`null`, `{}`, `[null]`, `[123]`, `[""]`, `["ssh-ed25519 bad-base64!"]`, `["ssh-ed25519 AAAA\nprivate-diagnostic"]`, `private-diagnostic`, `["ssh-ed25519 AAAA"] trailing`, strings.Repeat("x", signingKeysOutputLimit+1)}
	oversizedKey, _ := json.Marshal([]string{"ssh-ed25519 AAAA " + strings.Repeat("x", signingKeyLimit)})
	responses = append(responses, string(oversizedKey))
	tooMany, _ := json.Marshal(make([]string, 1000))
	responses = append(responses, string(tooMany))
	for _, response := range responses {
		docker.response = response
		if _, err := session.SigningKeys(context.Background(), "example"); err == nil || strings.Contains(err.Error(), "private-diagnostic") {
			t.Fatal("invalid key response accepted or leaked", err)
		}
	}
	docker.response = `[]`
	if keys, err := session.SigningKeys(context.Background(), "example"); err != nil || len(keys) != 0 {
		t.Fatal("empty public key list rejected", err)
	}
	docker.err = context.Canceled
	if _, err := session.SigningKeys(context.Background(), "example"); err == nil {
		t.Fatal("failed lookup accepted")
	}
	docker.err = nil
	docker.cleanupFailed = true
	if _, err := session.SigningKeys(context.Background(), "example"); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("failed cleanup ignored or leaked")
	}
}

func TestSigningKeysHelperPaginationValidationAndArgumentsOffline(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script := strings.Replace(helper, "if __name__ == '__main__':", "if False:", 1) + `
from unittest.mock import patch
from types import SimpleNamespace
import contextlib
import io
import tempfile

def response(keys, code=0):
 return SimpleNamespace(returncode=code, stdout=json.dumps(keys).encode())
key='ssh-ed25519 AAAA disposable test'
with tempfile.TemporaryDirectory() as tmp:
 directory=Path(tmp)
 directory.chmod(0o700)
 target=directory/'hosts.yml'
 target.write_text('offline disposable data')
 target.chmod(0o600)
 pages=[response([key]*100),response([key])]
 with patch('subprocess.run', side_effect=pages) as native, contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('signing-keys', directory, login='Example-User') == 0
  assert json.loads(output.getvalue()) == [key]*101
  assert native.call_count == 2
  for page,call in enumerate(native.call_args_list, 1):
   assert call.args[0] == ['gh','api','--hostname','github.com','users/Example-User/ssh_signing_keys?per_page=100&page='+str(page),'--jq','[.[] | .key]']
   assert call.kwargs['env'] == environment()
   assert call.kwargs['cwd'] == '/home/node'
   assert call.kwargs['timeout'] == 30
 with patch('subprocess.run', return_value=response([])) as native, contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('signing-keys', directory, login='example') == 0
  assert json.loads(output.getvalue()) == []
  assert native.call_count == 1
 with patch('subprocess.run', return_value=response([key]*100)) as native, contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('signing-keys', directory, login='example') == 1
  assert native.call_count == 10
  assert output.getvalue() == ''
 for bad in (response([],1),response(None),response({}),response([None]),response([123]),response(['']),response(['ssh-ed25519 bad!']),response(['ssh-ed25519 AAAA\nprivate-diagnostic']),response(['ssh-ed25519 AAAA '+ 'x'*SIGNING_KEY_LIMIT]),response([key]*101),SimpleNamespace(returncode=0,stdout=b'private-diagnostic'),SimpleNamespace(returncode=0,stdout=b'x'*(SIGNING_KEYS_OUTPUT_LIMIT+1))):
  with patch('subprocess.run', return_value=bad), contextlib.redirect_stdout(io.StringIO()) as output:
   try: result=run('signing-keys', directory, login='example')
   except (ValueError,TypeError): result=1
   assert result == 1
   assert output.getvalue() == ''
 large='ssh-ed25519 AAAA '+'x'*7000
 with patch('subprocess.run', return_value=response([large]*100)) as native, contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('signing-keys', directory, login='example') == 1
  assert native.call_count == 2
  assert output.getvalue() == ''
 with patch('subprocess.run', side_effect=[response([key]*100),response([],1)]), contextlib.redirect_stdout(io.StringIO()) as output:
  assert run('signing-keys', directory, login='example') == 1
  assert output.getvalue() == ''
 with patch('__main__.storage', side_effect=AssertionError('invalid arguments touched storage')):
  for invalid in ('','-owner','owner-','owner--name','../owner','owner?x','owner\n','owner/name','a'*40):
   assert run('signing-keys', directory, login=invalid) == 1
  assert run('signing-keys', directory, repository='example/project', login='example') == 1
  assert run('identity', directory, login='example') == 1
 for argv in (['helper','signing-keys'],['helper','signing-keys','example','extra'],['helper','identity','example']):
  with patch('sys.argv',argv), patch('__main__.run',side_effect=AssertionError('bad arity called run')):
   assert main() == 1
 for action,argument,want in (('signing-keys','example',{'repository':None,'login':'example'}),('repository','example/project',{'repository':'example/project','login':None}),('identity',None,{'repository':None,'login':None})):
  argv=['helper',action]+([argument] if argument else [])
  with patch('sys.argv',argv), patch('__main__.run',return_value=0) as invoke:
   assert main() == 0
   assert invoke.call_args.args == (action,)
   assert invoke.call_args.kwargs == want
 with patch('sys.argv',['helper','signing-keys','example']), patch('__main__.run',side_effect=ValueError('private-diagnostic')), contextlib.redirect_stderr(io.StringIO()) as output:
  assert main() == 1
  assert 'private-diagnostic' not in output.getvalue()
`
	if output, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("offline signing key checks failed: %v\n%s", err, output)
	}
}
