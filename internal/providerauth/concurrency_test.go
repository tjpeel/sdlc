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

func TestProvidersShareRuntimeWhileSameProviderWaits(t *testing.T) {
	manager, _ := fixture(t)
	_, first, err := manager.begin(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, other, err := manager.begin(context.Background(), "claude")
	if err != nil {
		t.Fatal("different provider blocked", err)
	}
	defer other.Close()
	if build, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "runtime-build.lock")); err == nil {
		build.Close()
		t.Fatal("build overlapped providers")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waited := make(chan string, 1)
	manager.OnWait = func(provider, reason string) { waited <- provider + ":" + reason }
	done := make(chan error, 1)
	go func() {
		_, lease, err := manager.begin(ctx, "codex")
		if lease != nil {
			lease.Close()
		}
		done <- err
	}()
	if value := await(t, waited); value != "codex:provider_busy" {
		t.Fatal(value)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
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
	lock, err := filelock.Acquire(filepath.Join(manager.Runtime.Directory, "provider-codex.lock"))
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

func TestSameProviderWaitsThroughContainerCleanup(t *testing.T) {
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
	waited := make(chan string, 1)
	waiter := manager
	waiter.OnWait = func(provider, reason string) { waited <- reason }
	acquired := make(chan string, 1)
	waiter.OnAcquired = func(provider string) { acquired <- provider }
	statusDone := make(chan error, 1)
	go func() { _, err := waiter.Status(ctx, "codex"); statusDone <- err }()
	if await(t, waited) != "provider_busy" {
		t.Fatal("same provider did not queue during cleanup")
	}
	select {
	case <-acquired:
		t.Fatal("lease released before cleanup")
	default:
	}
	close(docker.releaseCleanup)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if await(t, acquired) != "codex" {
		t.Fatal("wrong provider resumed")
	}
	if err := <-statusDone; err != nil {
		t.Fatal(err)
	}
}

type survivorDocker struct {
	*fakeDocker
	volume  string
	managed bool
}

func (docker survivorDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "ps" && has(args, "volume="+docker.volume) {
		return []byte("survivor"), nil
	}
	if args[0] == "container" {
		owner := labels(strings.TrimSuffix(strings.TrimPrefix(docker.volume, "sdlc-auth-"), "-codex"), "codex")
		if !docker.managed {
			owner = nil
		}
		return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": owner}, "State": map[string]any{"Status": "running", "Running": true}, "Mounts": []any{map[string]any{"Type": "volume", "Name": docker.volume}}}})
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
		manager.Docker = survivorDocker{docker, volume, managed}
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
