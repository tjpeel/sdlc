package notify

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/tjpeel/sdlc/internal/runstatus"
)

type Observer struct {
	mu           sync.Mutex
	sender       Sender
	bootstrapped bool
	states       map[string]string
}

func NewObserver(sender Sender) *Observer {
	return &Observer{sender: sender, states: make(map[string]string)}
}

var runID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Observe alerts once per attention transition. Initial historical ready runs
// are quiet; a run first seen ready after the first snapshot still needs review.
// A failed send consumes the transition, avoiding repeated delivery attempts.
func (observer *Observer) Observe(ctx context.Context, views []runstatus.View) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	failed := false
	present := make(map[string]bool, len(views))
	for _, view := range views {
		present[view.ID] = true
		state, label := attention(view)
		previous, seen := observer.states[view.ID]
		observer.states[view.ID] = state
		if state == "" || seen && previous == state || !observer.bootstrapped && state == "ready" || observer.sender == nil {
			continue
		}
		identity := "unavailable"
		if runID.MatchString(view.ID) {
			identity = view.ID[:8]
		}
		message := "SDLC: " + label + " (run " + identity + ")."
		if observer.sender.Send(ctx, message) != nil {
			failed = true
		}
	}
	for id := range observer.states {
		if !present[id] {
			delete(observer.states, id)
		}
	}
	observer.bootstrapped = true
	if failed {
		return fmt.Errorf("local notification delivery failed")
	}
	return nil
}

func attention(view runstatus.View) (string, string) {
	if !view.Available || view.State == "unavailable" {
		return "unavailable", "run unavailable"
	}
	if view.State == "ready" {
		return "ready", "ready for human review"
	}
	if view.Stale {
		return "stale", "controller heartbeat stale"
	}
	switch view.State {
	case "waiting_for_human":
		return view.State, "human answer needed"
	case "blocked":
		return view.State, "run blocked"
	case "failed":
		return view.State, "run failed"
	case "awaiting_reviewer":
		if view.Stopped && view.NeedsAttention {
			return view.State, "reviewer login needed"
		}
	case "interrupted":
		return view.State, "controller interrupted"
	}
	if view.Stopped {
		return "interrupted", "controller interrupted"
	}
	return "", ""
}
