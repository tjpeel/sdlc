package githubauth

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Opt in only with an explicitly selected local immutable runtime image. Fake
// authentication data remains inside network-disabled disposable containers.
func TestOfflineDockerNativeCacheAndLogoutPersistence(t *testing.T) {
	if os.Getenv("SDLC_GITHUB_AUTH_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_GITHUB_AUTH_DOCKER_TESTS=1 with an explicit test image")
	}
	image := os.Getenv("SDLC_GITHUB_AUTH_TEST_IMAGE")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("SDLC_GITHUB_AUTH_TEST_IMAGE must select an immutable local runtime image")
	}
	docker := LocalDocker{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		output, err := docker.Output(ctx, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}")
		if err != nil || json.Unmarshal(output, &endpoint) != nil {
			t.Fatal("cannot inspect local Docker endpoint")
		}
	}
	if !strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://") {
		t.Fatal("offline cache checks require a local Docker engine")
	}
	inspected, err := docker.Output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil || strings.TrimSpace(string(inspected)) != image {
		t.Fatal("selected test image is unavailable")
	}
	identifier, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	volume := "sdlc-github-auth-test-" + identifier + "-github"
	if _, err := docker.Output(ctx, "volume", "create", "--driver", "local", "--label", "io.sdlc.kind=disposable-auth-test", volume); err != nil {
		t.Fatal("cannot create disposable auth test volume")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := docker.Output(cleanup, "volume", "rm", volume); err != nil {
			t.Error("disposable auth test volume cleanup failed")
		}
	})
	manager := Manager{Docker: docker}
	if _, err := manager.container(ctx, image, volume, "github", "init"); err != nil {
		t.Fatal(err)
	}
	seed := helper + `
assert os.getuid() == 1000
assert not Path('/provider-auth').exists()
assert not Path('/var/run/docker.sock').exists()
assert not Path('/workspace/.git').exists()
os.umask(0o077)
(CONFIG/'config.yml').write_text('git_protocol: https\n')
(CONFIG/'hosts.yml').write_text('github.com:\n    user: offline-test-user\n    oauth_token: offline-disposable-test-data\n    git_protocol: https\n    users:\n        offline-test-user:\n            oauth_token: offline-disposable-test-data\n')
assert storage(CONFIG)
assert 'GH_TOKEN' in os.environ
assert 'GH_TOKEN' not in environment()
assert 'GITHUB_TOKEN' not in environment()
result=subprocess.run(['gh','config','get','git_protocol','--host','github.com'],env=environment(),capture_output=True,timeout=15)
assert result.returncode == 0 and result.stdout.strip() == b'https'
result=subprocess.run(['gh','auth','status','--hostname','github.com','--active'],env=environment(),capture_output=True,timeout=15)
assert result.returncode != 0
assert b'offline-test-user' in result.stdout + result.stderr
assert b'GH_TOKEN' not in result.stdout + result.stderr
print('offline native cache checked')
`
	seed = strings.Replace(seed, "if __name__ == '__main__':", "if False:", 1)
	name := "sdlc-github-auth-test-" + identifier
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		remaining, err := docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if err != nil {
			t.Error("cannot confirm disposable cache preparation container cleanup")
			return
		}
		if strings.TrimSpace(string(remaining)) != "" {
			if _, err := docker.Output(cleanup, "rm", "--force", name); err != nil {
				t.Error("disposable cache preparation container cleanup failed")
			}
		}
	})
	args := containerArgs(image, name, volume, "github", "logout")
	// The logout container is nonroot, writable-cache and network none. Replace
	// only its trusted Python script to seed disposable native test configuration.
	args[len(args)-2] = seed
	args = args[:len(args)-1]
	// Inject fake ambient overrides outside the CLI child; its environment must
	// retain only the selected native cache. Networking stays disabled throughout.
	for index, arg := range args {
		if arg == "--entrypoint" {
			args = append(args[:index], append([]string{"--env", "GH_TOKEN=offline-ambient-test-data", "--env", "GITHUB_TOKEN=offline-ambient-test-data"}, args[index:]...)...)
			break
		}
	}
	if _, err := docker.Output(ctx, args...); err != nil {
		t.Fatal("offline native cache preparation failed")
	}
	state, err := manager.status(ctx, image, volume, "github")
	if err != nil || state != "stored" {
		t.Fatal("fresh offline container could not see private native cache", err)
	}
	if _, err := manager.container(ctx, image, volume, "github", "logout"); err != nil {
		t.Fatal("native offline logout failed", err)
	}
	state, err = manager.status(ctx, image, volume, "github")
	if err != nil || state != "missing" {
		t.Fatal("native logout did not persist into a fresh offline container", err)
	}
}
