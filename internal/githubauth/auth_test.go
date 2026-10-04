package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const testID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var testImage = "sha256:" + strings.Repeat("b", 64)

type fakeDocker struct {
	mu                                              sync.Mutex
	calls                                           [][]string
	volumes                                         map[string]map[string]any
	result                                          string
	loggedIn                                        map[string]bool
	driver                                          string
	options                                         map[string]string
	badLabels, cancelled, cleanupFailed, staleImage bool
	leftover                                        bool
}

func (docker *fakeDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.calls = append(docker.calls, append([]string(nil), args...))
	switch args[0] {
	case "context":
		return []byte(`"unix:///tmp/fake-docker.sock"`), nil
	case "info":
		return []byte(`{"id":"fake-engine","os":"linux"}`), nil
	case "image":
		if docker.staleImage {
			return []byte("sha256:" + strings.Repeat("c", 64)), nil
		}
		return []byte(testImage), nil
	case "volume":
		switch args[1] {
		case "ls":
			var names []string
			for name := range docker.volumes {
				names = append(names, name)
			}
			return []byte(strings.Join(names, "\n")), nil
		case "create":
			name := args[len(args)-1]
			provider := "github"
			id := strings.TrimSuffix(strings.TrimPrefix(name, "sdlc-github-auth-"), "-"+provider)
			driver := docker.driver
			if driver == "" {
				driver = "local"
			}
			label := labels(id, provider)
			if docker.badLabels {
				label["io.sdlc.managed"] = "false"
			}
			docker.volumes[name] = map[string]any{"Name": name, "Driver": driver, "Labels": label, "Options": docker.options}
			return []byte(name), nil
		case "inspect":
			return json.Marshal([]map[string]any{docker.volumes[args[2]]})
		}
	case "run":
		if len(args) > 2 && args[len(args)-2] == "repository" {
			return []byte(docker.result), nil
		}
		if args[len(args)-1] == "identity" {
			return []byte(docker.result), nil
		}
		if args[len(args)-1] == "status" || args[len(args)-1] == "verify" {
			result := docker.result
			if result == "stored" && !docker.loggedIn[authVolume(args)] {
				result = "missing"
			}
			return []byte(`{"state":"` + result + `"}`), nil
		}
		if args[len(args)-1] == "logout" {
			delete(docker.loggedIn, authVolume(args))
		}
		return nil, nil
	case "ps":
		if docker.cleanupFailed {
			return nil, errors.New("fake-secret-diagnostic")
		}
		if docker.leftover {
			return []byte("leftover-id"), nil
		}
		return nil, nil
	case "rm":
		if ctx.Err() != nil {
			return nil, errors.New("cleanup used cancelled context")
		}
		docker.leftover = false
		return nil, nil
	}
	return nil, errors.New("unexpected fake command")
}

func (docker *fakeDocker) Run(context.Context, ...string) error {
	return errors.New("unexpected runtime mutation")
}
func (docker *fakeDocker) Interactive(_ context.Context, args ...string) error {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.calls = append(docker.calls, append([]string(nil), args...))
	if docker.cancelled {
		docker.leftover = true
		return errors.New("fake-secret-diagnostic")
	}
	if docker.loggedIn == nil {
		docker.loggedIn = map[string]bool{}
	}
	docker.loggedIn[authVolume(args)] = true
	docker.result = "stored"
	return nil
}

func authVolume(args []string) string {
	for index, arg := range args {
		if arg == "--mount" && index+1 < len(args) {
			for _, field := range strings.Split(args[index+1], ",") {
				if strings.HasPrefix(field, "src=") {
					return strings.TrimPrefix(field, "src=")
				}
			}
		}
	}
	return ""
}

func fixture(t *testing.T) (Manager, *fakeDocker) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	directory := t.TempDir()
	docker := &fakeDocker{volumes: map[string]map[string]any{}, result: "stored"}
	state := runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: testImage}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(directory, "runtime.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return Manager{Runtime: runtimeimage.Manager{Directory: directory, Docker: docker}, Docker: docker, Terminal: func() bool { return true }}, docker
}

func has(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestLoginUsesIsolatedImmutableContainersAndReusesVolume(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := manager.Login(context.Background()); err == nil {
			t.Fatal("configured profile accepted another login")
		}
	}

	if len(docker.volumes) != 1 {
		t.Fatal("repeated login created new authentication volumes")
	}
	for _, args := range docker.calls {
		if args[0] != "run" {
			continue
		}
		for _, value := range []string{testImage, "none", "--read-only", "ALL", "no-new-privileges", "--pull", "never", "HTTP_PROXY="} {
			if !has(args, value) {
				t.Fatalf("container missing isolation setting %s", value)
			}
		}
		if has(args, runtimeimage.Image) || has(args, "--privileged") || has(args, "--publish") {
			t.Fatal("unsafe runtime identity or container privileges")
		}
		mount := ""
		for i, arg := range args {
			if arg == "--mount" {
				mount = args[i+1]
			}
		}
		if !strings.Contains(mount, "volume-nocopy") || strings.Contains(mount, "type=bind") {
			t.Fatal("unsafe auth mount")
		}
		if args[len(args)-1] == "status" && (!strings.HasSuffix(mount, ",readonly") || !has(args, "1000:1000")) {
			t.Fatal("status credential mount is writable or privileged")
		}
		if args[len(args)-1] == "login" && !has(args, "bridge") {
			t.Fatal("login cannot access OAuth service")
		}
	}
}

func TestMissingStatusDoesNotCreateStorage(t *testing.T) {
	manager, docker := fixture(t)
	state, err := manager.Status(context.Background(), false)
	if err != nil || state != "missing" {
		t.Fatal(state, err)
	}
	if _, err := os.Stat(filepath.Join(manager.Runtime.Directory, "github-installation.json")); !os.IsNotExist(err) {
		t.Fatal("status created auth identity")
	}
	if len(docker.volumes) != 0 {
		t.Fatal("status created a volume")
	}
}

func TestUntrustedVolumesAreRejectedBeforeMounting(t *testing.T) {
	for _, cause := range []string{"driver", "options", "labels"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker := fixture(t)
			switch cause {
			case "driver":
				docker.driver = "third-party"
			case "options":
				docker.options = map[string]string{"type": "none", "o": "bind", "device": "/private-example"}
			case "labels":
				docker.badLabels = true
			}
			if err := manager.Login(context.Background()); err == nil {
				t.Fatal("untrusted storage accepted")
			}
			for _, args := range docker.calls {
				if args[0] == "run" {
					t.Fatal("untrusted storage was mounted")
				}
			}
		})
	}
}

func TestRejectedPreflightDoesNotStartContainer(t *testing.T) {
	for _, cause := range []string{"redirected", "image", "lock"} {
		t.Run(cause, func(t *testing.T) {
			manager, docker := fixture(t)
			switch cause {
			case "redirected":
				manager.Terminal = func() bool { return false }
			case "image":
				docker.staleImage = true
			case "lock":
				lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := manager.Login(ctx); err == nil {
				t.Fatal("failed preflight accepted")
			}
			for _, args := range docker.calls {
				if args[0] == "run" {
					t.Fatal("failed preflight launched container")
				}
			}
		})
	}
}

func TestFailedLoginCleansContainerAndKeepsVolume(t *testing.T) {
	manager, docker := fixture(t)
	docker.cancelled = true
	if err := manager.Login(context.Background()); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("failure diagnostics leaked or were ignored")
	}
	if docker.leftover || len(docker.volumes) != 1 {
		t.Fatal("failed login left container or deleted credential storage")
	}
	found := false
	for _, args := range docker.calls {
		if args[0] == "rm" && has(args, "--force") {
			found = true
		}
	}
	if !found {
		t.Fatal("Docker client failure did not explicitly remove container")
	}
}

func TestInvalidStatusAndCleanupFailureAreSafe(t *testing.T) {
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	docker.result = "fake-secret-response"
	if _, err := manager.Status(context.Background(), false); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("unsafe provider status response surfaced")
	}
	docker.result = "stored"
	docker.cleanupFailed = true
	if _, err := manager.Status(context.Background(), false); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("cleanup error leaked or was ignored")
	}
}
