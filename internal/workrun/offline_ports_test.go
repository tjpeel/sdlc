package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// DockerChecker invokes Run after its dedicated daemon is ready. Start both
// real check workers together so the same-port service lifetimes can overlap.
type concurrentPortRunner struct {
	mu       sync.Mutex
	ready    int
	start    chan struct{}
	services chan string
}

func (runner *concurrentPortRunner) Run(ctx context.Context, output io.Writer, args ...string) error {
	worker := ""
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--name" {
			worker = args[index+1]
			break
		}
	}
	if worker == "" {
		return fmt.Errorf("port probe worker has no owned container name")
	}
	runner.mu.Lock()
	runner.ready++
	if runner.ready == 2 {
		close(runner.start)
	}
	runner.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runner.start:
		return (localCheckRunner{}).Run(ctx, &portReadyWriter{ctx: ctx, output: output, worker: worker, services: runner.services}, args...)
	}
}

// Recognise one exact readiness line, including fragmented writes. Command JSON,
// logs containing the token and oversized lines cannot release the rendezvous.
type portReadyWriter struct {
	mu             sync.Mutex
	ctx            context.Context
	output         io.Writer
	worker         string
	services       chan<- string
	line           []byte
	dropping, sent bool
}

func (writer *portReadyWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	n, err := writer.output.Write(data)
	if n < 0 || n > len(data) {
		return n, err
	}
	for _, char := range data[:n] {
		if writer.sent {
			break
		}
		if char == '\n' {
			if !writer.dropping && string(writer.line) == "SDLC_PORT_READY" {
				writer.sent = true
				select {
				case writer.services <- writer.worker:
				case <-writer.ctx.Done():
				}
			}
			writer.line, writer.dropping = writer.line[:0], false
		} else if !writer.dropping {
			if len(writer.line) >= 256 {
				writer.line, writer.dropping = writer.line[:0], true
			} else {
				writer.line = append(writer.line, char)
			}
		}
	}
	return n, err
}

type portIsolationEvidence struct {
	Daemon    string  `json:"daemon"`
	Marker    string  `json:"marker"`
	Response  string  `json:"response"`
	Port      int     `json:"port"`
	ReadyAt   float64 `json:"ready_at"`
	HeldUntil float64 `json:"held_until"`
}

// No package restore or image pull is required inside either test daemon. The
// disposable HTTP service uses only the check worker's own Python and libraries.
const concurrentPortProbe = `import _socket,io,json,os,pathlib,re,subprocess,sys,sysconfig,tarfile,time,urllib.error,urllib.request
marker=__MARKER__
assert os.environ['DOCKER_HOST']=='unix:///run/sdlc/docker.sock'
assert os.environ['TESTCONTAINERS_HOST_OVERRIDE']=='localhost'
assert not pathlib.Path('/provider-auth').exists()
assert not pathlib.Path('/var/run/docker.sock').exists()
assert not pathlib.Path(os.environ['CODEX_HOME']).exists()
assert not pathlib.Path(os.environ['CLAUDE_CONFIG_DIR']).exists()
def docker(*args,**kwargs):
 env=os.environ.copy()
 env.pop('DOCKER_CONTEXT',None)
 return subprocess.check_output(['docker','--host','unix:///run/sdlc/docker.sock',*args],env=env,timeout=60,**kwargs)
daemon=docker('info','--format','{{.ID}}',text=True).strip()
assert daemon
files={'/bin/sh',sys.executable}
if getattr(_socket,'__file__',None): files.add(_socket.__file__)
for executable in list(files):
 files.update(re.findall(r'/[^\s()]+',subprocess.check_output(['ldd',executable],text=True)))
archive=io.BytesIO()
with tarfile.open(fileobj=archive,mode='w',dereference=True) as tar:
 for path in sorted(files): tar.add(path,arcname=path.lstrip('/'),recursive=False)
 encodings=pathlib.Path(sysconfig.get_path('stdlib'))/'encodings'
 tar.add(encodings,arcname=str(encodings).lstrip('/'))
docker('import','-','sdlc-port-smoke:local',input=archive.getvalue())
server='''import _socket
body='''+repr(marker.encode('ascii'))+'''
s=_socket.socket()
s.setsockopt(_socket.SOL_SOCKET,_socket.SO_REUSEADDR,1)
s.bind(('0.0.0.0',8080))
s.listen(8)
separator=bytes((13,10))
while True:
 descriptor,address=s._accept()
 client=_socket.socket(fileno=descriptor)
 try:
  client.recv(4096)
  client.sendall(b'HTTP/1.1 200 OK'+separator+b'Content-Length: '+str(len(body)).encode('ascii')+separator+b'Connection: close'+separator+separator+body)
 finally: client.close()
'''
docker('run','--detach','--name','port-service','--pull','never','--publish','18080:8080',
 '--env','PYTHONHOME='+sys.prefix,'--entrypoint',sys.executable,'sdlc-port-smoke:local','-S','-c',server)
ports=json.loads(docker('inspect','--format','{{json .NetworkSettings.Ports}}','port-service',text=True))
if not ports.get('8080/tcp'):
 print(docker('logs','port-service',text=True),flush=True)
 print(docker('inspect','--format','{{json .State}}','port-service',text=True),flush=True)
assert ports['8080/tcp'] and all(item['HostPort']=='18080' for item in ports['8080/tcp'])
response=None
for attempt in range(100):
 try:
  with urllib.request.urlopen('http://localhost:18080',timeout=2) as request:
   response=request.read().decode('ascii')
  break
 except (urllib.error.URLError,TimeoutError): time.sleep(0.05)
assert response==marker, (marker,response)
ready=time.time()
print('SDLC_PORT_READY',flush=True)
while not pathlib.Path('/tmp/sdlc-port-release').exists(): time.sleep(0.05)
time.sleep(2)
assert json.loads(docker('inspect','--format','{{json .State.Running}}','port-service',text=True)) is True
with urllib.request.urlopen('http://localhost:18080',timeout=2) as request:
 assert request.read().decode('ascii')==marker
held_until=time.time()
print('SDLC_PORT_EVIDENCE '+json.dumps({'daemon':daemon,'marker':marker,'response':response,'port':18080,'ready_at':ready,'held_until':held_until}),flush=True)
`

// Explicit opt-in: privileged daemons and their containers/volumes are managed
// by DockerChecker. No provider account, credentials or host service is used.
func TestOfflineDockerConcurrentPublishedPorts(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_DOCKER_TESTS=1 for the concurrent dedicated-daemon port probe")
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
	_, launch := sourceFixture(t)
	workspace := filepath.Join(realTemp(t), "workspace")
	if _, err := Capture(ctx, launch, workspace, "offline-ports", "main", "example/project", Roles{}); err != nil {
		t.Fatal(err)
	}
	runner := &concurrentPortRunner{start: make(chan struct{}), services: make(chan string, 2)}
	markers := []string{"disposable-port-one", "disposable-port-two"}
	var output [2]bytes.Buffer
	type result struct {
		index int
		err   error
	}
	completed := make(chan result, 2)
	for index, marker := range markers {
		go func(index int, marker string) {
			command := strings.ReplaceAll(concurrentPortProbe, "__MARKER__", strconv.Quote(marker))
			checker := DockerChecker{Runtime: runtime, ImageID: state.ImageID, DockerTests: true, Runner: runner}
			completed <- result{index, checker.Check(ctx, workspace, [][]string{{"python3", "-c", command}}, &output[index])}
		}(index, marker)
	}
	var failures []string
	finished := 0
	services := make(map[string]bool)
rendezvous:
	for len(services) < len(markers) {
		select {
		case worker := <-runner.services:
			if services[worker] {
				failures = append(failures, "duplicate service readiness for "+worker)
				cancel()
				break rendezvous
			}
			services[worker] = true
		case result := <-completed:
			finished++
			failures = append(failures, fmt.Sprintf("worker %d stopped before rendezvous: %v\n%s", result.index, result.err, &output[result.index]))
			cancel()
			break rendezvous
		case <-ctx.Done():
			failures = append(failures, "service rendezvous cancelled: "+ctx.Err().Error())
			cancel()
			break rendezvous
		}
	}
	if len(failures) == 0 {
		for worker := range services {
			if _, err := runtime.Docker.Output(ctx, "exec", worker, "python3", "-c", "from pathlib import Path; Path('/tmp/sdlc-port-release').touch()"); err != nil {
				failures = append(failures, "cannot release service rendezvous: "+err.Error())
				cancel()
				break
			}
		}
	}
	for finished < len(markers) {
		result := <-completed
		finished++
		if result.err != nil {
			cancel()
			failures = append(failures, fmt.Sprintf("worker %d: %v\n%s", result.index, result.err, output[result.index].String()))
		}
	}
	if len(failures) > 0 {
		t.Fatal(strings.Join(failures, "\n"))
	}
	var evidence [2]portIsolationEvidence
	for index := range markers {
		found := false
		for _, line := range strings.Split(output[index].String(), "\n") {
			if !strings.HasPrefix(line, "SDLC_PORT_EVIDENCE ") {
				continue
			}
			if found {
				t.Fatal("worker emitted duplicate port evidence")
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "SDLC_PORT_EVIDENCE ")), &evidence[index]); err != nil {
				t.Fatal(err)
			}
			found = true
		}
		item := evidence[index]
		if !found || item.Daemon == "" || item.Port != 18080 || item.Marker != markers[index] || item.Response != markers[index] || item.ReadyAt <= 0 || item.HeldUntil-item.ReadyAt < 2 {
			t.Fatalf("worker %d did not prove its port and response: %+v\n%s", index, item, &output[index])
		}
	}
	if evidence[0].Daemon == evidence[1].Daemon {
		t.Fatal("workers used the same Docker daemon")
	}
	if evidence[0].ReadyAt >= evidence[1].HeldUntil || evidence[1].ReadyAt >= evidence[0].HeldUntil {
		t.Fatalf("published services did not overlap; isolation is unverified: %+v", evidence)
	}
	t.Log("Two distinct dedicated Docker daemons served different disposable responses on localhost:18080 during overlapping service lifetimes")
}

func TestPortReadyWriterRequiresExactBoundedLineAcrossChunks(t *testing.T) {
	services := make(chan string, 2)
	var output bytes.Buffer
	writer := &portReadyWriter{ctx: context.Background(), output: &output, worker: "owned-worker", services: services}
	for _, text := range []string{`["python3","-c","print('SDLC_PORT_READY')"]` + "\n", strings.Repeat("x", 512) + "SDLC_PORT_READY\n", "prefix SDLC_PORT_READY\n", "SDLC_PORT_READY suffix\n"} {
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case value := <-services:
		t.Fatalf("false readiness: %s", value)
	default:
	}
	for _, chunk := range []string{"SDLC_", "PORT_", "READY", "\n", "SDLC_PORT_READY\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case value := <-services:
		if value != "owned-worker" {
			t.Fatal(value)
		}
	default:
		t.Fatal("fragmented exact readiness not recognized")
	}
	select {
	case value := <-services:
		t.Fatalf("duplicate readiness: %s", value)
	default:
	}
	if !strings.Contains(output.String(), "prefix SDLC_PORT_READY") {
		t.Fatal("readiness observer dropped ordinary log output")
	}
}

func TestPortReadyWriterCancellationUnblocksReadinessDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &portReadyWriter{ctx: ctx, output: io.Discard, worker: "owned-worker", services: make(chan string)}
	finished := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("SDLC_PORT_READY\n")); finished <- err }()
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled readiness delivery stayed blocked")
	}
}
