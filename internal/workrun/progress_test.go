package workrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runprogress"
)

func TestRunnerProgressSeparatesSourcesAndPreservesRawHooks(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	var live, raw, hook, native bytes.Buffer
	r.Output = &raw
	r.ProgressOutput = &live
	r.OnOutput = func(data []byte) { hook.Write(data) }
	r.OnNativeOutput = func(data []byte) { native.Write(data) }
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if raw.String() != hook.String() {
		t.Fatal("raw output hook changed")
	}
	if strings.Count(native.String(), "fake streamed event") != 3 {
		t.Fatal(native.String())
	}
	events, _, err := runprogress.Read(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Source] = true
		if e.Source == "agent" && (e.Provider == "" || e.Role == "" || e.Attempt == 0) {
			t.Fatalf("missing native identity: %+v", e)
		}
	}
	for _, source := range []string{"sdlc", "checks", "agent"} {
		if !seen[source] {
			t.Fatal("missing " + source)
		}
	}
	for _, label := range []string{"sdlc]", "checks]", "agent:codex implementation]", "agent:claude review]"} {
		if !strings.Contains(live.String(), label) {
			t.Fatalf("missing %s: %s", label, live.String())
		}
	}
}
func TestRunnerProgressRecordsHumanQuestions(t *testing.T) {
	dir, j := testRun(t)
	outcome := testOutcome("waiting_for_human")
	outcome.Questions = []string{"Which behaviour?", "Which input?"}
	r := fakeRunner(&fakeProvider{outcomes: []Outcome{outcome}}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	events, _, err := runprogress.Read(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, e := range events {
		text.WriteString(e.Text)
	}
	if !strings.Contains(text.String(), "waiting_for_human") || !strings.Contains(text.String(), "Which input?") {
		t.Fatal(text.String())
	}
}
func TestRunnerProgressFailureIsReported(t *testing.T) {
	dir, j := testRun(t)
	r := fakeRunner(&fakeProvider{outcomes: []Outcome{testOutcome("waiting_for_human")}}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ProgressOutput = brokenProgressWriter{}
	if err := r.Run(context.Background(), dir, j, ""); err == nil || !strings.Contains(err.Error(), "cannot retain or display run progress") {
		t.Fatal(err)
	}
}

type brokenProgressWriter struct{}

func (brokenProgressWriter) Write([]byte) (int, error) { return 0, errors.New("display closed") }

func TestRunnerProgressDisplayFailureStillRetainsTransition(t *testing.T) {
	dir, j := testRun(t)
	r := fakeRunner(&fakeProvider{outcomes: []Outcome{testOutcome("checks_requested")}}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ProgressOutput = failCheckingDisplay{}
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("missing presentation failure")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "checking" {
		t.Fatalf("checkpoint lost transition: %s", saved.State)
	}
}

type failCheckingDisplay struct{}

func (failCheckingDisplay) Write(data []byte) (int, error) {
	if strings.Contains(string(data), ": checking") {
		return 0, errors.New("display closed")
	}
	return len(data), nil
}

// This fixture uses the same eventWriter as NativeProvider.Execute, including
// forwarding each byte to presentation before parsing the native session ID.
type nativeProgressFixture struct{}

func (nativeProgressFixture) Status(context.Context, string) (string, error) { return "stored", nil }
func (nativeProgressFixture) Execute(_ context.Context, session Session, out, _ io.Writer) (SessionResult, error) {
	writer := eventWriter{provider: "codex", output: out}
	if _, err := writer.Write([]byte(codexEvents(testOutcome("checks_requested")))); err != nil {
		return writer.result, err
	}
	if err := writer.finish(session.Role); err != nil {
		return writer.result, err
	}
	return writer.result, nil
}

type failNativeStartDisplay struct{}

func (failNativeStartDisplay) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Native event: thread.started") {
		return 0, errors.New("display closed")
	}
	return len(data), nil
}
func TestRunnerNativeDisplayFailureRetainsSessionForResume(t *testing.T) {
	dir, j := testRun(t)
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = nativeProgressFixture{}
	r.ProgressOutput = failNativeStartDisplay{}
	if err := r.Run(context.Background(), dir, j, ""); err == nil || !strings.Contains(err.Error(), "display closed") {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SessionID != testNative || saved.State != "checking" {
		t.Fatalf("native resume identity lost: %+v", saved)
	}
	events, _, err := runprogress.Read(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, e := range events {
		text.WriteString(e.Text)
	}
	if !strings.Contains(text.String(), "turn.completed") {
		t.Fatal("persistence stopped after display failure")
	}
}
