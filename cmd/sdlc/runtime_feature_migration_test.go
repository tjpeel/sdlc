package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

type featureSDKDocker struct {
	args   []string
	output string
	err    error
}

func (d *featureSDKDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	d.args = append([]string(nil), args...)
	return []byte(d.output), d.err
}
func (*featureSDKDocker) Run(context.Context, ...string) error { return nil }

func TestFeatureRuntimeSDKCheckIsIsolatedAndRequired(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "global.json"), []byte(`{"sdk":{"version":"10.0.403"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	docker := &featureSDKDocker{output: "10.0.404\n"}
	manager := runtimeimage.Manager{Docker: docker}
	version, err := verifyFeatureRuntimeSDK(context.Background(), manager, "sha256:example", workspace)
	if err != nil || version != "10.0.404" {
		t.Fatalf("SDK resolution failed: %q %v", version, err)
	}
	joined := strings.Join(docker.args, " ")
	for _, required := range []string{"--rm", "--network none", "--read-only", "dst=/workspace,readonly", "--entrypoint dotnet", "--version"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing isolation %q: %s", required, joined)
		}
	}
	docker.err = errors.New("SDK unavailable")
	if _, err := verifyFeatureRuntimeSDK(context.Background(), manager, "sha256:example", workspace); err == nil {
		t.Fatal("unavailable SDK accepted")
	}
}
