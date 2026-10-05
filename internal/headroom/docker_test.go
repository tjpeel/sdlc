package headroom

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/proxy_smoke.py
var proxySmoke string

type localDocker struct{}

func (localDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// This opt-in test has no provider credentials, host mounts or external network.
func TestOfflineDockerTransports(t *testing.T) {
	if os.Getenv("SDLC_HEADROOM_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_HEADROOM_DOCKER_TESTS=1 after building the pinned sidecar")
	}
	for _, mode := range []string{"optimize", "passthrough"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			c, err := Resolve(ctx, localDocker{}, mode)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777"}
			policy := Args(c, "fixture")
			var proxyFlags []string
			for i, a := range policy {
				if a == "--env" {
					args = append(args, "--env", policy[i+1])
				}
				if a == c.ImageID {
					proxyFlags = policy[i+1:]
				}
			}
			encoded, err := json.Marshal(proxyFlags)
			if err != nil {
				t.Fatal(err)
			}
			args = append(args, "--env", "SMOKE_MODE="+mode, "--env", "SMOKE_PROXY_FLAGS="+string(encoded), "--entrypoint", "python", "--interactive", c.ImageID, "-")
			cmd := exec.CommandContext(ctx, "docker", args...)
			cmd.Stdin = strings.NewReader(proxySmoke)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("offline %s fixtures failed: %v\n%s", mode, err, output)
			}
			if !strings.Contains(string(output), "lossless-tool-log-roundtrip") {
				t.Fatalf("fixture proof missing: %s", output)
			}
			t.Log(string(output))
		})
	}
}
