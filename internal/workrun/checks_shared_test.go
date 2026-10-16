package workrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

type sharedEngineProbe struct {
	session   *sharedChecks
	violation string
	calls     []string
}

func (d *sharedEngineProbe) Run(ctx context.Context, args ...string) error {
	_, err := d.Output(ctx, args...)
	return err
}
func (d *sharedEngineProbe) Output(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	d.calls = append(d.calls, call)
	s := d.session
	switch {
	case strings.HasPrefix(call, "ps -aq --filter name="):
		return []byte("canonical"), nil
	case strings.HasPrefix(call, "ps -aq --filter volume="):
		if d.violation == "second owner" {
			return []byte("canonical\nforeign\n"), nil
		}
		return []byte("canonical"), nil
	case strings.HasPrefix(call, "volume ls -q"):
		return []byte("existing"), nil
	case strings.HasPrefix(call, "volume inspect"):
		labels := map[string]string{testOwnerLabel: s.name, testRecipeLabel: s.recipe}
		if d.violation == "pin" {
			labels[testRecipeLabel] = "different-recipe"
		}
		data, _ := json.Marshal(labels)
		return data, nil
	case call == "container inspect "+s.name:
		c := daemonInspection{ID: "canonical", Image: "sha256:" + strings.Repeat("a", 64), Path: "dockerd-entrypoint.sh", Args: []string{"--host=unix://" + checkSocket, "--tls=false", "--group=root", "--feature=containerd-snapshotter=false", "--storage-driver=overlay2"}}
		c.Config.Labels = map[string]string{testOwnerLabel: s.name, testRecipeLabel: s.recipe}
		c.HostConfig.Privileged = true
		c.State.Running = true
		if d.violation == "tcp exposure" {
			c.Args = append(c.Args, "--host=tcp://0.0.0.0:2375")
		}
		if d.violation == "foreign image" {
			c.Image = "sha256:" + strings.Repeat("b", 64)
		}
		for path, name := range map[string]string{"/var/lib/docker": s.name + "-data", "/sdlc": s.workVolume, "/run/sdlc": s.name + "-socket"} {
			c.Mounts = append(c.Mounts, struct{ Type, Name, Destination string }{"volume", name, path})
		}
		data, _ := json.Marshal([]daemonInspection{c})
		return data, nil
	case strings.HasPrefix(call, "image inspect"):
		return []byte("sha256:" + strings.Repeat("a", 64)), nil
	}
	return nil, nil
}

func TestSharedDaemonRejectsConflictingStoreOrRecipe(t *testing.T) {
	image := "docker:29.8.2-dind@sha256:" + strings.Repeat("c", 64)
	for _, violation := range []string{"", "second owner", "pin", "tcp exposure", "foreign image"} {
		t.Run(violation, func(t *testing.T) {
			engine := &sharedEngineProbe{violation: violation}
			checker := DockerChecker{Runtime: runtimeimage.Manager{Directory: t.TempDir(), Docker: engine}}
			s, err := newSharedChecks(checker, runtimeimage.State{Engine: "local-test-engine"}, image)
			if err != nil {
				t.Fatal(err)
			}
			engine.session = s
			err = s.ensure(context.Background(), image)
			if (err == nil) != (violation == "") {
				t.Fatalf("conflict handling: %v", err)
			}
			for _, call := range engine.calls {
				if strings.HasPrefix(call, "run ") || strings.HasPrefix(call, "start ") {
					t.Fatal("started a conflicting daemon", call)
				}
			}
		})
	}
}

type cleanupEngineProbe struct{ calls []string }

func (d *cleanupEngineProbe) Run(ctx context.Context, args ...string) error {
	_, err := d.Output(ctx, args...)
	return err
}
func (d *cleanupEngineProbe) Output(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	d.calls = append(d.calls, call)
	if strings.Contains(call, "container ls -q --filter label="+testOwnerLabel+"=") {
		return []byte("worker\nproxy\n"), nil
	}
	if strings.Contains(call, "container ls -q --filter label=io.sdlc.test-session=") {
		return []byte("service\n"), nil
	}
	return nil, nil
}
func TestSharedCleanupStopsProducersBeforeEnumeratingTheirResources(t *testing.T) {
	engine := &cleanupEngineProbe{}
	checker := DockerChecker{Runtime: runtimeimage.Manager{Directory: t.TempDir(), Docker: engine}}
	s, err := newSharedChecks(checker, runtimeimage.State{Engine: "offline"}, "docker:29.8.2-dind@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	name := "sdlc-check-" + strings.Repeat("b", 24)
	marker := filepath.Join(s.directory, name+".session")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	workerStopped, proxyStopped := false, false
	for _, call := range engine.calls {
		if strings.HasSuffix(call, "container rm -f worker") {
			workerStopped = true
		}
		if strings.HasSuffix(call, "container rm -f proxy") {
			proxyStopped = true
		}
		if strings.Contains(call, "container ls -q --filter label=io.sdlc.test-session=") && (!workerStopped || !proxyStopped) {
			t.Fatal("enumerated resources while producers could still create them")
		}
		if strings.Contains(call, "prune") {
			t.Fatal("cleanup requested global pruning")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("completed cleanup retained recovery marker")
	}
}

func TestPinnedCombinedCheckRecoveryUsesValidSharedSessionAndPreservesDaemon(t *testing.T) {
	engine := &cleanupEngineProbe{}
	checker := DockerChecker{WorkerName: "sdlc-check-" + strings.Repeat("c", 24), Runtime: runtimeimage.Manager{Directory: t.TempDir(), Docker: engine}}
	if !validCheckSession(checker.WorkerName) {
		t.Fatal("frozen generation name cannot be recovered by shared daemon")
	}
	s, err := newSharedChecks(checker, runtimeimage.State{Engine: "offline"}, "docker:29.8.2-dind@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.directory, checker.WorkerName+".session")
	if err := os.WriteFile(marker, []byte(checker.WorkerName+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checker.RecoverCombinedCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inner session marker survived recovery")
	}
	for _, call := range engine.calls {
		if call == "rm --force "+s.name || call == "volume rm "+s.workVolume {
			t.Fatal("generation recovery removed shared daemon/cache")
		}
	}
}

func TestSharedDaemonCachesAreScopedToFrozenRecipe(t *testing.T) {
	checker := DockerChecker{Runtime: runtimeimage.Manager{Directory: t.TempDir()}}
	state := runtimeimage.State{Engine: "offline-engine"}
	images := []string{"docker:29.8.2-dind@sha256:" + strings.Repeat("a", 64), "docker:29.9.0-dind@sha256:" + strings.Repeat("b", 64), "docker:29.9.0-dind@sha256:" + strings.Repeat("c", 64)}
	seen := map[string]bool{}
	for _, image := range images {
		first, err := newSharedChecks(checker, state, image)
		if err != nil {
			t.Fatal(err)
		}
		same, err := newSharedChecks(checker, state, image)
		if err != nil {
			t.Fatal(err)
		}
		if first.name != same.name || first.directory != same.directory || first.workVolume != same.workVolume {
			t.Fatal("same frozen recipe cannot reuse its cache")
		}
		if seen[first.name] || seen[first.directory] || seen[first.workVolume] {
			t.Fatal("different daemon pins share cache identity", image)
		}
		seen[first.name] = true
		seen[first.directory] = true
		seen[first.workVolume] = true
	}
}

type idleDaemonProbe struct{ calls []string }

func (d *idleDaemonProbe) Run(ctx context.Context, args ...string) error {
	_, err := d.Output(ctx, args...)
	return err
}
func (d *idleDaemonProbe) Output(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	d.calls = append(d.calls, call)
	if strings.HasPrefix(call, "ps -q ") {
		return []byte("owned-daemon"), nil
	}
	return nil, nil
}

func TestSharedDaemonStopsOnlyAfterLastSession(t *testing.T) {
	engine := &idleDaemonProbe{}
	checker := DockerChecker{Runtime: runtimeimage.Manager{Directory: t.TempDir(), Docker: engine}}
	s, err := newSharedChecks(checker, runtimeimage.State{Engine: "offline"}, "docker:29.8.2-dind@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.directory, "sdlc-check-"+strings.Repeat("b", 24)+".session")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.stopIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(engine.calls) != 0 {
		t.Fatal("touched daemon while another session exists", engine.calls)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := s.stopIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(engine.calls) != 2 || engine.calls[1] != "stop "+s.name {
		t.Fatal("idle daemon not stopped", engine.calls)
	}
	if !strings.Contains(engine.calls[0], "label="+testOwnerLabel+"="+s.name) || !strings.Contains(engine.calls[0], "label="+testRecipeLabel+"="+s.recipe) {
		t.Fatal("ownership not checked", engine.calls)
	}
}
