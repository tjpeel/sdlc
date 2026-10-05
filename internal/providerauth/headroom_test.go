package providerauth

import (
	"context"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/headroom"
)

const proxyDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type proxyDocker struct {
	streamDocker
	calls [][]string
}

func (d *proxyDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	d.calls = append(d.calls, append([]string(nil), args...))
	if args[0] == "image" && args[2] == proxyDigest {
		return []byte(`{"Id":"` + proxyDigest + `","Config":{"Labels":{"io.sdlc.headroom.version":"0.39.1","io.sdlc.headroom.policy":"lossless-v1"}}}`), nil
	}
	if args[0] == "run" && has(args, "--detach") {
		return []byte("proxy"), nil
	}
	if args[0] == "exec" && strings.Contains(args[len(args)-1], "127.0.0.1:8787") {
		if strings.Contains(args[len(args)-1], "/stats") {
			return []byte(`{"requests":1,"input_tokens":5}`), nil
		}
		return nil, nil
	}
	if args[0] == "ps" && strings.Contains(strings.Join(args, " "), "-headroom$") {
		return []byte("proxy"), nil
	}
	if args[0] == "rm" && strings.HasSuffix(args[len(args)-1], "-headroom") {
		return nil, nil
	}
	return d.fakeDocker.Output(ctx, args...)
}
func TestHeadlessProxyBoundary(t *testing.T) {
	manager, base := readyInteractive(t, "codex")
	request := headlessFixture(t)
	request.Headroom = headroom.Config{Mode: "optimize", ImageID: proxyDigest, PolicyVersion: headroom.PolicyVersion}
	var stats *headroom.Stats
	request.OnHeadroomStats = func(s headroom.Stats) { stats = &s }
	d := &proxyDocker{streamDocker: streamDocker{fakeDocker: base}}
	manager.Docker = d
	d.stream = func(ctx context.Context, in io.Reader, out, diagnostics io.Writer, args []string) error {
		var name, network string
		for i, a := range args {
			if a == "--name" {
				name = args[i+1]
			}
			if a == "--network" {
				network = args[i+1]
			}
		}
		if network != "container:"+name+"-headroom" || !has(args, "SDLC_HEADROOM=1") {
			t.Fatal("worker did not join private proxy", args)
		}
		for _, m := range mounts(t, args) {
			if m["dst"] == "/provider-auth" && !strings.HasSuffix(m["src"], "-codex") {
				t.Fatal("native identity changed")
			}
		}
		return nil
	}
	if err := manager.Headless(context.Background(), request, nil, nil); err != nil {
		t.Fatal(err)
	}
	if stats == nil || stats.Requests == nil || *stats.Requests != 1 {
		t.Fatal("missing metadata")
	}
	for _, args := range d.calls {
		if has(args, "--detach") {
			if has(args, "--mount") || has(args, "-p") || has(args, "--publish") {
				t.Fatal("sidecar carries mounts or ports")
			}
		}
	}
	// Deferred worker cleanup runs before metrics retrieval and sidecar removal.
	workerPS, statsIndex, proxyRM := -1, -1, -1
	for i, a := range d.calls {
		s := strings.Join(a, " ")
		if a[0] == "ps" && strings.Contains(s, "sdlc-headless-") && !strings.Contains(s, "-headroom$") {
			workerPS = i
		}
		if a[0] == "exec" && strings.Contains(s, "/stats") {
			statsIndex = i
		}
		if a[0] == "rm" && strings.HasSuffix(s, "-headroom") {
			proxyRM = i
		}
	}
	if workerPS < 0 || statsIndex <= workerPS || proxyRM <= statsIndex {
		t.Fatal("cleanup order", d.calls)
	}
}
func TestNativeProxyRoutePreservesDefaultsAndCredentials(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python unavailable")
	}
	script := `import ast,os
source = ` + strconv.Quote(headlessHelper) + `
tree=ast.parse(source)
function=next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name=="apply_headroom_route")
exec(compile(ast.Module(body=[function],type_ignores=[]),"helper","exec"))
os.environ.pop("SDLC_HEADROOM",None)
os.environ["OPENAI_BASE_URL"]="https://untrusted.invalid"
for provider in ("codex","claude"):
 command=[provider]; env={"NATIVE_CREDENTIAL":"opaque"}
 apply_headroom_route(provider,command,env)
 assert command==[provider] and env=={"NATIVE_CREDENTIAL":"opaque"}
os.environ["SDLC_HEADROOM"]="1"
for provider in ("codex","claude"):
 command=[provider];env={"NATIVE_CREDENTIAL":"opaque"}
 apply_headroom_route(provider,command,env)
 assert env["NATIVE_CREDENTIAL"]=="opaque"
 assert "AUTH_TOKEN" not in env and "OPENAI_API_KEY" not in env
 if provider=="codex":
  assert env["OPENAI_BASE_URL"]=="http://127.0.0.1:8787/v1" and command[1:]==["-c",'openai_base_url="http://127.0.0.1:8787/v1"']
 else:
  assert env["ANTHROPIC_BASE_URL"]=="http://127.0.0.1:8787" and command==[provider]
`
	if output, err := exec.Command("python3", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("route: %v %s", err, output)
	}
}
