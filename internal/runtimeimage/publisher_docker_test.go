package runtimeimage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This opt-in builds public runtime sources and exercises local binaries only.
// It never mounts existing account state or connects a provider client.
func TestPublisherRuntimeDockerImage(t *testing.T) {
	if os.Getenv("SDLC_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_DOCKER_TESTS=1 to build and test the publisher image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Minute)
	defer cancel()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate runtime source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	directory, cleanup, err := buildContext(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	log, err := os.CreateTemp("", "sdlc-publisher-docker-test-*.log")
	if err != nil {
		t.Fatal("cannot create private Docker diagnostics")
	}
	defer func() {
		log.Close()
		if !t.Failed() {
			os.Remove(log.Name())
		}
	}()
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	identifier := hex.EncodeToString(token[:])
	tag := "sdlc-publisher-test:" + identifier
	run := func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "docker", args...)
		command.Stdin = input
		command.Stderr = log
		var output bytes.Buffer
		command.Stdout = &output
		err := command.Run()
		if err != nil {
			log.Write(output.Bytes())
			return nil, fmt.Errorf("Docker operation failed; private diagnostics: %s", log.Name())
		}
		return output.Bytes(), nil
	}
	imageBuilt := false
	var containers []string
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		for _, name := range containers {
			output, err := run(cleanupCtx, nil, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
			if err != nil {
				t.Errorf("test container cleanup inspection failed; private diagnostics: %s", log.Name())
				continue
			}
			if strings.TrimSpace(string(output)) == name {
				if _, err := run(cleanupCtx, nil, "rm", "--force", name); err != nil {
					t.Errorf("test container cleanup failed; private diagnostics: %s", log.Name())
				}
			}
		}
		if !imageBuilt {
			return
		}
		// The test tag is unique; removing it cannot retag or remove sdlc:local.
		if _, err := run(cleanupCtx, nil, "image", "rm", tag); err != nil {
			t.Errorf("test image cleanup failed; private diagnostics: %s", log.Name())
		}
	}()
	build := exec.CommandContext(ctx, "docker", "build", "--progress", "plain", "--tag", tag, directory)
	build.Stdout, build.Stderr = log, log
	t.Log("Building a separate publisher test image from the sanitized source closure")
	if err := build.Run(); err != nil {
		t.Fatalf("publisher image build failed; private diagnostics: %s", log.Name())
	}
	imageBuilt = true
	inspect, err := run(ctx, nil, "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(string(inspect))
	if !strings.HasPrefix(imageID, "sha256:") {
		t.Fatal("test image has no content identity")
	}
	containerRun := func(entrypoint string, args ...string) []string {
		name := fmt.Sprintf("sdlc-publisher-image-test-%s-%d", identifier, len(containers))
		containers = append(containers, name)
		base := []string{"run", "--name", name, "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "1g", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777", "--env", "HOME=/tmp", "--env", "GH_CONFIG_DIR=/tmp/github-empty", "--env", "GH_TOKEN=", "--env", "GITHUB_TOKEN=", "--env", "SSH_AUTH_SOCK=", "--entrypoint", entrypoint, imageID}
		return append(base, args...)
	}
	t.Run("baked trusted entrypoint", func(t *testing.T) {
		output, err := run(ctx, strings.NewReader("disposable-fake-secret"), containerRun("/usr/local/bin/sdlc-publisher")...)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(output, &response) != nil || response.Error == "" || bytes.Contains(output, []byte("disposable-fake-secret")) {
			t.Fatal("publisher startup did not return a bounded refusal")
		}
	})
	t.Run("offline signed publication", func(t *testing.T) {
		testOfflineSignedPublisher(t, ctx, tag, identifier, run, containerRun, log)
	})
	t.Run("official CLI flags without authentication", func(t *testing.T) {
		script := `import os,pathlib,subprocess
assert os.getuid()==1000
for path in ['/github-auth','/provider-auth','/var/run/docker.sock','/run/sdlc/docker.sock']:
 assert not pathlib.Path(path).exists(),path
cases=[(['/usr/local/bin/gh','auth','status','--help'],['--active','--hostname']),(['/usr/local/bin/gh','auth','switch','--help'],['--user','--hostname']),(['/usr/local/bin/gh','auth','git-credential','--help'],['git-credential']),(['/usr/local/bin/gh','pr','checks','--help'],['--json']),(['/usr/bin/git','--version'],['git version']),(['/usr/bin/ssh-keygen','-?'],['ssh-keygen'])]
for command,required in cases:
 result=subprocess.run(command,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
 assert all(flag in result.stdout for flag in required),command
print('Publisher binaries and official CLI flags verified offline')`
		if _, err := run(ctx, nil, containerRun("python3", "-c", script)...); err != nil {
			t.Fatal(err)
		}
	})
}

// Only the remote Git operations and gh are faked. Bundle validation, object
// integrity, commit-tree signing and signature verification use the image's Git.
func testOfflineSignedPublisher(t *testing.T, ctx context.Context, baseTag, identifier string, run func(context.Context, io.Reader, ...string) ([]byte, error), containerRun func(string, ...string) []string, log *os.File) {
	t.Helper()
	fixture := t.TempDir()
	if err := os.Chmod(fixture, 0777); err != nil {
		t.Fatal(err)
	}
	mount := func(source, target string) string { return "type=bind,src=" + source + ",dst=" + target }
	// Docker options must precede the image; containerRun's image is the last
	// argument when no command arguments have been supplied.
	withMounts := func(args []string, mounts ...string) []string {
		position := len(args) - 1
		image := args[position]
		additions := []string{}
		for _, value := range mounts {
			additions = append(additions, "--mount", value)
		}
		return append(append(args[:position], additions...), image)
	}
	prepare := `import json,os,pathlib,subprocess
root=pathlib.Path('/tmp/repository');root.mkdir()
env={'PATH':'/usr/bin:/bin','HOME':'/tmp','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_TERMINAL_PROMPT':'0'}
def git(*args):
 return subprocess.check_output(['/usr/bin/git','-C',str(root),'-c','core.hooksPath=/dev/null','-c','user.name=Disposable Example','-c','user.email=example@example.invalid','-c','commit.gpgsign=false',*args],env=env).decode().strip()
git('init','--template=','--initial-branch=main')
(root/'example.txt').write_text('original\n');git('add','example.txt');git('commit','-m','Disposable source')
base=git('rev-parse','HEAD')
(root/'example.txt').write_text('verified implementation\n');git('add','example.txt');git('commit','-m','Disposable implementation')
head=git('rev-parse','HEAD');tree=git('rev-parse','HEAD^{tree}');git('bundle','create','/fixture/source.bundle','HEAD')
subprocess.run(['/usr/bin/ssh-keygen','-q','-t','ed25519','-N','','-C','disposable@example.invalid','-f','/fixture/disposable-key'],env=env,check=True)
public=' '.join(subprocess.check_output(['/usr/bin/ssh-keygen','-y','-f','/fixture/disposable-key'],env=env).decode().split()[:2])
fingerprint=subprocess.check_output(['/usr/bin/ssh-keygen','-lf','/fixture/disposable-key.pub','-E','sha256'],env=env).decode().split()[1]
identity={'repository_id':456,'repository_name':'example/project','github_volume':'disposable-no-auth','profile_id':'example','github_id':123,'github_login':'example','git_name':'Disposable Example','git_email':'example@example.invalid','ssh_public_key':public,'ssh_fingerprint':fingerprint}
request={'action':'publish','plan':{'publication_identity':identity,'root':'','reference':'EXAMPLE-1','source_sha':base,'base_sha':base,'base':'main','branch':'example-1','repository':'example/project','pr_title':'Disposable signed publication','pr_body':'Offline generated fixture'},'previous':{},'expected_head':head,'expected_tree':tree}
pathlib.Path('/fixture/request.json').write_text(json.dumps(request))
for path in pathlib.Path('/fixture').iterdir():path.chmod(0o644)
`
	args := withMounts(containerRun("python3"), mount(fixture, "/fixture"))
	args = append(args, "-c", prepare)
	if _, err := run(ctx, nil, args...); err != nil {
		t.Fatal(err)
	}
	// The build context contains these public shims only, with no generated key.
	shimContext := t.TempDir()
	gitShim := `#!/usr/bin/python3
import json,os,pathlib,sys
args=sys.argv[1:]
with open('/publisher/commands.log','a') as log:log.write(json.dumps(['git',*args])+'\n')
state=pathlib.Path('/publisher/remote.json')
if 'ls-remote' in args:
 if state.exists():print(json.loads(state.read_text())['head']+'\trefs/heads/example-1')
elif 'push' in args:
 head=args[-1].split(':')[0]
 state.write_text(json.dumps({'head':head}))
 with open('/publisher/side-effects.log','a') as log:log.write('push\n')
else:
 os.execv('/usr/bin/git-real',['git',*args])
`
	ghShim := `#!/usr/bin/python3
import json,pathlib,sys
args=sys.argv[1:]
with open('/publisher/commands.log','a') as log:log.write(json.dumps(['gh',*args])+'\n')
request=json.loads(pathlib.Path('/request.json').read_text());plan=request['plan']
state=pathlib.Path('/publisher/remote.json');head=json.loads(state.read_text())['head'] if state.exists() else ''
created=pathlib.Path('/publisher/pr-created')
if args[:2]==['api','user']:print(json.dumps({'id':123,'login':'example'}))
elif args[:2]==['api','repos/example/project']:print(json.dumps({'id':456,'full_name':'example/project'}))
elif args[:2]==['api','repos/example/project/branches/main']:print(json.dumps({'commit':{'sha':plan['base_sha']}}))
elif args[:2]==['auth','status']:pass
elif args[:2]==['pr','list']:
 print(json.dumps([{'number':1,'url':'https://github.com/example/project/pull/1','baseRefName':'main','headRefOid':head,'state':'OPEN'}] if created.exists() else []))
elif args[:2] in [['pr','create'],['pr','edit']]:
 created.write_text('draft')
 with open('/publisher/side-effects.log','a') as log:log.write(args[1]+'\n')
elif args[:2]==['pr','view']:print(json.dumps({'number':1,'url':'https://github.com/example/project/pull/1','baseRefOid':plan['base_sha'],'headRefOid':head}))
else:raise RuntimeError('unexpected offline gh operation')
`
	for name, body := range map[string]string{"git": gitShim, "gh": ghShim, "Dockerfile": "FROM " + baseTag + "\nRUN mv /usr/bin/git /usr/bin/git-real\nCOPY --chmod=0755 git /usr/bin/git\nCOPY --chmod=0755 gh /usr/local/bin/gh\n"} {
		if err := os.WriteFile(filepath.Join(shimContext, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shimTag := "sdlc-publisher-fixture:" + identifier
	built := false
	defer func() {
		if built {
			cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if _, err := run(cleanup, nil, "image", "rm", shimTag); err != nil {
				t.Error("fixture image cleanup failed")
			}
		}
	}()
	command := exec.CommandContext(ctx, "docker", "build", "--network", "none", "--tag", shimTag, shimContext)
	command.Stdout, command.Stderr = log, log
	if command.Run() != nil {
		t.Fatalf("offline fixture image build failed; private diagnostics: %s", log.Name())
	}
	built = true
	inspect, err := run(ctx, nil, "image", "inspect", "--format", "{{.Id}}", shimTag)
	if err != nil {
		t.Fatal(err)
	}
	shimID := strings.TrimSpace(string(inspect))
	key, err := os.ReadFile(filepath.Join(fixture, "disposable-key"))
	if err != nil {
		t.Fatal("cannot read generated disposable key")
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	originalRequest, err := os.ReadFile(filepath.Join(fixture, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"signed publication", "fingerprint mismatch", "stale tested head"} {
		t.Run(scenario, func(t *testing.T) {
			state := t.TempDir()
			if err := os.Chmod(state, 0777); err != nil {
				t.Fatal(err)
			}
			var request map[string]any
			if err := json.Unmarshal(originalRequest, &request); err != nil {
				t.Fatal(err)
			}
			if scenario == "fingerprint mismatch" {
				request["plan"].(map[string]any)["publication_identity"].(map[string]any)["ssh_fingerprint"] = "SHA256:incorrect"
			}
			if scenario == "stale tested head" {
				request["expected_head"] = request["plan"].(map[string]any)["source_sha"]
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			requestPath := filepath.Join(fixture, strings.ReplaceAll(scenario, " ", "-")+".json")
			if err := os.WriteFile(requestPath, encoded, 0644); err != nil {
				t.Fatal(err)
			}
			args := containerRun("/usr/local/bin/sdlc-publisher")
			args[len(args)-1] = shimID
			args = withMounts(args, mount(requestPath, "/request.json")+",readonly", mount(filepath.Join(fixture, "source.bundle"), "/source.bundle")+",readonly", mount(state, "/publisher"))
			// stdin is the only channel delivering the generated private key.
			args = append(args[:1], append([]string{"--interactive"}, args[1:]...)...)
			output, err := run(ctx, bytes.NewReader(key), args...)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Error       string `json:"error"`
				Publication struct {
					Head   string `json:"head_sha"`
					Base   string `json:"base_sha"`
					URL    string `json:"url"`
					Number int    `json:"number"`
				} `json:"publication"`
			}
			if len(output) > 1024*1024 || json.Unmarshal(output, &response) != nil || bytes.Contains(output, key) {
				t.Fatal("invalid publisher response or signing key disclosed")
			}
			if scenario != "signed publication" {
				if scenario == "stale tested head" {
					if info, err := os.Stat(filepath.Join(state, "publication.git")); err != nil || !info.IsDir() {
						t.Fatal("stale-head case did not reach real bundle validation")
					}
				}
				if response.Error == "" {
					t.Fatal("changed frozen boundary accepted")
				}
				if _, err := os.Stat(filepath.Join(state, "side-effects.log")); !os.IsNotExist(err) {
					t.Fatal("refused publication had remote side effects")
				}
				return
			}
			if response.Error != "" || response.Publication.Number != 1 || response.Publication.URL != "https://github.com/example/project/pull/1" || response.Publication.Head == request["expected_head"] {
				audit, _ := os.ReadFile(filepath.Join(state, "commands.log"))
				log.Write(audit)
				t.Fatalf("real signed publication failed; private diagnostics: %s", log.Name())
			}
			effects, err := os.ReadFile(filepath.Join(state, "side-effects.log"))
			if err != nil || string(effects) != "push\ncreate\n" {
				t.Fatal("publication did not perform exactly one fake push and draft creation")
			}
			// Independently verify the retained signature with the original unshimmed
			// Git, and compare its exact tree with the tested tree after container exit.
			verify := `import pathlib,subprocess,sys
public=pathlib.Path('/fixture/disposable-key.pub').read_text();pathlib.Path('/tmp/allowed').write_text('* '+public)
git=['/usr/bin/git','-C','/publisher/publication.git','-c','safe.directory=/publisher/publication.git','-c','core.hooksPath=/dev/null','-c','gpg.format=ssh','-c','gpg.ssh.allowedSignersFile=/tmp/allowed']
subprocess.run(git+['verify-commit',sys.argv[1]],check=True)
assert subprocess.check_output(git+['rev-parse',sys.argv[1]+'^{tree}'],text=True).strip()==sys.argv[2]
assert 'gpgsig -----BEGIN SSH SIGNATURE-----' in subprocess.check_output(git+['cat-file','commit',sys.argv[1]],text=True)
`
			verifyArgs := withMounts(containerRun("python3"), mount(state, "/publisher")+",readonly", mount(fixture, "/fixture")+",readonly")
			verifyArgs = append(verifyArgs, "-c", verify, response.Publication.Head, request["expected_tree"].(string))
			if _, err := run(ctx, nil, verifyArgs...); err != nil {
				t.Fatal(err)
			}
		})
	}
}
