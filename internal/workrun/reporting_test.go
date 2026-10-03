package workrun

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func TestReportingOwnsControllerLockAndRetainsStopReason(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{executeErr: errors.New("offline access denied")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	started, finished, activity := false, false, false
	var states []string
	r.Output = io.Discard
	r.OnStart = func(Journal) error {
		if lock, err := filelock.Acquire(filepath.Join(dir, "run.lock")); err == nil {
			lock.Close()
			t.Fatal("reporter started outside controller lock")
		}
		started = true
		return nil
	}
	r.OnState = func(snapshot Journal) error {
		if !started || finished {
			t.Fatal("state update outside reporter lifetime")
		}
		states = append(states, snapshot.State)
		return nil
	}
	r.OnOutput = func([]byte) { activity = true }
	r.OnFinish = func() error {
		if lock, err := filelock.Acquire(filepath.Join(dir, "run.lock")); err == nil {
			lock.Close()
			t.Fatal("reporter closed outside controller lock")
		}
		finished = true
		return nil
	}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("run error: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.StopReason != "offline access denied" || loaded.State != "failed" || loaded.ResumeState != "implementing" || !started || !finished || !activity || len(states) < 3 {
		t.Fatalf("reporting: %+v states=%v err=%v", loaded, states, err)
	}
}

func TestReportingDoesNotStartForSecondController(t *testing.T) {
	dir, j := testRun(t)
	lock, err := filelock.Acquire(filepath.Join(dir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.OnStart = func(Journal) error { t.Fatal("second controller started reporting"); return nil }
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("concurrent controller accepted")
	}
}

func TestReporterStartupFailurePreventsExecution(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.OnStart = func(Journal) error { return errors.New("reporting unavailable") }
	if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 {
		t.Fatal("execution proceeded without reporting")
	}
}

func TestStaleLoadedCheckpointCannotRestartCompletedWork(t *testing.T) {
	dir, j := testRun(t)
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	stale := *j
	j.State = "ready"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.OnStart = func(Journal) error { t.Fatal("stale controller started reporting"); return nil }
	if err := r.Run(context.Background(), dir, &stale, ""); err == nil || len(p.calls) != 0 {
		t.Fatal("stale checkpoint executed")
	}
}

type providerShapedCheck struct{}

func (providerShapedCheck) Check(_ context.Context, _ string, _ [][]string, output io.Writer) error {
	_, err := io.WriteString(output, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":999}}\n")
	return err
}

func TestCheckLogsCannotEnterNativeEventObserver(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Checker = providerShapedCheck{}
	var native, all strings.Builder
	r.OnOutput = func(data []byte) { all.Write(data) }
	r.OnNativeOutput = func(data []byte) { native.Write(data) }
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all.String(), "999") || strings.Contains(native.String(), "999") || !strings.Contains(native.String(), "fake streamed event") {
		t.Fatal("native events mixed with repository logs")
	}
}

func TestFreshReviewerReceivesRecordedHumanQuestions(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.PendingRole = "waiting_for_human", "review"
	question := "Should missing records return 404?"
	j.Outcome.Questions = []string{question}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, "Yes, return 404."); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || !strings.Contains(p.calls[0].Prompt, question) || !strings.Contains(p.calls[0].Prompt, "Yes, return 404.") {
		t.Fatal("fresh review omitted original question")
	}
}
