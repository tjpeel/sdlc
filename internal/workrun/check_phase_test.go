package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type phaseCheckRunner struct {
	calls [][]string
}

func (runner *phaseCheckRunner) Run(_ context.Context, output io.Writer, args ...string) error {
	runner.calls = append(runner.calls, slices.Clone(args))
	if len(runner.calls) == 4 {
		io.WriteString(output, "TestRegression expected fixed\n")
		return errors.New("disposable baseline failure")
	}
	return nil
}

func checkMount(args []string, destination string) string {
	for _, arg := range args {
		if strings.HasSuffix(arg, "dst="+destination) {
			return arg
		}
	}
	return ""
}

func TestCandidatePhaseReusesBuildStateAndSeparatesBaseline(t *testing.T) {
	checker, docker, _, workspace := checkFixture(t)
	runner := &phaseCheckRunner{}
	checker.Runner = runner
	ctx := context.Background()
	err := checker.WithCandidate(ctx, workspace, io.Discard, func(scoped Checker) error {
		if err := scoped.Check(ctx, workspace, [][]string{{"dotnet", "restore"}}, io.Discard); err != nil {
			return err
		}
		verification := scoped.(VerificationChecker)
		request := regressionRequest()
		request.Baseline = nil
		if err := verification.Verify(ctx, workspace, request, io.Discard); err != nil {
			return err
		}
		return verification.Verify(ctx, workspace, regressionRequest(), io.Discard)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatal("expected three candidate commands and one baseline command", len(runner.calls))
	}
	for _, destination := range []string{"/workspace", "/tmp/check-home"} {
		first := checkMount(runner.calls[0], destination)
		if first == "" || checkMount(runner.calls[1], destination) != first || checkMount(runner.calls[2], destination) != first || checkMount(runner.calls[3], destination) == first {
			t.Fatal("candidate and baseline storage lifetime is wrong", destination)
		}
	}
	names := map[string]bool{}
	for _, call := range runner.calls {
		position := slices.Index(call, "--name")
		if position < 0 || names[call[position+1]] {
			t.Fatal("worker name was reused")
		}
		names[call[position+1]] = true
	}
	volumes := map[string]bool{}
	for _, call := range docker.calls {
		if len(call) >= 3 && call[0] == "volume" {
			if call[1] == "create" {
				volumes[call[len(call)-1]] = true
			} else if call[1] == "rm" {
				delete(volumes, call[2])
			}
		}
	}
	if len(volumes) != 0 {
		t.Fatal("phase storage survived successful cleanup", volumes)
	}
}

func TestCandidatePhaseUsesFreshCopyAfterSourceMutation(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	docker.fingerprints = []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	err := checker.WithCandidate(context.Background(), workspace, io.Discard, func(scoped Checker) error {
		for range 2 {
			if err := scoped.Check(context.Background(), workspace, [][]string{{"go", "test", "./..."}}, io.Discard); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || checkMount(runner.calls[0], "/workspace") == checkMount(runner.calls[1], "/workspace") || checkMount(runner.calls[0], "/tmp/check-home") == checkMount(runner.calls[1], "/tmp/check-home") {
		t.Fatal("mutated source copy was reused")
	}
}

func TestCandidatePhaseUsesFreshCopyAfterFailedCommand(t *testing.T) {
	checker, docker, runner, workspace := checkFixture(t)
	// Model source mutation during a failed command. Its post-command
	// fingerprint is unavailable because execution exits before that probe.
	docker.fingerprints = []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	runner.fail = true
	err := checker.WithCandidate(context.Background(), workspace, io.Discard, func(scoped Checker) error {
		if err := scoped.Check(context.Background(), workspace, [][]string{{"dotnet", "format"}}, io.Discard); !errors.Is(err, ErrCheckFailed) {
			t.Fatalf("expected semantic failure: %v", err)
		}
		runner.fail = false
		request := regressionRequest()
		request.Baseline = nil
		return scoped.(VerificationChecker).Verify(context.Background(), workspace, request, io.Discard)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || checkMount(runner.calls[0], "/workspace") == checkMount(runner.calls[1], "/workspace") || checkMount(runner.calls[0], "/tmp/check-home") == checkMount(runner.calls[1], "/tmp/check-home") {
		t.Fatal("failed command state was reused by requested verification")
	}
}

func TestCandidateFingerprintTracksSourceAndFrozenInputs(t *testing.T) {
	source, copy, inputs := t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []*string{&source, &copy, &inputs} {
		resolved, err := filepath.EvalSymlinks(*root)
		if err != nil {
			t.Fatal(err)
		}
		*root = resolved
	}
	for _, root := range []string{source, copy} {
		if err := os.WriteFile(filepath.Join(root, "source.cs"), []byte("committed source"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--template="}, {"add", "source.cs"}, {"-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Create disposable fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %s: %v", data, err)
		}
	}
	for _, root := range []string{inputs, copy} {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DISPOSABLE_INPUT=one"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/source", source, "/workspace", copy, "/inputs", inputs).Replace(candidateFingerprint)
	fingerprint := func() string {
		t.Helper()
		cmd := exec.Command("python3", "-c", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fingerprint: %s: %v", data, err)
		}
		return string(data)
	}
	initial := fingerprint()
	if err := os.WriteFile(filepath.Join(copy, "build.dll"), []byte("build output"), 0644); err != nil {
		t.Fatal(err)
	}
	if fingerprint() != initial {
		t.Fatal("build output invalidated reuse")
	}
	if err := os.WriteFile(filepath.Join(copy, ".env"), []byte("DISPOSABLE_INPUT=two"), 0644); err != nil {
		t.Fatal(err)
	}
	if fingerprint() == initial {
		t.Fatal("changed frozen input retained reuse")
	}
	if err := os.WriteFile(filepath.Join(copy, ".env"), []byte("DISPOSABLE_INPUT=one"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(copy, "source.cs")); err != nil {
		t.Fatal(err)
	}
	if fingerprint() == initial {
		t.Fatal("missing source retained reuse")
	}
}
