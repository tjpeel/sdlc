package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/notify"
	"github.com/tjpeel/sdlc/internal/workrun"
)

type notificationRecorder struct {
	messages []string
	fail     bool
}

func (r *notificationRecorder) Send(ctx context.Context, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.messages = append(r.messages, message)
	if r.fail {
		return fmt.Errorf("private helper diagnostics")
	}
	return nil
}

func TestRunNotificationsKeepCompletionAndDeliveryFailureSeparate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	recorder := &notificationRecorder{fail: true}
	observe := runNotifications(ctx, &out, recorder)
	j := workrun.Journal{ID: strings.Repeat("a", 24), State: "implementing", Instructions: "private implementation prompt", Outcome: workrun.Outcome{Questions: []string{"private question"}}}
	observe(j, false)
	j.State = "waiting_for_human"
	observe(j, false)
	observe(j, true)
	j.State = "implementing"
	observe(j, false)
	j.State = "ready"
	observe(j, false)
	observe(j, true)
	if len(recorder.messages) != 2 || !strings.Contains(recorder.messages[0], "human answer needed") || !strings.Contains(recorder.messages[1], "ready for human review") {
		t.Fatal(recorder.messages)
	}
	if strings.Count(out.String(), "could not deliver") != 1 || strings.Contains(out.String(), "private") || strings.Contains(strings.Join(recorder.messages, ""), "private") {
		t.Fatal("delivery error was repeated or private content leaked")
	}
	if j.State != "ready" {
		t.Fatal("notification failure changed the outcome")
	}
}

func TestDashboardNotificationFailureDoesNotStopWatchingAndSeesOffPageRuns(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("desktop option is intentionally macOS only")
	}
	root, marker, _ := dashboardFixture(t)
	before := dashboardTree(t, root)
	recorder := &notificationRecorder{fail: true}
	factory := func(notify.Options, io.Writer) (notify.Sender, error) { return recorder, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 550*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	// Selecting the live run must still observe the other run's human question.
	if err := dashboardCommandWithNotifications(ctx, []string{"--watch", "--interval", "250ms", "--run", "0123456789abcdef01234560", "--notify", "desktop"}, &out, factory); err != nil {
		t.Fatal(err)
	}
	if len(recorder.messages) != 1 || !strings.Contains(recorder.messages[0], "human answer needed") {
		t.Fatalf("off-view attention missed or repeated: %v", recorder.messages)
	}
	if strings.Count(out.String(), "could not deliver") != 1 || strings.Count(out.String(), "Run:") < 2 || strings.Contains(out.String(), "private helper diagnostics") {
		t.Fatal(out.String())
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestRunNotificationFlagsDoNotChangeFrozenExecutionSettings(t *testing.T) {
	base := []string{"--reference", "example", "--ticket", "01-example.md", "--resume", strings.Repeat("a", 24)}
	options, err := parseRunOptions(append(base, "--notify", "bell"))
	if err != nil || options.notifications.Mode != "bell" {
		t.Fatalf("host alert options rejected on resume: %v", err)
	}
	for _, extra := range [][]string{{"--notify", "invalid"}, {"--sound"}, {"--notify", "off", "--sound"}, {"--provider", "claude"}} {
		if _, err := parseRunOptions(append(append([]string(nil), base...), extra...)); err == nil {
			t.Fatalf("invalid resume configuration accepted: %v", extra)
		}
	}
}

func TestResumeNotificationsDoNotRepeatAlreadyAnsweredQuestion(t *testing.T) {
	sender := &notificationRecorder{}
	observe := runNotifications(context.Background(), io.Discard, sender)
	j := workrun.Journal{ID: strings.Repeat("a", 24), State: "waiting_for_human"}
	observe(runNotificationBaseline(j), false)
	j.State = "implementing"
	observe(j, false)
	if len(sender.messages) != 0 {
		t.Fatal("resuming with an answer emitted the obsolete question")
	}
	j.State = "waiting_for_human"
	observe(j, false)
	if len(sender.messages) != 1 {
		t.Fatal("a newly confirmed human question was suppressed")
	}
}
