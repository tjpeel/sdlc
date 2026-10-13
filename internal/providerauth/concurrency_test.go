package providerauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func await(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not reach expected point")
		return ""
	}
}

func TestProvidersShareRuntimeWithoutProviderGate(t *testing.T) {
	manager, _ := fixture(t)
	for _, provider := range []string{"codex", "claude", "codex"} {
		_, lease, err := manager.begin(context.Background(), provider)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
	}
	if build, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock")); err == nil {
		build.Close()
		t.Fatal("build overlapped providers")
	}
}

func TestQueuedProviderRechecksRuntimeAfterBuild(t *testing.T) {
	manager, docker := fixture(t)
	build, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer build.Close()
	waited := make(chan string, 1)
	manager.OnWait = func(provider, reason string) { waited <- reason }
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { done <- manager.Login(ctx, "codex") }()
	if await(t, waited) != "runtime_busy" {
		t.Fatal("wrong waiting reason")
	}
	docker.mu.Lock()
	docker.staleImage = true
	docker.mu.Unlock()
	build.Close()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal(err)
	}
	for _, call := range docker.calls {
		if call[0] == "run" {
			t.Fatal("stale runtime launched provider")
		}
	}
}

func TestCancelledQueuedLoginDoesNotLaunchContainer(t *testing.T) {
	manager, docker := fixture(t)
	lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := manager.Login(ctx, "codex"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(docker.calls) != 0 {
		t.Fatal("queued cancellation called Docker")
	}
}

func TestInstallationCreationIsSerialized(t *testing.T) {
	manager, _ := fixture(t)
	var group sync.WaitGroup
	results := make(chan string, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			id, err := manager.identity(true)
			if err != nil {
				results <- "error"
			} else {
				results <- id
			}
		}()
	}
	group.Wait()
	close(results)
	selected := ""
	for id := range results {
		if selected == "" {
			selected = id
		}
		if id == "error" || id != selected {
			t.Fatal("first logins selected different installations")
		}
	}
}

type cleanupDocker struct {
	*fakeDocker
	entered, cleanup               chan string
	releaseSession, releaseCleanup chan struct{}
}

func (docker *cleanupDocker) Stream(ctx context.Context, _ io.Reader, _, _ io.Writer, args ...string) error {
	docker.entered <- "headless"
	select {
	case <-docker.releaseSession:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (docker *cleanupDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "ps" && strings.Contains(strings.Join(args, " "), "name=^/sdlc-headless-") {
		docker.cleanup <- "cleanup"
		select {
		case <-docker.releaseCleanup:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return docker.fakeDocker.Output(ctx, args...)
}

func TestSameProviderStatusOverlapsContainerCleanup(t *testing.T) {
	manager, fake := readyInteractive(t, "codex")
	docker := &cleanupDocker{fakeDocker: fake, entered: make(chan string, 1), cleanup: make(chan string, 1), releaseSession: make(chan struct{}), releaseCleanup: make(chan struct{})}
	manager.Docker = docker
	request := headlessFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Headless(ctx, request, nil, nil) }()
	await(t, docker.entered)
	close(docker.releaseSession)
	await(t, docker.cleanup)
	statusDone := make(chan error, 1)
	go func() { _, err := manager.Status(ctx, "codex"); statusDone <- err }()
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("status blocked during independent cleanup")
	}
	close(docker.releaseCleanup)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

}

type survivorDocker struct {
	*fakeDocker
	volume                   string
	managed                  bool
	operation, status, image string
}

func (docker survivorDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "ps" && (has(args, "volume="+docker.volume) || has(args, "id=survivor")) {
		return []byte("survivor"), nil
	}
	if args[0] == "container" {
		owner := labels(strings.TrimSuffix(strings.TrimPrefix(docker.volume, "sdlc-auth-"), "-codex"), "codex")
		if !docker.managed {
			owner = nil
		}
		if docker.operation != "" {
			owner["io.sdlc.operation"] = docker.operation
		}
		status := docker.status
		if status == "" {
			status = "running"
		}
		return json.Marshal([]any{map[string]any{"Name": "/" + docker.operation, "Image": docker.image, "Config": map[string]any{"Labels": owner}, "State": map[string]any{"Status": status, "Running": status == "running"}, "Mounts": []any{map[string]any{"Type": "volume", "Name": docker.volume}}}})
	}
	return docker.fakeDocker.Output(ctx, args...)
}
func TestSurvivingCacheContainerBlocksNextOperation(t *testing.T) {
	for _, managed := range []bool{false, true} {
		manager, docker := readyInteractive(t, "codex")
		volume := ""
		for name := range docker.volumes {
			volume = name
		}
		manager.Docker = survivorDocker{fakeDocker: docker, volume: volume, managed: managed}
		if _, err := manager.Status(context.Background(), "codex"); err == nil {
			t.Fatal("survivor did not block cache")
		}
		for _, call := range docker.calls {
			if call[0] == "run" {
				t.Fatal("cache mounted beside survivor")
			}
		}
	}
}

func TestDifferentProvidersEnterHeadlessTogether(t *testing.T) {
	manager, fake := readyInteractive(t, "codex")
	if err := manager.Login(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	manager.Docker = streamDocker{fake, func(ctx context.Context, _ io.Reader, _, _ io.Writer, args []string) error {
		entered <- args[len(args)-5]
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	codex, claude := headlessFixture(t), headlessFixture(t)
	claude.Provider = "claude"
	done := make(chan error, 2)
	go func() { done <- manager.Headless(ctx, codex, nil, nil) }()
	go func() { done <- manager.Headless(ctx, claude, nil, nil) }()
	first, second := await(t, entered), await(t, entered)
	close(release)
	if first == second || (first != "codex" && first != "claude") || (second != "codex" && second != "claude") {
		t.Fatalf("providers did not enter together: %s, %s", first, second)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndependentManagersEnterSameProviderTogether(t *testing.T) {
	manager, fake := readyInteractive(t, "codex")
	entered := make(chan string, 2)
	release := make(chan struct{})
	docker := streamDocker{fake, func(ctx context.Context, _ io.Reader, _, _ io.Writer, args []string) error {
		entered <- args[len(args)-5]
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	manager.Docker = docker
	other := New(manager.Runtime)
	other.Docker = docker
	first, second := headlessFixture(t), headlessFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- manager.Headless(ctx, first, nil, nil) }()
	go func() { done <- other.Headless(ctx, second, nil, nil) }()
	if await(t, entered) != "codex" || await(t, entered) != "codex" {
		t.Fatal("same provider did not overlap")
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLiveRegisteredContainerTransitionsPermitConcurrentCacheAccess(t *testing.T) {
	manager, docker := readyInteractive(t, "codex")
	volume := ""
	for name := range docker.volumes {
		volume = name
	}
	operation := "sdlc-headless-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	lock, err := manager.registerOperation(operation, volume, "codex", testOperationImage)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-auth-"), "-codex")
	for _, state := range []string{"created", "running", "exited", "removing"} {
		manager.Docker = survivorDocker{fakeDocker: docker, volume: volume, managed: true, operation: operation, status: state, image: testOperationImage}
		if err := manager.guardVolume(context.Background(), volume, installation, "codex"); err != nil {
			t.Fatalf("live %s transition blocked: %v", state, err)
		}
	}
	lock.Close()
	if err := manager.guardVolume(context.Background(), volume, installation, "codex"); err == nil {
		t.Fatal("orphan transition accepted")
	}
}
