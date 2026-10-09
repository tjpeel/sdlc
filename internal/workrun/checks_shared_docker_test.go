package workrun

import (
	"bytes"
	"context"
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

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/testproxy"
)

// This child is a separate controller process; its kernel leases must protect
// the same cache without serialising another controller's running checks.
func TestSharedCheckChild(t *testing.T) {
	selection := os.Getenv("SDLC_SHARED_CHECK_CHILD")
	if selection == "" {
		t.Skip("subprocess fixture")
	}
	var config struct{ Directory, Name, Workspace, Marker string }
	if json.Unmarshal([]byte(selection), &config) != nil {
		t.Fatal("invalid child selection")
	}
	manager := runtimeimage.Manager{Directory: config.Directory, Name: config.Name, Docker: runtimeimage.LocalDocker{}}
	state, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checker := DockerChecker{Runtime: manager, ImageID: state.ImageID, DockerTests: true, DaemonImage: state.DependencyPins.DaemonImage, DaemonMode: SharedTestDaemon}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	script := strings.ReplaceAll(sharedComposeCheck, "__MARKER__", fmt.Sprintf("%q", config.Marker))
	if err := checker.Check(ctx, config.Workspace, [][]string{{"python3", "-c", script}}, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

const sharedComposeCheck = `import os,pathlib,subprocess,time,urllib.request,json,http.client,socket
os.environ['MARKER']=__MARKER__
def docker(*args):
 return subprocess.check_output(['docker',*args],timeout=90)
compose=['compose','-p','same-project','--profile','tests']
try:
 docker(*compose,'build','web')
 docker(*compose,'up','-d','web')
 container=docker(*compose,'ps','-q','web').decode().strip()
 labels=json.loads(docker('inspect',container))[0]['Config']['Labels']
 assert labels['io.sdlc.test-internal']=='false' and labels['io.sdlc.test-owner']==''
 image=json.loads(docker('inspect',container))[0]['Image']
 class Engine(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.connect('/run/sdlc/docker.sock')
 def reject(path,body):
  c=Engine('docker');c.request('POST',path,json.dumps(body),{'Content-Type':'application/json'})
  r=c.getresponse();data=r.read();assert r.status==403,(path,r.status,data);c.close()
 reject('/containers/create',{'Image':image,'HostConfig':{'privileged':True,'binds':['/run/sdlc/docker.sock:/raw']}})
 reject('/volumes'+'/create',{'Name':'escape','Driver':'local','driverOpts':{'device':'/','type':'none','o':'bind'}})
 pathlib.Path('/workspace/daemon-link').symlink_to('/sdlc/templates')
 reject('/containers/create',{'Image':image,'HostConfig':{'Binds':['/workspace/daemon-link:/foreign']}})
 print('SDLC_SHARED_SECURITY_BOUNDARIES_PASSED',flush=True)
 for _ in range(100):
  try:
   assert urllib.request.urlopen('http://localhost:18080',timeout=1).read().decode()==os.environ['MARKER'];break
  except OSError: time.sleep(.1)
 else: raise AssertionError('service did not start')
 assert os.environ['MARKER'].encode() in docker(*compose,'run','--rm','--no-deps','host-tests')
 print('SDLC_SHARED_READY',flush=True)
 while not pathlib.Path('release').exists(): time.sleep(.1)
 assert urllib.request.urlopen('http://localhost:18080',timeout=1).read().decode()==os.environ['MARKER']
finally:
 docker(*compose,'down','--volumes','--remove-orphans','--rmi','local')
print('SDLC_SHARED_CLEANED',flush=True)
`

// The probe exercises real wiring, cross-process cache ownership, fixed names,
// fixed ports, host networking, warm reuse and controller crash recovery.
func TestOfflineSharedDockerChecks(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_SHARED_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_SHARED_DOCKER_TESTS=1 for credential-free shared-daemon checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	base, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := base.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.DependencyPins == nil {
		t.Fatal("fixture needs pinned daemon")
	}
	root := realTemp(t)
	if evidence := os.Getenv("SDLC_SHARED_DOCKER_EVIDENCE"); evidence != "" {
		t.Cleanup(func() {
			os.MkdirAll(evidence, 0700)
			for _, name := range []string{"first.log", "second.log", "warm.log", "dotnet.log", "security.log"} {
				if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
					os.WriteFile(filepath.Join(evidence, name), data, 0600)
				}
			}
		})
	}
	id, _ := NewID()
	manager := runtimeimage.Manager{Name: "shared-probe-" + id[:12], Directory: root, Docker: base.Docker}
	baseTag := "sdlc:shared-base-" + id[:12]
	if _, err := manager.Docker.Output(ctx, "image", "tag", state.ImageID, baseTag); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Docker.Output(context.Background(), "image", "rm", baseTag) })
	build := filepath.Join(root, "image")
	if err := os.Mkdir(build, 0700); err != nil {
		t.Fatal(err)
	}
	goBuild := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(build, "proxy"), "./cmd/sdlc-test-proxy")
	goBuild.Dir = "../.."
	goBuild.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOPROXY=off", "GOSUMDB=off")
	if output, err := goBuild.CombinedOutput(); err != nil {
		t.Fatalf("build proxy: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte("FROM "+baseTag+"\nLABEL io.sdlc.test-owner=forged-controller-label io.sdlc.test-internal=true\nCOPY proxy /usr/local/bin/sdlc-test-proxy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Docker.Output(ctx, "build", "--network", "none", "--pull=false", "--tag", manager.ImageName(), build); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Docker.Output(context.Background(), "image", "rm", manager.ImageName()) })
	image, err := manager.Docker.Output(ctx, "image", "inspect", "--format", "{{.Id}}", manager.ImageName())
	if err != nil {
		t.Fatal(err)
	}
	state.Name, state.ImageID = manager.Name, strings.TrimSpace(string(image))
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(root, "runtime."+manager.Name+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	checker := DockerChecker{Runtime: manager, DockerTests: true, DaemonImage: state.DependencyPins.DaemonImage, DaemonMode: SharedTestDaemon}
	s, err := newSharedChecks(checker, state, checker.DaemonImage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		s.host(cleanup, "rm", "-f", s.name)
		for _, volume := range []string{s.name + "-data", s.workVolume, s.name + "-socket"} {
			s.host(cleanup, "volume", "rm", volume)
		}
	})
	workspace := filepath.Join(root, "source")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Dockerfile":      "FROM " + manager.ImageName() + "\nCOPY server.py /server.py\nCOPY fake-secret.txt /private\nENTRYPOINT [\"python3\",\"/server.py\"]\n",
		"fake-secret.txt": "disposable fake source marker\n",
		"server.py":       "import http.server,os\nclass H(http.server.BaseHTTPRequestHandler):\n def do_GET(self):\n  b=os.environ['MARKER'].encode();self.send_response(200);self.send_header('Content-Length',str(len(b)));self.end_headers();self.wfile.write(b)\nhttp.server.HTTPServer(('0.0.0.0',8080),H).serve_forever()\n",
		"compose.yml":     "services:\n  web:\n    container_name: fixed-service\n    build: .\n    environment: {MARKER: '${MARKER}'}\n    ports: ['18080:8080']\n    networks: [fixed]\n  host-tests:\n    image: " + manager.ImageName() + "\n    network_mode: host\n    entrypoint: [python3, -c]\n    command: [\"import urllib.request;print(urllib.request.urlopen('http://localhost:18080').read().decode())\"]\n    profiles: [tests]\nnetworks:\n  fixed:\n    name: fixed-network\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--initial-branch=main", "--template="}, {"add", "."}, {"-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-m", "Create public shared Docker fixture"}} {
		if _, err := isolatedGit(ctx, workspace, args...); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type child struct {
		command *exec.Cmd
		log     string
	}
	start := func(marker string) child {
		t.Helper()
		selection, _ := json.Marshal(struct{ Directory, Name, Workspace, Marker string }{root, manager.Name, workspace, marker})
		command := exec.CommandContext(ctx, executable, "-test.run=^TestSharedCheckChild$", "-test.v")
		command.Env = append(os.Environ(), "SDLC_SHARED_CHECK_CHILD="+string(selection))
		log := filepath.Join(root, marker+".log")
		file, err := os.Create(log)
		if err != nil {
			t.Fatal(err)
		}
		command.Stdout, command.Stderr = file, file
		if err := command.Start(); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
		t.Cleanup(func() {
			if command.ProcessState == nil {
				command.Process.Kill()
				command.Wait()
			}
		})
		return child{command, log}
	}
	waitReady := func(c child) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(c.log)
			if bytes.Contains(data, []byte("\nSDLC_SHARED_READY\n")) {
				return
			}
			if bytes.Contains(data, []byte("--- FAIL:")) {
				t.Fatalf("child failed: %s", data)
			}
			time.Sleep(100 * time.Millisecond)
		}
		data, _ := os.ReadFile(c.log)
		t.Fatalf("readiness timed out: %s", data)
	}
	first, second := start("first"), start("second")
	waitReady(first)
	waitReady(second)
	// Kill one controller while its worker remains active. A new controller must
	// recover only this orphan, without touching the other live session.
	first.command.Process.Kill()
	first.command.Wait()
	third := start("warm")
	waitReady(third)
	data, err = s.inner(ctx, "container", "ls", "-q", "--filter", "label="+testproxy.SessionLabel)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, container := range strings.Fields(string(data)) {
		marker, err := s.inner(ctx, "exec", container, "python3", "-c", "import os;print(os.environ['MARKER'])")
		if err != nil {
			t.Fatal(err)
		}
		labels = append(labels, strings.TrimSpace(string(marker)))
	}
	if len(labels) != 2 || strings.Contains(strings.Join(labels, ","), "first") {
		t.Fatalf("orphan survived or live session lost: %v", labels)
	}
	// The raw controller can see a victim's final image and its unlabeled classic
	// intermediate. A separate check must not inspect or derive from either one,
	// or join the victim's network. The marker is disposable fixture data.
	images, err := s.inner(ctx, "image", "ls", "--no-trunc", "-q", "--filter", "label="+testproxy.SessionLabel)
	if err != nil || len(strings.Fields(string(images))) == 0 {
		t.Fatal("missing victim build image", err)
	}
	foreign := strings.Fields(string(images))[0]
	parent, err := s.inner(ctx, "image", "inspect", "--format", "{{.Parent}}", foreign)
	if err != nil || len(bytes.TrimSpace(parent)) == 0 {
		t.Fatal("missing private classic intermediate", err)
	}
	private := strings.TrimSpace(string(parent))
	marker, err := s.inner(ctx, "run", "--rm", "--network", "none", "--entrypoint", "cat", private, "/private")
	if err != nil || string(marker) != "disposable fake source marker\n" {
		t.Fatal("private intermediate fixture lacks copied source", err)
	}
	networks, err := s.inner(ctx, "network", "ls", "-q", "--filter", "label="+testproxy.SessionLabel)
	if err != nil || len(strings.Fields(string(networks))) == 0 {
		t.Fatal("missing victim network", err)
	}
	security := fmt.Sprintf(`import json,http.client,socket,subprocess,pathlib,tempfile
class Engine(http.client.HTTPConnection):
 def connect(self):
  self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.connect('/run/sdlc/docker.sock')
def request(method,path,body=None):
 c=Engine('docker');c.request(method,path,json.dumps(body) if body is not None else None,{'Content-Type':'application/json'})
 r=c.getresponse();data=r.read();status=r.status;c.close();return status,data
foreign=%q;private=%q;base=%q;network=%q
for image in [foreign,private]:
 assert request('GET','/images/'+image+'/json')[0]==403
 assert request('POST','/containers/create',{'Image':image})[0]==403
status,images=request('GET','/images/json?all=1')
assert status==200 and all(i['Id'] not in [foreign,private] for i in json.loads(images))
for source in ['FROM '+foreign+'\n','FROM '+private+'\n','FROM '+base+'\nCOPY\t--from='+private+' /private /stolen\n']:
 with tempfile.TemporaryDirectory(dir='/workspace') as root:
  pathlib.Path(root,'Dockerfile').write_text(source)
  result=subprocess.run(['docker','build','--network','none','--pull=false','-t','rejected-private-build',root],capture_output=True)
  assert result.returncode!=0,result.stdout
response=request('POST','/build?networkmode='+network)
assert response[0] in [403,404],response
assert request('POST','/images/create?fromImage=docker.io/library/sdlc-test-base:rejected')[0]==403
print('SHARED_PRIVATE_CACHE_BOUNDARIES_PASSED',flush=True)`, foreign, private, state.ImageID, strings.Fields(string(networks))[0])
	file, err := os.Create(filepath.Join(root, "security.log"))
	if err != nil {
		t.Fatal(err)
	}
	err = checker.Check(ctx, workspace, [][]string{{"python3", "-c", security}}, file)
	file.Close()
	if err != nil {
		data, _ := os.ReadFile(filepath.Join(root, "security.log"))
		t.Fatalf("shared security boundaries failed: %v\n%s", err, data)
	}
	// Release the surviving sessions from their trusted controller paths.
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".session") {
			session := strings.TrimSuffix(entry.Name(), ".session")
			if _, err := s.host(ctx, "exec", s.name, "touch", "/sdlc/workspaces/"+session+"/release"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []child{second, third} {
		if err := c.command.Wait(); err != nil {
			data, _ := os.ReadFile(c.log)
			t.Fatalf("check/cleanup failed: %s %v", data, err)
		}
	}
	warm, _ := os.ReadFile(third.log)
	if !bytes.Contains(warm, []byte("Source template reused")) || bytes.Contains(warm, []byte("Reusing required host image")) {
		t.Fatalf("warm cache missed: %s", warm)
	}
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls", "-q", "--filter", "label=" + testproxy.SessionLabel}
		if kind == "container" {
			args = append(args, "-a")
		}
		resources, err := s.inner(ctx, args...)
		if err != nil || strings.TrimSpace(string(resources)) != "" {
			t.Fatalf("owned resources survived cleanup: %s %v", resources, err)
		}
	}
	cache, err := s.inner(ctx, "image", "ls", "-q", "sdlc-test-cache")
	if err != nil || len(bytes.TrimSpace(cache)) == 0 {
		t.Fatal("image build cache lost", err)
	}
	if os.Getenv("SDLC_SHARED_DOTNET_TESTS") == "1" {
		dotnetSource := filepath.Join(root, "dotnet-source")
		os.Mkdir(dotnetSource, 0700)
		if err := copyDotnetSmokeFixture("../../examples/dotnet-smoke", dotnetSource); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "--initial-branch=main", "--template="}, {"add", "."}, {"-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-m", "Create public .NET fixture"}} {
			if _, err := isolatedGit(ctx, dotnetSource, args...); err != nil {
				t.Fatal(err)
			}
		}
		inputs := filepath.Join(root, "dotnet-inputs")
		os.Mkdir(inputs, 0700)
		contents := "SMOKE_MONGO_CONNECTION_STRING=mongodb://mongo:27017/?serverSelectionTimeoutMS=2000\nSMOKE_COMPOSE_MARKER=fake-dotenv-shared-fixture\nSMOKE_ENV_FILE_MARKER=fake-dotenv-shared-fixture\n"
		if err := os.WriteFile(filepath.Join(inputs, ".env"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		checker.InputDirectory = inputs
		file, err := os.Create(filepath.Join(root, "dotnet.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		script := `import subprocess
subprocess.run(['dotnet','build','Smoke.slnx','--disable-build-servers','-m:1','-p:UseSharedCompilation=false'],check=True)
subprocess.run(['dotnet','test','tests/Smoke.UnitTests/Smoke.UnitTests.csproj','--no-build','--no-restore'],check=True)
subprocess.run(['python3','scripts/integration.py'],check=True)
print('SHARED_DOTNET_PASSED',flush=True)`
		if err := checker.Check(ctx, dotnetSource, [][]string{{"python3", "-c", script}}, file); err != nil {
			t.Fatalf("shared .NET check: %v; diagnostics retained with other fixture logs", err)
		}
	}
	t.Logf("Parallel controller processes, fixed names/ports, host network, crash recovery and warm reuse passed. Evidence: %s", root)
}
