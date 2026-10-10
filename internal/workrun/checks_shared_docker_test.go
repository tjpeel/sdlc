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
	"regexp"
	"runtime"
	"strconv"
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
	checker := DockerChecker{Runtime: manager, ImageID: state.ImageID, DockerTests: true, DaemonImage: state.DependencyPins.DaemonImage, DaemonMode: SharedTestDaemon, nugetSeedDirectory: filepath.Join(config.Directory, "empty-host-nuget")}
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
			for _, name := range []string{"first.log", "second.log", "warm.log", "dotnet.log", "dotnet-warm.log", "security.log"} {
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
	checker := DockerChecker{Runtime: manager, DockerTests: true, DaemonImage: state.DependencyPins.DaemonImage, DaemonMode: SharedTestDaemon, nugetSeedDirectory: filepath.Join(root, "empty-host-nuget")}
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
	exerciseOfflineNugetCache(t, ctx, checker, state.ImageID, root)
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
		if err := checker.Check(ctx, dotnetSource, [][]string{{"dotnet", "restore", "Smoke.slnx"}, {"python3", "-c", script}}, file); err != nil {
			t.Fatalf("shared .NET check: %v; diagnostics retained with other fixture logs", err)
		}
		cold, err := os.ReadFile(filepath.Join(root, "dotnet.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`NuGet seed: [1-9][0-9]* added`).Match(cold) {
			t.Fatal("real SDK packages were not promoted into the persistent seed; inspect dotnet.log")
		}
		warmFile, err := os.Create(filepath.Join(root, "dotnet-warm.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer warmFile.Close()
		// Fresh committed workspace: no prior obj/bin state and no package source.
		warm := `import pathlib,subprocess
pathlib.Path('/workspace/empty-feed').mkdir()
subprocess.run(['dotnet','restore','Smoke.slnx','--source','/workspace/empty-feed','--no-http-cache'],check=True)
subprocess.run(['dotnet','build','Smoke.slnx','--no-restore','--disable-build-servers','-m:1','-p:UseSharedCompilation=false'],check=True)
subprocess.run(['dotnet','test','tests/Smoke.UnitTests/Smoke.UnitTests.csproj','--no-build','--no-restore'],check=True)
print('SHARED_REAL_NUGET_WARM_EMPTY_SOURCE_PASSED',flush=True)`
		if err := checker.Check(ctx, dotnetSource, [][]string{{"python3", "-c", warm}}, warmFile); err != nil {
			t.Fatalf("real SDK warm package restore without a source: %v; inspect dotnet-warm.log", err)
		}
	}
	t.Logf("Parallel controller processes, fixed names/ports, host network, crash recovery and warm reuse passed. Evidence: %s", root)
}

// Real SDK restores/builds use a disposable package with a compiled source
// dependency. Lost HOME in the second worker produces a compiler error. No
// credential or real host package data enters this fixture.
func exerciseOfflineNugetCache(t *testing.T, ctx context.Context, checker DockerChecker, image, root string) {
	t.Helper()
	source := filepath.Join(root, "nuget-source")
	host := filepath.Join(root, "nuget-host")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	generator := exec.CommandContext(ctx, "python3", "-c", `import pathlib,zipfile,hashlib,base64,json,sys
root=pathlib.Path(sys.argv[1]);p=root/'example.package'/'1.0.0';p.mkdir(parents=True)
files={'example.package.nuspec':b'<package><metadata><id>Example.Package</id><version>1.0.0</version><authors>Example</authors><description>Disposable offline package</description></metadata></package>', 'build/Example.Package.props':b'<Project><ItemGroup><Compile Include="$(MSBuildThisFileDirectory)Package.cs" /></ItemGroup></Project>', 'build/Package.cs':b'namespace Example.Package; public static class Marker { public const string Value="OFFLINE_PACKAGE_COMPILED"; }'}
a=p/'example.package.1.0.0.nupkg'
with zipfile.ZipFile(a,'w') as z:
 for name,data in files.items():
  z.writestr(name,data);f=p/name;f.parent.mkdir(parents=True,exist_ok=True);f.write_bytes(data)
h=base64.b64encode(hashlib.sha512(a.read_bytes()).digest()).decode();pathlib.Path(str(a)+'.sha512').write_text(h);(p/'.nupkg.metadata').write_text(json.dumps({'version':2,'contentHash':h,'source':'https://example.invalid/fake-private-feed'}))`, host)
	if output, err := generator.CombinedOutput(); err != nil {
		t.Fatalf("create disposable package: %v: %s", err, output)
	}
	files := map[string]string{
		"NuGet.Config":     "<configuration><packageSources><clear /></packageSources></configuration>",
		"App/App.csproj":   "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net10.0</TargetFramework><OutputType>Exe</OutputType></PropertyGroup><ItemGroup><PackageReference Include=\"Example.Package\" Version=\"1.0.0\" /></ItemGroup></Project>",
		"App/Program.cs":   "System.Console.WriteLine(Example.Package.Marker.Value);\n",
		"Tool/Tool.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net10.0</TargetFramework><OutputType>Exe</OutputType><PackAsTool>true</PackAsTool><ToolCommandName>example-tool</ToolCommandName><PackageId>Example.Tool</PackageId><Version>1.0.0</Version></PropertyGroup></Project>",
		"Tool/Program.cs":  "System.Console.WriteLine(\"OFFLINE_TOOL_HOME_PRESERVED\");\n",
	}
	for name, data := range files {
		path := filepath.Join(source, name)
		os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--initial-branch=main", "--template="}, {"add", "."}, {"-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-m", "Create disposable NuGet fixture"}} {
		if _, err := isolatedGit(ctx, source, args...); err != nil {
			t.Fatal(err)
		}
	}
	checker.nugetSeedDirectory = host
	setup := `import pathlib,subprocess,os
subprocess.run(['dotnet','restore','App/App.csproj','--configfile','NuGet.Config'],check=True)
subprocess.run(['dotnet','restore','Tool/Tool.csproj','--configfile','NuGet.Config'],check=True)
subprocess.run(['dotnet','pack','Tool/Tool.csproj','--no-restore','-o','/workspace/feed'],check=True)
subprocess.run(['dotnet','tool','install','--global','Example.Tool','--version','1.0.0','--add-source','/workspace/feed','--configfile','/workspace/NuGet.Config'],check=True)
pathlib.Path(os.environ['NUGET_SCRATCH'],'session-marker').write_text('private-session')`
	build := `import pathlib,subprocess,os,errno
assert pathlib.Path(os.environ['NUGET_SCRATCH'],'session-marker').read_text()=='private-session'
subprocess.run(['dotnet','build','App/App.csproj','--no-restore','--disable-build-servers','-p:UseSharedCompilation=false'],check=True)
assert subprocess.check_output([os.environ['HOME']+'/.dotnet/tools/example-tool']).strip()==b'OFFLINE_TOOL_HOME_PRESERVED'
assert subprocess.check_output(['dotnet','run','--project','App/App.csproj','--no-build','--no-restore']).strip()==b'OFFLINE_PACKAGE_COMPILED'
home=pathlib.Path(os.environ['HOME'])
try: (home/'.nuget').rename(home/'.nuget-moved')
except OSError as e: assert e.errno==errno.EBUSY
else: (home/'.nuget').symlink_to('/sdlc/nuget/packages')
subprocess.run(['docker','run','--rm','--network','none','--entrypoint','python3',__IMAGE__,'-c',"import pathlib,os;pathlib.Path(os.environ['NUGET_PACKAGES'],'isolation-marker').write_text('session-only')"],check=True)
subprocess.run(['docker','run','--rm','--network','none','--mount','type=bind,src=/workspace,dst=/workspace','--workdir','/workspace','--entrypoint','dotnet',__IMAGE__,'build','App/App.csproj','--no-restore','--disable-build-servers','-p:UseSharedCompilation=false'],check=True)
print('NUGET_SPLIT_COMMANDS_TOOL_AND_NESTED_PASSED',flush=True)`
	build = strings.ReplaceAll(build, "__IMAGE__", fmt.Sprintf("%q", image))
	selected, err := checker.Runtime.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newSharedChecks(checker, selected, checker.DaemonImage)
	if err != nil {
		t.Fatal(err)
	}
	inspectRoots := checkCacheOutput(func(data []byte) (int, error) {
		if strings.Contains(string(data), "Shared Docker image/build cache ready") {
			metadata, err := session.host(ctx, "exec", session.name, "sh", "-c", `stat -c '%u:%g:%a' /sdlc/nuget-sessions; for directory in /sdlc/nuget-sessions/*; do if test -d "$directory"; then stat -c '%u:%g:%a' "$directory"; fi; done`)
			if err != nil {
				return 0, fmt.Errorf("inspect protected NuGet roots: %w", err)
			}
			rows := strings.Fields(string(metadata))
			if len(rows) < 2 {
				return 0, fmt.Errorf("private cache root missing: %s", metadata)
			}
			for i, row := range rows {
				parts := strings.Split(row, ":")
				if len(parts) != 3 || parts[0] != "0" || parts[1] != "0" {
					return 0, fmt.Errorf("cache source ancestor is not controller-owned: %s", row)
				}
				mode, err := strconv.ParseUint(parts[2], 8, 32)
				if err != nil || mode&0022 != 0 || (i > 0 && mode != 0700) {
					return 0, fmt.Errorf("cache source ancestor is writable or unprotected: %s", row)
				}
			}
		}
		return len(data), nil
	})
	type outcome struct {
		log string
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var out bytes.Buffer
			err := checker.Check(ctx, source, [][]string{{"python3", "-c", setup}, {"python3", "-c", build}}, io.MultiWriter(&out, inspectRoots))
			results <- outcome{out.String(), err}
		}()
	}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("parallel private NuGet session: %v\n%s", result.err, result.log)
		}
		t.Log(result.log)
	}
	if _, err = session.host(ctx, "exec", session.name, "test", "!", "-e", "/sdlc/nuget/packages/isolation-marker"); err != nil {
		t.Fatal("nested cache mount escaped into persistent seed", err)
	}
	checker.nugetSeedDirectory = filepath.Join(root, "empty-host-nuget")
	warm := `import subprocess
subprocess.run(['dotnet','restore','App/App.csproj','--configfile','NuGet.Config'],check=True)
subprocess.run(['dotnet','build','App/App.csproj','--no-restore','--disable-build-servers','-p:UseSharedCompilation=false'],check=True)
print('NUGET_WARM_NO_SOURCE_PASSED',flush=True)`
	var out bytes.Buffer
	if err := checker.Check(ctx, source, [][]string{{"python3", "-c", warm}}, &out); err != nil {
		t.Fatalf("warm seed with no host cache or package source: %v\n%s", err, out.String())
	}
	t.Log(out.String())
}

type checkCacheOutput func([]byte) (int, error)

func (writer checkCacheOutput) Write(data []byte) (int, error) { return writer(data) }
