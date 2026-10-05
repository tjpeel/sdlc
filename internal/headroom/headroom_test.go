package headroom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeDocker struct {
	calls      [][]string
	stats      string
	readyError bool
	started    bool
	build      string
	onReady    func()
}

func (d *fakeDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	d.calls = append(d.calls, append([]string(nil), args...))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	switch args[0] {
	case "image":
		return []byte(`{"Id":"` + digest + `","Config":{"Labels":{"io.sdlc.headroom.version":"0.39.1","io.sdlc.headroom.policy":"lossless-v1"}}}`), nil
	case "run":
		d.started = true
		return []byte("container"), nil
	case "exec":
		if strings.Contains(args[len(args)-1], "/stats") {
			return []byte(d.stats), nil
		}
		if d.readyError {
			if d.onReady != nil {
				d.onReady()
			}
			return nil, errors.New("not ready")
		}
		return nil, nil
	case "ps":
		if d.started {
			return []byte("container"), nil
		}
		return nil, nil
	case "rm":
		d.started = false
		return nil, nil
	}
	return nil, errors.New("unexpected command")
}
func (d *fakeDocker) Input(ctx context.Context, r io.Reader, args ...string) ([]byte, error) {
	b, err := io.ReadAll(r)
	d.build = string(b)
	d.calls = append(d.calls, args)
	return nil, err
}
func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
func TestPolicyAndImage(t *testing.T) {
	for _, c := range []Config{{}, {Mode: "off"}} {
		if c.Validate() != nil {
			t.Fatal("direct defaults rejected")
		}
	}
	for _, c := range []Config{{Mode: "invalid"}, {Mode: "optimize"}, {Mode: "off", ImageID: digest}, {Mode: "optimize", ImageID: digest, PolicyVersion: "other"}} {
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	d := &fakeDocker{}
	c, err := Resolve(context.Background(), d, "optimize")
	if err != nil || c.ImageID != digest {
		t.Fatal(c, err)
	}
	if _, err = verifiedImage([]byte(`{"Id":"` + digest + `","Config":{"Labels":{}}}`)); err == nil {
		t.Fatal("unlabelled image accepted")
	}
}
func TestPrivateConservativeContainer(t *testing.T) {
	c := Config{Mode: "optimize", ImageID: digest, PolicyVersion: PolicyVersion}
	args := Args(c, "private")
	for _, v := range []string{"--pull", "never", "--read-only", "127.0.0.1", "--lossless", "--no-ccr", "--disable-kompress-fallback", "--no-cache", "--stateless", "HEADROOM_BEACON=off", "HEADROOM_UPDATE_CHECK=off", "HEADROOM_TOOL_SEARCH=0", "HEADROOM_OUTPUT_SHAPER=0"} {
		if !contains(args, v) {
			t.Fatal("missing", v)
		}
	}
	for _, v := range []string{"--mount", "--volume", "--publish", "-p", "--memory-db-path", "--learn", "--memory", "--offline"} {
		if contains(args, v) && v != "--memory" {
			t.Fatal("unsafe option", v)
		}
	}
	// --memory is the Docker resource limit, never a proxy feature.
	for i, v := range args {
		if v == digest && contains(args[i+1:], "--memory") {
			t.Fatal("memory enabled")
		}
	}
	c.Mode = "passthrough"
	if !contains(Args(c, "private"), "--no-optimize") {
		t.Fatal("passthrough optimizing")
	}
}
func TestLifecycleAndStatsWhitelist(t *testing.T) {
	d := &fakeDocker{stats: `{"requests":2,"input_tokens":100,"output_tokens":10,"saved_tokens":20,"request_messages":["SECRET"]}`}
	c := Config{Mode: "optimize", ImageID: digest, PolicyVersion: PolicyVersion}
	p, err := Start(context.Background(), d, c, "private")
	if err != nil {
		t.Fatal(err)
	}
	stats := p.Stats(context.Background())
	b, _ := json.Marshal(stats)
	if stats.Requests == nil || *stats.Requests != 2 || strings.Contains(string(b), "SECRET") {
		t.Fatal(stats)
	}
	d.stats = `{"requests":-1}`
	if p.Stats(context.Background()).Error == "" {
		t.Fatal("negative metrics accepted")
	}
	if err = p.Close(); err != nil || d.started {
		t.Fatal(err)
	}
}
func TestCancelledStartCleansSidecar(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDocker{readyError: true, onReady: cancel}
	c := Config{Mode: "optimize", ImageID: digest, PolicyVersion: PolicyVersion}
	if _, err := Start(ctx, d, c, "private"); err == nil || d.started {
		t.Fatal("cancelled start survived")
	}
	if !contains(d.calls[1], "run") || !contains(d.calls[len(d.calls)-1], "rm") {
		t.Fatal("test did not reach sidecar creation and removal", d.calls)
	}
}

func TestStatsMissingCountersStayUnknown(t *testing.T) {
	d := &fakeDocker{stats: `{"requests":1,"input_tokens":null,"output_tokens":0}`}
	p := &Instance{docker: d, config: Config{Mode: "optimize"}}
	stats := p.Stats(context.Background())
	if stats.Complete || stats.InputTokens != nil || stats.SavedTokens != nil || stats.OutputTokens == nil || *stats.OutputTokens != 0 {
		t.Fatal("missing counters became zero or complete", stats)
	}
}
func TestBuildHasNoHostContext(t *testing.T) {
	d := &fakeDocker{}
	if _, err := Build(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if d.build != Dockerfile || strings.Contains(d.build, "COPY") || strings.Contains(d.build, "ADD") {
		t.Fatal("unexpected context")
	}
	if got := strings.Join(d.calls[0], " "); got != "build --tag "+Image+" -" {
		t.Fatal(got)
	}
}
