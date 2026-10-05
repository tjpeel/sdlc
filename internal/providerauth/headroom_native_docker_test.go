package providerauth

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/native_route.py
var nativeRouteProbe string

// This checks the unmodified pinned client's inference handshake, rather than
// constructing a provider request in a test client. All account data is fake;
// the container has no mounts and cannot reach external services.
func TestOfflineDockerNativeHeadroomRoute(t *testing.T) {
	if os.Getenv("SDLC_HEADROOM_NATIVE_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_HEADROOM_NATIVE_DOCKER_TESTS=1 with the pinned local SDLC runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	image := os.Getenv("SDLC_HEADROOM_NATIVE_IMAGE")
	if image == "" {
		output, err := exec.CommandContext(ctx, "docker", "image", "inspect", "sdlc:local", "--format", "{{.Id}}").Output()
		if err != nil {
			t.Fatal("recorded local runtime unavailable", err)
		}
		image = strings.TrimSpace(string(output))
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("native probe requires an immutable local image ID")
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--entrypoint", "python3", "--interactive", image, "-"}
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = strings.NewReader(nativeRouteProbe)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline native route failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"saved_account_forwarding": true`) {
		t.Fatalf("native route proof missing: %s", output)
	}
	t.Log(string(output))
}
