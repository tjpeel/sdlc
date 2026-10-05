package main

import (
	"context"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/notify"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func runNotificationBaseline(snapshot workrun.Journal) workrun.Journal {
	if snapshot.State != "ready" {
		snapshot.State = "implementing"
	}
	return snapshot
}

// Notification failure never changes a ticket outcome or its persisted state.
func runNotifications(ctx context.Context, output io.Writer, sender notify.Sender) func(workrun.Journal, bool) {
	observer := notify.NewObserver(sender)
	warned := false
	return func(snapshot workrun.Journal, stopped bool) {
		attention := snapshot.State == "ready" || snapshot.State == "waiting_for_human" || snapshot.State == "awaiting_reviewer" || snapshot.State == "blocked" || snapshot.State == "failed" || stopped
		view := runstatus.View{ID: snapshot.ID, Available: true, State: snapshot.State, NeedsAttention: attention, Stopped: stopped}
		if observer.Observe(context.WithoutCancel(ctx), []runstatus.View{view}) != nil && !warned {
			fmt.Fprintln(output, "SDLC could not deliver a local notification; check local notification settings. The run continues.")
			warned = true
		}
	}
}
