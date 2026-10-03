package runstatus

import (
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

type Snapshot struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	ControllerID   string    `json:"controller_id"`
	Role           string    `json:"role"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Effort         string    `json:"effort"`
	StartedAt      time.Time `json:"started_at"`
	HeartbeatAt    time.Time `json:"heartbeat_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	OutputBytes    uint64    `json:"output_bytes"`
	Stopped        bool      `json:"stopped"`
	StoppedAt      time.Time `json:"stopped_at,omitempty"`
	Usage          Usage     `json:"usage"`
}

type Tracker struct {
	mu        sync.Mutex
	directory string
	snapshot  Snapshot
	err       error
	closed    bool
	stop      chan struct{}
	done      chan struct{}
	observer  usageObserver
	attempt   int
	sessionID string
}

// Begin must be called while holding the selected run's controller lock. Only
// activity.json is owned here; the heartbeat never reads or mutates a journal.
func Begin(directory string, journal workrun.Journal) (*Tracker, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	persisted, err := workrun.Load(directory)
	if err != nil {
		return nil, err
	}
	if journal.ID != persisted.ID {
		return nil, fmt.Errorf("tracker identity does not match journal")
	}
	controller, err := workrun.NewID()
	if err != nil {
		return nil, err
	}
	role, model := currentModel(journal)
	now := time.Now().UTC()
	t := &Tracker{directory: directory, attempt: journal.Attempt, sessionID: journal.SessionID, stop: make(chan struct{}), done: make(chan struct{}), snapshot: Snapshot{
		Version: 1, ID: journal.ID, ControllerID: controller, Role: role, Provider: model.Provider, Model: model.Name, Effort: model.Effort,
		StartedAt: now, HeartbeatAt: now, LastActivityAt: now,
	}}
	if err := replaceJSON(filepath.Join(directory, "activity.json"), t.snapshot); err != nil {
		return nil, err
	}
	go t.heartbeat()
	return t, nil
}

func (t *Tracker) heartbeat() {
	defer close(t.done)
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.mu.Lock()
			if !t.closed {
				t.snapshot.HeartbeatAt = time.Now().UTC()
				if err := t.persist(); err != nil {
					t.err = err
				}
			}
			t.mu.Unlock()
		}
	}
}

// persist rejects an older controller after a new controller takes ownership.
// The caller holds mu and the run controller lock guards normal ownership.
func (t *Tracker) persist() error {
	path := filepath.Join(t.directory, "activity.json")
	var current Snapshot
	if err := readJSON(path, &current); err != nil {
		return err
	}
	if current.ControllerID != t.snapshot.ControllerID || current.ID != t.snapshot.ID {
		return fmt.Errorf("controller activity ownership changed")
	}
	return replaceJSON(path, t.snapshot)
}

// Update is suitable for Runner.OnState. It captures only scalar role data.
func (t *Tracker) Update(journal workrun.Journal) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("activity tracker is closed")
	}
	if journal.ID != t.snapshot.ID {
		return fmt.Errorf("activity identity does not match journal")
	}
	role, model := currentModel(journal)
	if role != t.snapshot.Role || model.Provider != t.snapshot.Provider || model.Name != t.snapshot.Model || journal.Attempt != t.attempt || (t.sessionID != "" && journal.SessionID != "" && journal.SessionID != t.sessionID) {
		t.observer.reset()
		t.snapshot.Usage = Usage{}
	}
	t.attempt, t.sessionID = journal.Attempt, journal.SessionID
	t.snapshot.Role, t.snapshot.Provider, t.snapshot.Model, t.snapshot.Effort = role, model.Provider, model.Name, model.Effort
	t.snapshot.HeartbeatAt = time.Now().UTC()
	if err := t.persist(); err != nil {
		t.err = err
		return err
	}
	return nil
}

// Activity records generic output without parsing its content or performing
// file I/O. Check and diagnostic output can never update native usage telemetry.
func (t *Tracker) Activity(data []byte) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.snapshot.LastActivityAt = time.Now().UTC()
	t.snapshot.OutputBytes += uint64(len(data))
}

// NativeEvent observes only the provider's native stdout transport. It does not
// count output bytes or activity twice when the same output reaches Activity.
// Bounded malformed telemetry is ignored and never causes a run to fail.
func (t *Tracker) NativeEvent(data []byte) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.observer.feed(data, t.snapshot.Provider, t.snapshot.Model, &t.snapshot.Usage)
}

type activityWriter struct {
	tracker *Tracker
	output  io.Writer
}

func (w activityWriter) Write(data []byte) (int, error) {
	n, err := w.output.Write(data)
	if n > 0 && n <= len(data) {
		w.tracker.Activity(data[:n])
	}
	return n, err
}

func (t *Tracker) Writer(output io.Writer) io.Writer {
	if output == nil {
		output = io.Discard
	}
	return activityWriter{t, output}
}

// Close flushes pending output activity and marks the controller stopped. It is
// idempotent and waits for its heartbeat worker before returning.
func (t *Tracker) Close() error {
	t.mu.Lock()
	if !t.closed {
		// A final native JSON object can arrive without a trailing newline.
		if !t.observer.dropping {
			t.observer.consume(t.observer.line, t.snapshot.Provider, t.snapshot.Model, &t.snapshot.Usage)
		}
		t.observer.reset()
		t.closed = true
		close(t.stop)
		now := time.Now().UTC()
		t.snapshot.HeartbeatAt, t.snapshot.StoppedAt, t.snapshot.Stopped = now, now, true
		if err := t.persist(); err != nil {
			t.err = err
		}
	}
	err := t.err
	t.mu.Unlock()
	<-t.done
	return err
}
