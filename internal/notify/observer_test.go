package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

type fakeSender struct {
	messages []string
	err      error
}

func (sender *fakeSender) Send(_ context.Context, message string) error {
	sender.messages = append(sender.messages, message)
	return sender.err
}

func testView(state string) runstatus.View {
	return runstatus.View{ID: "abcdef0123456789abcdef01", Available: true, State: state,
		Root: "/private-example", Reference: "private-reference", Ticket: "private-ticket", Directory: "/private-directory",
		Questions: []string{"private-question"}, StopReason: "private-stop", Error: "private-error", ActivityError: "private-activity",
		Findings: []workrun.Finding{{Scenario: "private-finding"}}, PR: workrun.Publication{URL: "https://example.invalid/private-pr"}}
}

func TestObserverAttentionPayloadsContainOnlyFixedLabelsAndValidPrefix(t *testing.T) {
	for _, scenario := range []struct {
		state                       string
		label                       string
		stale, unavailable, stopped bool
	}{
		{"waiting_for_human", "human answer needed", false, false, true},
		{"blocked", "run blocked", false, false, true},
		{"failed", "run failed", false, false, true},
		{"awaiting_reviewer", "reviewer login needed", false, false, true},
		{"implementing", "controller heartbeat stale", true, false, false},
		{"unavailable", "run unavailable", false, true, false},
		{"implementing", "controller interrupted", false, false, true},
		{"interrupted", "controller interrupted", false, false, false},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			sender := &fakeSender{}
			observer := NewObserver(sender)
			view := testView(scenario.state)
			view.Stale, view.Available, view.Stopped = scenario.stale, !scenario.unavailable, scenario.stopped
			view.NeedsAttention = true
			if err := observer.Observe(context.Background(), []runstatus.View{view}); err != nil || len(sender.messages) != 1 {
				t.Fatal("initial attention was not delivered", err)
			}
			want := "SDLC: " + scenario.label + " (run abcdef01)."
			if sender.messages[0] != want || strings.Contains(sender.messages[0], "private") {
				t.Fatal("private or unexpected alert payload", sender.messages)
			}
			view.HeartbeatAt = time.Now()
			view.Questions = []string{"different private question"}
			if err := observer.Observe(context.Background(), []runstatus.View{view}); err != nil || len(sender.messages) != 1 {
				t.Fatal("unchanged attention repeated", err)
			}
		})
	}
}

func TestAwaitingReviewerLoginAlertRequiresStoppedAttention(t *testing.T) {
	sender := &fakeSender{}
	observer := NewObserver(sender)
	v := testView("awaiting_reviewer")
	v.Live, v.NeedsAttention = true, true
	if err := observer.Observe(context.Background(), []runstatus.View{v}); err != nil || len(sender.messages) != 0 {
		t.Fatal("transient reviewer preflight emitted a false login alert")
	}
	v.Live, v.Stopped = false, true
	if err := observer.Observe(context.Background(), []runstatus.View{v}); err != nil || len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "reviewer login needed") {
		t.Fatal("stopped reviewer-auth checkpoint did not alert")
	}
}

func TestObserverHistoricalReadyQuietButTransitionsAndNewReadyAlert(t *testing.T) {
	sender := &fakeSender{}
	observer := NewObserver(sender)
	historical := testView("ready")
	historical.Stopped = true
	active := testView("implementing")
	active.ID = "0123456789abcdef01234567"
	if err := observer.Observe(context.Background(), []runstatus.View{historical, active}); err != nil || len(sender.messages) != 0 {
		t.Fatal("historical ready or active run alerted", err)
	}
	active.State, active.Stopped = "ready", true
	newReady := testView("ready")
	newReady.ID = "123456789abcdef012345678"
	if err := observer.Observe(context.Background(), []runstatus.View{historical, active, newReady}); err != nil || len(sender.messages) != 2 {
		t.Fatal("transitioned or newly observed ready run missed", err, sender.messages)
	}
	for _, message := range sender.messages {
		if !strings.Contains(message, "ready for human review") {
			t.Fatal("ready was classified as interrupted", message)
		}
	}
	if err := observer.Observe(context.Background(), []runstatus.View{historical, active, newReady}); err != nil || len(sender.messages) != 2 {
		t.Fatal("ready alerts repeated", err)
	}
}

func TestObserverActiveResetRenotifiesAndFailedDeliveryIsConsumed(t *testing.T) {
	sender := &fakeSender{err: errors.New("private delivery failure")}
	observer := NewObserver(sender)
	view := testView("blocked")
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("failed delivery was not redacted", err)
	}
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err != nil || len(sender.messages) != 1 {
		t.Fatal("failed delivery retried", err)
	}
	view.State = "implementing"
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err != nil || len(sender.messages) != 1 {
		t.Fatal("active reset alerted", err)
	}
	view.State = "blocked"
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err == nil || len(sender.messages) != 2 {
		t.Fatal("new attention transition was suppressed", err)
	}
	view.State = "waiting_for_human"
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err == nil || len(sender.messages) != 3 {
		t.Fatal("changed attention stage was suppressed", err)
	}
}

func TestObserverAttemptsOtherRunsAfterFailureAndBoundsStateToCurrentViews(t *testing.T) {
	sender := &fakeSender{err: errors.New("disposable failure")}
	observer := NewObserver(sender)
	first, second := testView("blocked"), testView("failed")
	second.ID = "0123456789abcdef01234567"
	if err := observer.Observe(context.Background(), []runstatus.View{first, second}); err == nil || len(sender.messages) != 2 {
		t.Fatal("delivery failure prevented another transition", err)
	}
	sender.err = nil
	if err := observer.Observe(context.Background(), []runstatus.View{second}); err != nil || len(observer.states) != 1 {
		t.Fatal("absent run states accumulated", err)
	}
	if err := observer.Observe(context.Background(), nil); err != nil || len(observer.states) != 0 {
		t.Fatal("empty registry retained notification states", err)
	}
	ready := testView("ready")
	if err := observer.Observe(context.Background(), []runstatus.View{ready}); err != nil || len(sender.messages) != 3 {
		t.Fatal("ready run after empty snapshot missed", err)
	}
}

func TestObserverInvalidRegistryIDCannotEnterPayload(t *testing.T) {
	sender := &fakeSender{}
	observer := NewObserver(sender)
	view := testView("unavailable")
	view.ID = "private-name\ncontrol"
	view.Available = false
	if err := observer.Observe(context.Background(), []runstatus.View{view}); err != nil || len(sender.messages) != 1 || sender.messages[0] != "SDLC: run unavailable (run unavailable)." {
		t.Fatal("unsafe registry identity entered payload", sender.messages, err)
	}
	if err := NewObserver(nil).Observe(context.Background(), []runstatus.View{view}); err != nil {
		t.Fatal("disabled observer failed", err)
	}
}
