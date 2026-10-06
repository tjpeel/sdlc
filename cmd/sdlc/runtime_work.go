package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func runtimeSavedWorkGuardFromEnvironment(source string) func(context.Context, runtimeimage.State) error {
	return func(ctx context.Context, previous runtimeimage.State) error {
		manager, err := runtimeimage.New(io.Discard, io.Discard)
		if err != nil {
			return err
		}
		return runtimeSavedWorkGuard(manager.Directory, source)(ctx, previous)
	}
}

// Only known roots can be inspected: registry, project catalogue, source and cwd.
// The runtime writer lock excludes new connected runs while this check executes.
func runtimeSavedWorkGuard(stateDirectory, source string) func(context.Context, runtimeimage.State) error {
	return func(ctx context.Context, previous runtimeimage.State) error {
		roots := map[string]bool{}
		if caller := os.Getenv("SDLC_UPDATE_PROJECT_ROOT"); caller != "" {
			if !filepath.IsAbs(caller) || filepath.Clean(caller) != caller {
				return fmt.Errorf("update caller project root must be absolute and clean")
			}
			roots[caller] = true
		}
		blocked := map[string]bool{}
		views, err := runstatus.New(stateDirectory).List(time.Now().UTC())
		if err != nil {
			return fmt.Errorf("cannot inspect saved runs: %w", err)
		}
		for _, view := range views {
			if view.Root != "" {
				roots[view.Root] = true
			}
			if !view.Available && !view.Preparation {
				return fmt.Errorf("cannot verify saved run %s; resolve its saved state before replacing the runtime; use sdlc update --cli-only", view.ID)
			}
			if view.Journal != nil && view.Journal.ImageID == previous.ImageID && (view.State != "ready" || view.Live) {
				blocked["run "+view.ID] = true
			}
		}
		projects, err := projectList(ctx)
		if err != nil {
			return fmt.Errorf("cannot inspect project catalogue: %w", err)
		}
		for _, p := range projects {
			roots[p.Root] = true
		}
		for _, candidate := range []string{source, previous.Source} {
			if candidate == "" {
				continue
			}
			root, err := filepath.Abs(candidate)
			if err != nil {
				return err
			}
			// An explicit replacement source may repair a moved build-only clone.
			// Registry/catalogue roots remain mandatory even at the old path.
			required := candidate == source || source == ""
			if alreadyRequired, exists := roots[root]; !exists || !alreadyRequired {
				roots[root] = required
			}
		}
		for root, required := range roots {
			canonical, err := filepath.EvalSymlinks(root)
			if err != nil {
				if !required && errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("cannot verify saved work root %s: %w; use sdlc update --cli-only", root, err)
			}
			root = canonical
			if err := scanRuntimeWork(ctx, root, previous.ImageID, blocked); err != nil {
				return fmt.Errorf("cannot verify saved work in %s: %w; use sdlc update --cli-only", root, err)
			}
		}
		if len(blocked) != 0 {
			ids := make([]string, 0, len(blocked))
			for id := range blocked {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return fmt.Errorf("runtime replacement would prevent resuming %s; complete this saved work first, or use sdlc update --cli-only", strings.Join(ids, ", "))
		}
		return nil
	}
}

// Walk only the fixed checkpoint layout, never workspaces, output or ticket bodies.
func runtimeWorkDirectories(path string) ([]string, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		// Check existing ancestors even when the leaf is absent. A symlinked
		// .sdlc must not turn a missing work directory into a silent success.
		ancestor := filepath.Dir(path)
		for {
			if _, err := os.Lstat(ancestor); err == nil {
				if err := viewDirectory(ancestor); err != nil {
					return nil, err
				}
				return nil, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			next := filepath.Dir(ancestor)
			if next == ancestor {
				return nil, fmt.Errorf("saved work ancestor is unavailable")
			}
			ancestor = next
		}
	}
	if err := viewDirectory(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(513)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 512 {
		return nil, fmt.Errorf("too many saved work entries to verify safely")
	}
	result := []string{}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("saved work directories must not be symlinks")
		}
		if e.IsDir() {
			result = append(result, filepath.Join(path, e.Name()))
		}
	}
	return result, nil
}

func scanRuntimeWork(ctx context.Context, root, image string, blocked map[string]bool) error {
	refs, err := runtimeWorkDirectories(filepath.Join(root, ".sdlc", "work"))
	if err != nil {
		return err
	}
	ids := regexp.MustCompile(`^[0-9a-f]{24}$`)
	checkpoints := 0
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		seriesPath := filepath.Join(ref, "series")
		if _, err := os.Lstat(seriesPath); err == nil {
			series, err := workseries.Load(seriesPath)
			if err != nil {
				return fmt.Errorf("feature %s has an invalid checkpoint: %w", filepath.Base(ref), err)
			}
			if len(series.Settings) != 0 {
				settings, err := loadSeriesSettings(series.Settings)
				if err != nil {
					return fmt.Errorf("feature %s has invalid frozen settings", filepath.Base(ref))
				}
				complete := len(series.Results) == len(series.Plan.Tickets)
				for _, ticket := range series.Plan.Tickets {
					if series.Results[ticket.File].State != "merged" {
						complete = false
					}
				}
				if settings.ImageID == image && !complete {
					blocked["feature "+filepath.Base(ref)] = true
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tickets, err := runtimeWorkDirectories(filepath.Join(ref, "runs"))
		if err != nil {
			return err
		}
		for _, ticket := range tickets {
			runs, err := runtimeWorkDirectories(ticket)
			if err != nil {
				return err
			}
			for _, run := range runs {
				checkpoints++
				if checkpoints > 4096 {
					return fmt.Errorf("too many saved checkpoints to verify safely")
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !ids.MatchString(filepath.Base(run)) {
					return fmt.Errorf("invalid saved run directory")
				}
				if _, err := os.Lstat(filepath.Join(run, "journal.json")); errors.Is(err, os.ErrNotExist) {
					continue
				}
				journal, err := workrun.Load(run)
				if err != nil {
					return fmt.Errorf("run %s has an invalid checkpoint: %w", filepath.Base(run), err)
				}
				journalRoot, err := filepath.EvalSymlinks(journal.Plan.Root)
				if err != nil || journalRoot != root || journal.Plan.Reference != filepath.Base(ref) {
					return fmt.Errorf("saved run identity does not match its directory")
				}
				if journal.ImageID == image {
					if journal.State != "ready" {
						blocked["run "+journal.ID] = true
					} else {
						lock, err := probeRunController(run)
						if err != nil {
							blocked["run "+journal.ID] = true
						}
						if lock != nil {
							lock.Close()
						}
					}
				}
			}
		}
	}
	return nil
}
