package filelock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedLeasesBlockExclusiveAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease")
	first, err := AcquireContext(context.Background(), path, Shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireContext(context.Background(), path, Shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if writer, err := Acquire(path); err == nil {
		writer.Close()
		t.Fatal("writer overlapped readers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waits := 0
	if writer, err := AcquireContext(ctx, path, Exclusive, func() { waits++ }); !errors.Is(err, context.DeadlineExceeded) {
		if writer != nil {
			writer.Close()
		}
		t.Fatal(err)
	}
	if waits != 1 {
		t.Fatal("wait notification was not once")
	}
}

func TestWaitErrorsAndAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "lease")
	if _, err := AcquireContext(ctx, path, Exclusive, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled waiter touched lock")
	}
	notified := false
	if _, err := AcquireContext(context.Background(), t.TempDir(), Exclusive, func() { notified = true }); err == nil || notified {
		t.Fatal("filesystem error treated as contention")
	}
}

func TestCrashReleasesLease(t *testing.T) {
	if path := os.Getenv("SDLC_TEST_CRASH_LOCK"); path != "" {
		lock, err := AcquireContext(context.Background(), path, Exclusive, nil)
		if err != nil {
			os.Exit(2)
		}
		_ = lock
		os.Stdout.WriteString("held\n")
		for {
			time.Sleep(time.Hour)
		}
	}
	path := filepath.Join(t.TempDir(), "lease")
	command := exec.Command(os.Args[0], "-test.run=^TestCrashReleasesLease$")
	command.Env = append(os.Environ(), "SDLC_TEST_CRASH_LOCK="+path)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	ready := make(chan error, 1)
	go func() { var data [5]byte; _, err := output.Read(data[:]); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child never acquired lock")
	}
	if second, err := Acquire(path); err == nil {
		second.Close()
		t.Fatal("child lease missing")
	}
	command.Process.Kill()
	command.Wait()
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal("crashed process retained lease", err)
	}
	lock.Close()
}
