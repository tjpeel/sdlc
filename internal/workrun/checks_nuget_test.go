package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNugetPackageImportAndPromotion(t *testing.T) {
	command := exec.Command("python3", "-m", "unittest", "-v", "nuget_test")
	command.Dir = "cache"
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package-cache validation: %v\n%s", err, output)
	}
}

type nugetCapabilityProbe struct {
	help      string
	calls     int
	arguments []string
}

func (probe *nugetCapabilityProbe) Run(ctx context.Context, args ...string) error {
	_, err := probe.Output(ctx, args...)
	return err
}
func (probe *nugetCapabilityProbe) Output(_ context.Context, args ...string) ([]byte, error) {
	probe.calls++
	probe.arguments = append([]string{}, args...)
	return []byte(probe.help), nil
}

func TestFrozenProxyCapabilityIsRememberedByImageIdentity(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			probe := &nugetCapabilityProbe{help: "Usage: proxy -session string -workspace string"}
			if supported {
				probe.help += " -nuget-cache string"
			}
			checker := DockerChecker{Runtime: runtimeimage.Manager{Directory: t.TempDir(), Docker: probe}}
			state := runtimeimage.State{Engine: "offline", ImageID: "sha256:" + strings.Repeat("a", 64)}
			cache, err := newSharedChecks(checker, state, "docker:29.8.2-dind@sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			name := "sdlc-check-" + strings.Repeat("c", 24)
			for i := 0; i < 2; i++ {
				found, err := cache.nugetCapability(context.Background(), name)
				if err != nil || found != supported {
					t.Fatal("wrong frozen proxy capability", found, err)
				}
			}
			if probe.calls != 1 {
				t.Fatal("capability was reprobed for unchanged immutable image", probe.calls)
			}
			if !strings.Contains(strings.Join(probe.arguments, " "), "--help 2>&1") {
				t.Fatal("help stderr is not captured for capability negotiation")
			}
			cache.imageID = "sha256:" + strings.Repeat("d", 64)
			if _, err := cache.nugetCapability(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			if probe.calls != 2 {
				t.Fatal("different frozen image reused old capability")
			}
		})
	}
}

func TestMissingHostPythonPreservesRuntimeCachePreparation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	// The skip must not need even a host HOME or package directory.
	t.Setenv("HOME", "")
	staging := t.TempDir()
	var log bytes.Buffer
	if err := exportNugetHost(context.Background(), "", staging, "missing-index", "missing-project", "current-seed-epoch", &log); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(staging, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Epoch    string         `json:"epoch"`
		Packages map[string]any `json:"packages"`
	}
	if err = json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if index.Epoch != "current-seed-epoch" || index.Packages == nil || len(index.Packages) != 0 {
		t.Fatal("skip produced invalid seed index", string(data))
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 1 || entries[0].Name() != "index.json" {
		t.Fatal("skip copied host data", entries, err)
	}
	if !strings.Contains(log.String(), "runtime package cache reuse continues") {
		t.Fatal("missing host seed fallback explanation", log.String())
	}
}
