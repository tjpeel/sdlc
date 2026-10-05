package terminallaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type backendFunc func(context.Context, string) error

func (f backendFunc) Launch(ctx context.Context, command string) error { return f(ctx, command) }

func fixture(t *testing.T) (*Store, Request) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(filepath.Join(base, "launches")), Request{ID: id, Root: base, Executable: exe, Args: []string{"run", "--reference", "DEMO-42"}}
}

func TestLaunchConsumeAndCorrelation(t *testing.T) {
	s, request := fixture(t)
	calls := 0
	backend := backendFunc(func(ctx context.Context, command string) error {
		calls++
		if strings.Contains(command, "DEMO-42") || strings.Contains(command, "--reference") {
			t.Fatalf("work arguments leaked into terminal command: %s", command)
		}
		if !strings.Contains(command, "--state-dir "+quote(filepath.Dir(s.Directory))) {
			t.Fatalf("custom state directory missing: %s", command)
		}
		got, err := s.Consume(request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Root != request.Root || got.Args[2] != "DEMO-42" {
			t.Fatalf("wrong request: %+v", got)
		}
		if _, err := s.Consume(request.ID); err == nil {
			t.Fatal("duplicate consume succeeded")
		}
		if err := s.RecordRun(request.ID, RunIdentity{Reference: "DEMO-42", Directory: request.Root, RunIDs: []string{"run-one"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordRun(request.ID, RunIdentity{RunIDs: []string{"run-one", "run-two"}}); err != nil {
			t.Fatal(err)
		}
		return s.Complete(request.ID, nil)
	})
	receipt, err := s.Launch(context.Background(), request, backend)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "finished" || receipt.Reference != "DEMO-42" || len(receipt.RunIDs) != 2 {
		t.Fatalf("receipt: %+v", receipt)
	}
	if _, err := s.Launch(context.Background(), request, backend); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("duplicate launch: %d calls", calls)
	}
	request.Args = []string{"run", "--reference", "DIFFERENT"}
	if _, err := s.Launch(context.Background(), request, backend); err == nil {
		t.Fatal("changed request accepted")
	}
}

func TestConcurrentConsumeAtMostOnce(t *testing.T) {
	s, request := fixture(t)
	if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Consume(request.ID); results <- err }()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d helpers consumed launch", succeeded)
	}
}

func TestAmbiguousAcknowledgementNeverRetries(t *testing.T) {
	s, request := fixture(t)
	calls := 0
	backend := backendFunc(func(context.Context, string) error { calls++; return errors.New("lost acknowledgement") })
	r, err := s.Launch(context.Background(), request, backend)
	if err == nil || r.State != "unknown" {
		t.Fatalf("receipt=%+v error=%v", r, err)
	}
	if _, err := s.Launch(context.Background(), request, backend); err == nil {
		t.Fatal("uncertain launch reported as successful")
	}
	if calls != 1 {
		t.Fatal("uncertain launch retried")
	}
	// A delayed native helper may still consume an unknown launch once.
	if _, err := s.Consume(request.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMissingCapabilityAllowsOneManualHandoff(t *testing.T) {
	s, request := fixture(t)
	r, err := s.Launch(context.Background(), request, nil)
	if !errors.Is(err, ErrUnsupported) || r.State != "unavailable" || !strings.Contains(r.ManualCommand, "launch execute --id "+request.ID) || strings.Contains(r.ManualCommand, "DEMO-42") {
		t.Fatalf("receipt=%+v error=%v", r, err)
	}
	if _, err := s.Launch(context.Background(), request, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate unavailable reported success: %v", err)
	}
	if _, err := s.Consume(request.ID); err != nil {
		t.Fatalf("manual helper refused unavailable launch: %v", err)
	}
	if _, err := s.Consume(request.ID); err == nil {
		t.Fatal("manual handoff consumed twice")
	}
}

func TestUnknownManualFallbackAndDelayedHelperConsumeOnce(t *testing.T) {
	s, request := fixture(t)
	r, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return errors.New("lost acknowledgement") }))
	if err == nil || r.State != "unknown" || !strings.Contains(r.ManualCommand, "launch execute --id "+request.ID) {
		t.Fatalf("receipt=%+v error=%v", r, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Consume(request.ID); results <- err }()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("manual and delayed native helper both started: %d", succeeded)
	}
}

func TestAcknowledgementFailurePreservesStartedHelper(t *testing.T) {
	s, request := fixture(t)
	r, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error {
		if _, err := s.Consume(request.ID); err != nil {
			t.Fatal(err)
		}
		return errors.New("acknowledgement lost after start")
	}))
	if err == nil || r.State != "started" {
		t.Fatalf("receipt=%+v error=%v", r, err)
	}
}

func TestUnsafePathsAndPermissions(t *testing.T) {
	for _, kind := range []string{"id", "directory-link", "envelope-link", "hard-link", "public-store", "public-envelope"} {
		t.Run(kind, func(t *testing.T) {
			s, request := fixture(t)
			if kind == "id" {
				if _, err := s.Status("../../outside"); err == nil {
					t.Fatal("traversal accepted")
				}
				return
			}
			if kind == "directory-link" {
				if err := os.Symlink(request.Root, s.Directory); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Launch(context.Background(), request, nil); err == nil {
					t.Fatal("symlink directory accepted")
				}
				return
			}
			if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Directory, request.ID+".json")
			switch kind {
			case "public-store":
				if err := os.Chmod(s.Directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "public-envelope":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "public-lock":
				if err := os.Chmod(filepath.Join(s.Directory, request.ID+".lock"), 0644); err != nil {
					t.Fatal(err)
				}
			case "envelope-link":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(request.Root, "outside.json")
				if err := os.WriteFile(outside, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(path, filepath.Join(request.Root, "outside.json")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Status(request.ID); err == nil {
				t.Fatal("unsafe storage accepted")
			}
		})
	}
}

func TestExecutableReplacementRejected(t *testing.T) {
	s, request := fixture(t)
	data, err := os.ReadFile(request.Executable)
	if err != nil {
		t.Fatal(err)
	}
	request.Executable = filepath.Join(request.Root, "fake-sdlc")
	if err := os.WriteFile(request.Executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Executable, []byte("replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(request.ID); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestPreparedExecutableCannotBeConsumedByAnotherHelper(t *testing.T) {
	s, request := fixture(t)
	data, err := os.ReadFile(request.Executable)
	if err != nil {
		t.Fatal(err)
	}
	request.Executable = filepath.Join(request.Root, "another-sdlc")
	if err := os.WriteFile(request.Executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(request.ID); err == nil || !strings.Contains(err.Error(), "not the prepared") {
		t.Fatalf("wrong helper accepted: %v", err)
	}
}

func TestProjectReplacementRejected(t *testing.T) {
	s, request := fixture(t)
	project := filepath.Join(request.Root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	request.Root = project
	if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(project, project+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(request.ID); err == nil || !strings.Contains(err.Error(), "root changed") {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestParallelRunCallbacksRetainEveryRunID(t *testing.T) {
	s, request := fixture(t)
	if _, err := s.Launch(context.Background(), request, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(request.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for _, id := range []string{"one", "two", "three", "one"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); errors <- s.RecordRun(request.ID, RunIdentity{RunIDs: []string{id}}) }(id)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Status(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.RunIDs) != 3 {
		t.Fatalf("lost or repeated run IDs: %v", r.RunIDs)
	}
}

func TestAdapterFlagsAndNULRejected(t *testing.T) {
	for _, arg := range []string{"--terminal=background", "--launch-id", "--json", "nul\x00value"} {
		s, r := fixture(t)
		r.Args = append(r.Args, arg)
		if _, err := s.Launch(context.Background(), r, nil); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}

func TestLiteralInputFlagsAreNotAdapterOptions(t *testing.T) {
	s, r := fixture(t)
	r.Args = []string{"run", "--input", "--json", "--input", "--terminal", "--reference", "DEMO-42"}
	if _, err := s.Launch(context.Background(), r, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewHashIsValidatedAndPartOfRequestIdentity(t *testing.T) {
	s, r := fixture(t)
	r.PreviewHash = strings.Repeat("a", 64)
	if _, err := s.Launch(context.Background(), r, backendFunc(func(context.Context, string) error { return nil })); err != nil {
		t.Fatal(err)
	}
	r.PreviewHash = strings.Repeat("b", 64)
	if _, err := s.Launch(context.Background(), r, nil); err == nil || !strings.Contains(err.Error(), "different arguments") {
		t.Fatalf("changed preview accepted: %v", err)
	}
	s, r = fixture(t)
	r.PreviewHash = "INVALID"
	if _, err := s.Launch(context.Background(), r, nil); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestITermMissingCapability(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	if err := (ITerm2{}).Launch(context.Background(), "unused"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error=%v", err)
	}
}
