// Package runstatus projects private run journals and controller activity for
// an installation-wide dashboard. Journals remain the authoritative state.
package runstatus

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runusage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const HeartbeatInterval = 5 * time.Second
const StaleAfter = 20 * time.Second

var identifier = regexp.MustCompile(`^[0-9a-f]{24}$`)

type Registry struct {
	stateDir string
	mu       sync.Mutex
}

func New(stateDir string) *Registry { return &Registry{stateDir: filepath.Clean(stateDir)} }

type entry struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Directory   string `json:"directory"`
	Root        string `json:"root"`
	Reference   string `json:"reference"`
	Ticket      string `json:"ticket"`
	Preparation bool   `json:"preparation,omitempty"`
}

// View keeps controller liveness independent of the journal's workflow stage.
// Journal is available for detail views but is never copied into the registry.
type View struct {
	Metrics                                           *runusage.Summary
	MetricsError                                      string
	ID, Root, Reference, Ticket, Directory, State     string
	Role, Provider, Model, Effort                     string
	StartedAt, UpdatedAt, HeartbeatAt, LastActivityAt time.Time
	Live, Stale, Stopped, NeedsAttention              bool
	Available                                         bool
	Error, ActivityError, StopReason                  string
	Questions                                         []string
	Findings                                          []workrun.Finding
	CI                                                workrun.CIResult
	PR                                                workrun.Publication
	Activity                                          Snapshot
	Journal                                           *workrun.Journal
	Preparation, FeatureOwned                         bool
}

// Register requires a valid persisted journal, so an entry cannot advertise a
// run before its recovery checkpoint exists. Re-registering the same run is safe.
func (r *Registry) Register(directory string, journal workrun.Journal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if !identifier.MatchString(journal.ID) {
		return fmt.Errorf("invalid run identity")
	}
	persisted, err := workrun.Load(directory)
	if err != nil {
		return err
	}
	if persisted.ID != journal.ID || persisted.Plan.Root != journal.Plan.Root || persisted.Plan.Reference != journal.Plan.Reference || persisted.Plan.Ticket != journal.Plan.Ticket {
		return fmt.Errorf("registered identity does not match persisted journal")
	}
	if err := privateDirectory(r.stateDir); err != nil {
		return err
	}
	dir := filepath.Join(r.stateDir, "runs")
	if err := privateDirectory(dir); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, journal.ID+".lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("registry lock must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	path := filepath.Join(dir, journal.ID+".json")
	var old entry
	if err := readJSON(path, &old); err == nil {
		if old.Version != 1 || old.ID != journal.ID || old.Directory != directory || old.Root != persisted.Plan.Root || old.Reference != persisted.Plan.Reference || old.Ticket != persisted.Plan.Ticket {
			return fmt.Errorf("run identity is already registered elsewhere")
		}
		if old.Preparation {
			canonical, err := workrun.RunDirectory(old.Root, old.Reference, filepath.Base(old.Ticket), old.ID, false)
			if err != nil || canonical != directory {
				return fmt.Errorf("preparation promotion requires its canonical execution journal")
			}
		}
		old.Preparation = false
		return replaceJSON(path, old)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("existing registry entry is unavailable: %w", err)
	}
	return replaceJSON(path, entry{Version: 1, ID: journal.ID, Directory: directory, Root: persisted.Plan.Root, Reference: persisted.Plan.Reference, Ticket: persisted.Plan.Ticket})
}

func (r *Registry) List(now time.Time) ([]View, error) {
	dir := filepath.Join(r.stateDir, "runs")
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return []View{}, nil
	}
	if err := realDirectory(dir); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(files))
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		v := View{ID: id, State: "unavailable", NeedsAttention: true}
		var e entry
		if err := readJSON(filepath.Join(dir, file.Name()), &e); err != nil {
			v.Error = err.Error()
		} else if e.Version != 1 || e.ID != id || !identifier.MatchString(id) || !filepath.IsAbs(e.Directory) || filepath.Clean(e.Directory) != e.Directory {
			v.Error = "invalid registry entry"
		} else {
			v.Root, v.Reference, v.Ticket, v.Directory = e.Root, e.Reference, e.Ticket, e.Directory
			j, err := workrun.Load(e.Directory)
			if err != nil {
				p, preparationErr := LoadPreparation(e.Directory)
				if !e.Preparation || preparationErr != nil || p.ID != id || p.Root != e.Root || p.Reference != e.Reference || e.Ticket != ".sdlc/work/"+p.Reference+"/tickets/"+p.Ticket {
					v.Error = err.Error()
				} else {
					v.Available, v.Stopped, v.NeedsAttention = true, true, true
					v.State, v.StopReason = "blocked", p.Reason
					v.Preparation, v.FeatureOwned = true, p.FeatureOwned
					v.Role, v.Provider, v.Model, v.Effort = "implementation", p.Roles.Implementation.Provider, p.Roles.Implementation.Name, p.Roles.Implementation.Effort
					v.StartedAt, v.UpdatedAt = p.FailedAt, p.FailedAt
				}
			} else if j.ID != id || j.Plan.Root != e.Root || j.Plan.Reference != e.Reference || j.Plan.Ticket != e.Ticket {
				v.Error = "journal identity does not match registry"
			} else {
				v = project(j, e.Directory)
				metrics, err := runusage.LoadSummary(e.Directory, j.ID, j.Attempt)
				if err != nil {
					v.MetricsError = "recorded usage is unavailable: " + err.Error()
				} else {
					v.Metrics = &metrics
				}
				var a Snapshot
				if err := readJSON(filepath.Join(e.Directory, "activity.json"), &a); err != nil {
					if !os.IsNotExist(err) {
						v.ActivityError = err.Error()
						v.NeedsAttention = true
					}
					v.Stopped = true
				} else if a.Version != 1 || a.ID != id || !identifier.MatchString(a.ControllerID) || a.HeartbeatAt.IsZero() || a.HeartbeatAt.After(now.Add(HeartbeatInterval)) {
					v.ActivityError = "invalid activity snapshot"
					v.Stopped = true
					v.NeedsAttention = true
				} else {
					v.Activity, v.HeartbeatAt, v.LastActivityAt = a, a.HeartbeatAt, a.LastActivityAt
					v.Stopped = a.Stopped
					v.Stale = !a.Stopped && now.Sub(a.HeartbeatAt) > StaleAfter
					v.Live = !a.Stopped && !v.Stale
				}
				if (v.Stopped || v.Stale) && !terminal(j.State) {
					v.NeedsAttention = true
				}
			}
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].UpdatedAt.Equal(views[j].UpdatedAt) {
			return views[i].ID < views[j].ID
		}
		return views[i].UpdatedAt.After(views[j].UpdatedAt)
	})
	return views, nil
}

func project(j workrun.Journal, directory string) View {
	role, model := currentModel(j)
	started := j.StartedAt
	if started.IsZero() {
		started = j.UpdatedAt
	}
	return View{ID: j.ID, Root: j.Plan.Root, Reference: j.Plan.Reference, Ticket: j.Plan.Ticket, Directory: directory, State: j.State,
		Role: role, Provider: model.Provider, Model: model.Name, Effort: model.Effort, StartedAt: started, UpdatedAt: j.UpdatedAt,
		Available: true, NeedsAttention: j.State == "ready" || j.State == "waiting_for_human" || j.State == "blocked" || j.State == "failed" || j.StopReason != "",
		StopReason: j.StopReason, Questions: j.Outcome.Questions, Findings: j.Outcome.Findings, CI: j.CI, PR: j.Publication, Journal: &j}
}

func terminal(state string) bool {
	return state == "ready" || state == "waiting_for_human" || state == "blocked" || state == "failed"
}

func currentModel(j workrun.Journal) (string, workrun.Model) {
	state := j.State
	if state == "waiting_for_human" {
		state = j.PendingRole
	}
	if state == "blocked" || state == "failed" {
		state = j.ResumeState
	}
	if state == "review" || state == "reviewing" || state == "awaiting_reviewer" {
		return "review", j.Plan.Roles.Review
	}
	return "implementation", j.Plan.Roles.Implementation
}

func (r *Registry) Begin(directory string, journal workrun.Journal) (*Tracker, error) {
	if err := r.Register(directory, journal); err != nil {
		return nil, err
	}
	return Begin(directory, journal)
}
