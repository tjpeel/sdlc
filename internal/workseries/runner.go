package workseries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

type seriesJob struct {
	ticket Ticket
	target Target
	result Result
}
type completion struct {
	ticket Ticket
	result Result
	err    error
}

func (r Runner) Run(ctx context.Context, directory string, state *State) error {
	if r.Driver == nil || state == nil {
		return errors.New("series runner requires a driver and state")
	}
	if r.Parallel < 1 {
		r.Parallel = 1
	}
	if r.PollInterval <= 0 {
		r.PollInterval = 5 * time.Second
	}
	if err := realDir(directory); err != nil {
		return err
	}
	lockPath := filepath.Join(directory, "series.lock")
	if i, e := os.Lstat(lockPath); e == nil && (!i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0) {
		return errors.New("series lock must be a regular file")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return fmt.Errorf("cannot lock series; another controller may be running: %w", err)
	}
	defer lock.Close()
	if i, e := os.Lstat(lockPath); e != nil || !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("series lock changed while opening")
	}
	loaded, loadErr := Load(directory)
	if loadErr == nil {
		if !state.UpdatedAt.IsZero() && !state.UpdatedAt.Equal(loaded.UpdatedAt) {
			return errors.New("series state changed; reload before running")
		}
		*state = loaded
	} else if _, e := os.Lstat(filepath.Join(directory, "journal.json")); e == nil {
		return loadErr
	} else if !os.IsNotExist(e) {
		return e
	}
	if r.Initialize != nil {
		if err := r.Initialize(ctx, state); err != nil {
			return err
		}
	}
	if err := validateState(state); err != nil {
		return err
	}
	if err := seriesDirectoryMatches(directory, state.Plan); err != nil {
		return err
	}
	checkpoint := func() error {
		if err := Save(directory, state); err != nil {
			return err
		}
		if r.OnChange != nil {
			return r.OnChange(*state)
		}
		return nil
	}
	if err := checkpoint(); err != nil {
		return err
	}
	write := func(format string, args ...any) {
		if r.Output != nil {
			fmt.Fprintf(r.Output, format, args...)
		}
	}
	attempted := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.observe(ctx, state, checkpoint, write); err != nil {
			return err
		}
		if err := attention(*state); err != nil {
			return err
		}
		launched, err := r.launch(ctx, state, checkpoint, write, attempted)
		if err != nil {
			return err
		}
		if launched {
			continue
		}
		if allMerged(*state) {
			return nil
		}
		if !r.Watch {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.PollInterval):
		}
	}
}

func seriesDirectoryMatches(directory string, p Plan) error {
	expected, err := Directory(p.Root, p.Reference, false)
	if err != nil || filepath.Clean(directory) != expected {
		return errors.New("series journal does not belong to this directory")
	}
	return nil
}

func (r Runner) observe(ctx context.Context, s *State, save func() error, write func(string, ...any)) error {
	_, err := r.observeReady(ctx, s, save, write, true)
	return err
}

func (r Runner) observeReady(ctx context.Context, s *State, save func() error, write func(string, ...any), allowReconcile bool) (bool, error) {
	deferred := false
	for _, t := range s.Plan.Tickets {
		key := filepath.Base(t.File)
		result, ok := s.Results[key]
		if !ok || (result.State != "ready" && result.State != "waiting_base" && !result.DependencyInvalid) {
			continue
		}
		var o Observation
		var err error
		if result.HeadSHA == "" {
			o.State = "OPEN"
		} else {
			o, err = r.Driver.Observe(ctx, result)
		}
		if err != nil {
			return deferred, err
		}
		switch strings.ToUpper(o.State) {
		case "OPEN", "READY", "":
			if o.HeadSHA != "" && o.HeadSHA != result.HeadSHA {
				if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
					return deferred, err
				}
				result.State = "blocked"
				result.DependencyInvalid = false
				result.StopReason = "pull request head changed outside this series"
				s.Results[key] = result
				write("%s blocked: %s\n", key, result.StopReason)
				if err := save(); err != nil {
					return deferred, err
				}
				continue
			}
			target, wait, err := r.target(ctx, *s, t)
			if err != nil {
				if invalidErr := r.invalidate(ctx, s, t, save); invalidErr != nil {
					return deferred, invalidErr
				}
				if invalidErr := r.invalidateDescendants(ctx, s, t, save); invalidErr != nil {
					return deferred, invalidErr
				}
				return deferred, err
			}
			if o.Base != "" && o.Base != result.Base && !knownAutomaticRetarget(*s, t, result, target, o.Base) {
				if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
					return deferred, err
				}
				result.State = "blocked"
				result.DependencyInvalid = false
				result.StopReason = "pull request base changed outside this series"
				s.Results[key] = result
				write("%s blocked: %s\n", key, result.StopReason)
				if err := save(); err != nil {
					return deferred, err
				}
				continue
			}
			if wait {
				if err := r.invalidate(ctx, s, t, save); err != nil {
					return deferred, err
				}
				continue
			}
			if ciNeedsAction(o.CI) {
				if !allowReconcile {
					deferred = true
					continue
				}
				result.StopReason = o.Details
				if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
					return deferred, err
				}
				if result.State == "waiting_base" {
					result.State = "ready"
				}
				next, e := r.Driver.Reconcile(ctx, t, target, result)
				next.Dependencies = target.Dependencies
				next.DependencyInvalid = false
				if e != nil {
					r.reconcileFailed(s, key, result, next, e)
					if saveErr := save(); saveErr != nil {
						return deferred, saveErr
					}
					continue
				}
				next = completeResult(result, next)
				if next.StopReason == "" {
					next.StopReason = o.Details
				}
				s.Results[key] = next
				if err := save(); err != nil {
					return deferred, err
				}
				continue
			}
			if !wait && (result.DependencyInvalid || result.State == "waiting_base" || !reflect.DeepEqual(target.Dependencies, result.Dependencies) || target.Base != result.Base || target.SHA != result.BaseSHA || (o.BaseSHA != "" && o.BaseSHA != result.BaseSHA)) {
				if !allowReconcile {
					deferred = true
					continue
				}
				if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
					return deferred, err
				}
				if result.State == "waiting_base" {
					result.State = "ready"
				}
				next, e := r.Driver.Reconcile(ctx, t, target, result)
				next.Dependencies = target.Dependencies
				next.DependencyInvalid = false
				if e != nil {
					r.reconcileFailed(s, key, result, next, e)
					if saveErr := save(); saveErr != nil {
						return deferred, saveErr
					}
					continue
				}
				if next.RunID == "" {
					next.RunID = result.RunID
				}
				s.Results[key] = next
				if err := save(); err != nil {
					return deferred, err
				}
				write("%s reconciled\n", key)
			}
		case "MERGED":
			if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
				return deferred, err
			}
			if o.HeadSHA == "" || o.HeadSHA != result.HeadSHA || o.MergeSHA == "" || (o.Base != "" && o.Base != result.Base) {
				if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
					return deferred, err
				}
				result.State = "blocked"
				result.DependencyInvalid = false
				result.StopReason = "merged pull request does not match checkpoint"
			} else {
				result.State = "merged"
				result.MergeSHA = o.MergeSHA
			}
			s.Results[key] = result
			if err := save(); err != nil {
				return deferred, err
			}
		case "CLOSED":
			if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
				return deferred, err
			}
			result.State = "blocked"
			result.DependencyInvalid = false
			result.StopReason = "pull request was closed"
			s.Results[key] = result
			if err := save(); err != nil {
				return deferred, err
			}
		default:
			if err := r.invalidateDescendants(ctx, s, t, save); err != nil {
				return deferred, err
			}
			result.State = "blocked"
			result.DependencyInvalid = false
			result.StopReason = "unknown pull request state"
			s.Results[key] = result
			if err := save(); err != nil {
				return deferred, err
			}
		}
	}
	return deferred, nil
}

func ciNeedsAction(ci string) bool {
	ci = strings.ToLower(strings.TrimSpace(ci))
	return ci != "" && ci != "passed"
}
func knownAutomaticRetarget(s State, t Ticket, result Result, target Target, observedBase string) bool {
	if len(t.DependsOn) != 1 {
		return false
	}
	parent := s.Results[t.DependsOn[0]]
	return parent.State == "merged" && parent.MergeSHA != "" && result.Base == parent.Branch && target.Base == s.Plan.Base && observedBase == target.Base
}

func (r Runner) launch(ctx context.Context, s *State, save func() error, write func(string, ...any), attempted map[string]bool) (bool, error) {
	jobs := []seriesJob{}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan completion, r.Parallel)
	fill := func() error {
		for _, t := range s.Plan.Tickets {
			if len(jobs) >= r.Parallel {
				break
			}
			key := filepath.Base(t.File)
			old, exists := s.Results[key]
			if exists && !resumable(old.State) {
				continue
			}
			if attempted[key] {
				continue
			}
			if conflicts(t, s.Plan, s.Results, jobs) {
				continue
			}
			target, wait, err := r.target(ctx, *s, t)
			if err != nil {
				return err
			}
			if wait {
				continue
			}
			if exists && (old.Base != "" || old.BaseSHA != "") && (old.Base != target.Base || old.BaseSHA != target.SHA) {
				if old.Directory == "" && old.HeadSHA == "" {
					old.Base, old.BaseSHA = target.Base, target.SHA
					s.Results[key] = old
					if err := save(); err != nil {
						return err
					}
				} else {
					next, e := r.Driver.Reconcile(ctx, t, target, old)
					if e != nil {
						r.reconcileFailed(s, key, old, next, e)
						if saveErr := save(); saveErr != nil {
							return saveErr
						}
						continue
					}
					if next.RunID == "" {
						next.RunID = old.RunID
					}
					s.Results[key] = next
					if err := save(); err != nil {
						return err
					}
					old = next
				}
			}
			if !exists {
				old = Result{RunID: runID(s.Plan, t), State: "prepared", Base: target.Base, BaseSHA: target.SHA}
				s.Results[key] = old
				if err := save(); err != nil {
					return err
				}
			}
			old.Dependencies = target.Dependencies
			old.DependencyInvalid = false
			old.State = "running"
			old.StopReason = ""
			s.Results[key] = old
			if err := save(); err != nil {
				return err
			}
			jobs = append(jobs, seriesJob{t, target, old})
			attempted[key] = true
			go func(job seriesJob) {
				result, err := r.Driver.Execute(jobCtx, job.ticket, job.target, &job.result)
				completed <- completion{job.ticket, result, err}
			}(jobs[len(jobs)-1])
		}
		return nil
	}
	var firstErr error
	if err := fill(); err != nil {
		firstErr = err
		cancel()
	}
	launched := len(jobs) > 0
	var ticks <-chan time.Time
	if r.Watch {
		ticker := time.NewTicker(r.PollInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for len(jobs) > 0 {
		var item completion
		select {
		case item = <-completed:
			key := filepath.Base(item.ticket.File)
			for i, job := range jobs {
				if filepath.Base(job.ticket.File) == key {
					jobs = append(jobs[:i], jobs[i+1:]...)
					break
				}
			}
			if firstErr != nil {
				continue
			}
			current := s.Results[key]
			if current.State == "waiting_base" && !attentionState(item.result.State) && (item.err == nil || isDependencyInvalidation(item.err)) {
				item.result = completeResult(current, item.result)
				item.result.State, item.result.StopReason = current.State, current.StopReason
				item.result.DependencyInvalid = true
				s.Results[key] = item.result
				item.err = nil
			}
			if item.err != nil {
				r.reconcileFailed(s, key, current, item.result, item.err)
				write("%s %s: %v\n", key, s.Results[key].State, item.err)
			} else {
				item.result = completeResult(current, item.result)
				if !knownState(item.result.State) {
					firstErr = fmt.Errorf("%s returned an invalid run state", key)
					cancel()
					continue
				}
				s.Results[key] = item.result
				write("%s %s\n", key, item.result.State)
			}
			if err := save(); err != nil {
				firstErr = err
				cancel()
				continue
			}
		case <-ticks:
			if firstErr != nil {
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			firstErr = err
			cancel()
			continue
		}
		// Reconcile can execute a worker, so defer repairs until active jobs drain.
		deferred, err := r.observeReady(ctx, s, save, write, len(jobs) == 0)
		if err != nil {
			firstErr = err
			cancel()
			continue
		}
		if !deferred && attention(*s) == nil {
			if err := fill(); err != nil {
				firstErr = err
				cancel()
			}
		}
	}
	return launched, firstErr
}

func completeResult(current, returned Result) Result {
	if returned.RunID == "" {
		returned.RunID = current.RunID
	}
	if returned.Base == "" {
		returned.Base = current.Base
	}
	if returned.BaseSHA == "" {
		returned.BaseSHA = current.BaseSHA
	}
	if returned.Dependencies == nil {
		returned.Dependencies = current.Dependencies
	}
	return returned
}

func (r Runner) reconcileFailed(s *State, key string, current, returned Result, err error) {
	if knownState(returned.State) && returned.State != "" {
		returned = completeResult(current, returned)
		if returned.StopReason == "" {
			returned.StopReason = err.Error()
		}
		if !attentionState(returned.State) {
			returned.State = "blocked"
		}
		s.Results[key] = returned
		return
	}
	current.State = "blocked"
	current.StopReason = err.Error()
	s.Results[key] = current
}

func (r Runner) target(ctx context.Context, s State, t Ticket) (Target, bool, error) {
	deps := t.DependsOn
	if len(deps) > 1 {
		vector := make([]Dependency, 0, len(deps))
		unmerged := false
		for _, name := range deps {
			parent := s.Results[name]
			if parent.State != "ready" && parent.State != "merged" {
				return Target{}, true, nil
			}
			vector = append(vector, Dependency{Ticket: name, RunID: parent.RunID, Branch: parent.Branch, Base: parent.Base, BaseSHA: parent.BaseSHA, HeadSHA: parent.HeadSHA, State: parent.State, MergeSHA: parent.MergeSHA, PRNumber: parent.PRNumber})
			unmerged = unmerged || parent.State == "ready"
		}
		target, err := r.Driver.Base(ctx, s.Plan.Base)
		if err != nil {
			return Target{}, false, err
		}
		target.Ancestors = mergedAncestors(s, t)
		target.Dependencies = vector
		if unmerged {
			driver, ok := r.Driver.(CombinedDriver)
			if !ok {
				return Target{}, true, nil
			}
			target, err = driver.Combine(ctx, t, target, vector)
		}
		return target, false, err
	}
	if len(deps) == 1 {
		parent := s.Results[deps[0]]
		switch parent.State {
		case "ready":
			target, e := r.Driver.Base(ctx, parent.Branch)
			if e != nil {
				return Target{}, false, e
			}
			if target.SHA != parent.HeadSHA {
				return Target{}, false, fmt.Errorf("parent %s no longer resolves to its checkpointed head", deps[0])
			}
			return target, false, nil
		case "merged":
			target, e := r.Driver.Base(ctx, s.Plan.Base)
			target.Ancestors = mergedAncestors(s, t)
			return target, false, e
		default:
			return Target{}, true, nil
		}
	}
	target, e := r.Driver.Base(ctx, s.Plan.Base)
	return target, false, e
}
func mergedAncestors(s State, t Ticket) []string {
	seen := map[string]bool{}
	values := []string{}
	byName := ticketMap(s.Plan.Tickets)
	var collect func(string)
	collect = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		parent := byName[name]
		for _, d := range parent.DependsOn {
			collect(d)
		}
		if result := s.Results[name]; result.State == "merged" && result.MergeSHA != "" {
			values = append(values, result.MergeSHA)
		}
	}
	for _, d := range t.DependsOn {
		collect(d)
	}
	return values
}
func resumable(state string) bool {
	switch state {
	case "", "prepared", "running", "implementing", "repairing", "checking", "publishing", "ci", "reviewing":
		return true
	}
	return false
}
func knownState(state string) bool {
	switch state {
	case "waiting_base", "prepared", "running", "implementing", "repairing", "checking", "publishing", "ci", "reviewing", "ready", "merged", "blocked", "waiting_human", "waiting_for_human", "awaiting_reviewer", "failed", "stopped":
		return true
	}
	return false
}
func attention(s State) error {
	for _, t := range s.Plan.Tickets {
		r := s.Results[filepath.Base(t.File)]
		if attentionState(r.State) && !r.DependencyInvalid {
			return fmt.Errorf("series requires attention: %s: %s", filepath.Base(t.File), r.StopReason)
		}
	}
	return nil
}

func attentionState(state string) bool {
	return state == "blocked" || state == "waiting_human" || state == "waiting_for_human" || state == "awaiting_reviewer" || state == "failed" || state == "stopped"
}
func conflicts(t Ticket, p Plan, results map[string]Result, jobs []seriesJob) bool {
	for _, j := range jobs {
		if overlaps(t, j.ticket) {
			return true
		}
	}
	for _, other := range p.Tickets {
		if filepath.Base(other.File) == filepath.Base(t.File) {
			continue
		}
		r, ok := results[filepath.Base(other.File)]
		if ok && r.State == "running" && overlaps(t, other) {
			return true
		}
	}
	return false
}
func runID(p Plan, t Ticket) string {
	sum := sha256.Sum256([]byte(p.Root + "\x00" + p.Reference + "\x00" + p.DefinitionSHA + "\x00" + filepath.Base(t.File)))
	return hex.EncodeToString(sum[:12])
}

var runIDRE = regexp.MustCompile(`^[0-9a-f]{24}$`)

func runIDPattern(s string) bool { return runIDRE.MatchString(s) }
func allMerged(s State) bool {
	for _, t := range s.Plan.Tickets {
		if s.Results[filepath.Base(t.File)].State != "merged" {
			return false
		}
	}
	return true
}

// invalidate binds the wait marker to the run journal as well as the feature.
func (r Runner) invalidate(ctx context.Context, s *State, t Ticket, save func() error) error {
	key := filepath.Base(t.File)
	result, exists := s.Results[key]
	if !exists || result.State == "merged" || result.DependencyInvalid {
		return nil
	}
	if driver, ok := r.Driver.(CombinedDriver); ok {
		if err := driver.Invalidate(ctx, t, result); err != nil {
			return err
		}
	}
	result.DependencyInvalid = true
	if !attentionState(result.State) && (result.Directory != "" || result.HeadSHA != "" || result.State == "running") {
		result.State, result.StopReason = "waiting_base", "dependency base changed; waiting for parent readiness"
	}
	s.Results[key] = result
	return save()
}
func (r Runner) invalidateDescendants(ctx context.Context, s *State, parent Ticket, save func() error) error {
	for _, child := range s.Plan.Tickets {
		for _, name := range child.DependsOn {
			if name == filepath.Base(parent.File) {
				if err := r.invalidate(ctx, s, child, save); err != nil {
					return err
				}
				if err := r.invalidateDescendants(ctx, s, child, save); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func isDependencyInvalidation(err error) bool {
	var invalid interface{ DependencyInvalidated() bool }
	return errors.As(err, &invalid) && invalid.DependencyInvalidated()
}
