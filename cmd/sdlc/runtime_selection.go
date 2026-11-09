package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func runtimeSelectionCommand(ctx context.Context, args []string, out io.Writer) error {
	manager, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	if len(args) == 2 && args[1] == "--help" && args[0] != "migrate" {
		if args[0] == "use" {
			fmt.Fprintln(out, "Usage: sdlc runtime use NAME (use local for the original runtime)")
		} else {
			fmt.Fprintln(out, "Usage: sdlc runtime list")
		}
		return nil
	}
	if args[0] == "migrate" {
		return runtimeMigrationCommand(ctx, args[1:], out)
	}
	if args[0] == "use" {
		if len(args) != 2 {
			return fmt.Errorf("usage: sdlc runtime use NAME (use local for the original runtime)")
		}
		name := args[1]
		if name == "local" {
			name = ""
		}
		if err := runtimeimage.ValidateName(name); err != nil {
			return err
		}
		lease, err := leaseRuntimeRun(ctx, manager.Directory, out)
		if err != nil {
			return err
		}
		defer lease.Close()
		selected, err := runtimeimage.NewNamed(io.Discard, io.Discard, name)
		if err != nil {
			return err
		}
		if _, err := selected.Status(ctx); err != nil {
			return err
		}
		if err := runtimeimage.WriteDefaultName(manager.Directory, name); err != nil {
			return err
		}
		fmt.Fprintf(out, "Default for new runs: %s\n", args[1])
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: sdlc runtime list")
	}
	selected, err := runtimeimage.ReadDefaultName(manager.Directory)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(manager.Directory)
	if err != nil {
		return err
	}
	names := []string{}
	for _, entry := range entries {
		filename := entry.Name()
		if filename == "runtime.json" {
			names = append(names, "")
			continue
		}
		if !strings.HasPrefix(filename, "runtime.") || !strings.HasSuffix(filename, ".json") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filename, "runtime."), ".json")
		if name != "" && runtimeimage.ValidateName(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	knownImages := map[string]bool{}
	views, err := runstatus.New(manager.Directory).List(time.Now().UTC())
	if err != nil {
		return err
	}
	projects, err := projectList(ctx)
	if err != nil {
		return err
	}
	roots := map[string]bool{}
	registered := map[string]bool{}
	for _, view := range views {
		registered[view.ID] = true
		if view.Root != "" {
			roots[view.Root] = true
		}
	}
	for _, project := range projects {
		roots[project.Root] = true
	}
	for _, name := range names {
		m, err := runtimeimage.NewNamed(io.Discard, io.Discard, name)
		if err != nil {
			return err
		}
		state, err := m.RecordedState()
		if err != nil {
			return fmt.Errorf("runtime %q: %w", name, err)
		}
		_, availability := m.Status(ctx)
		knownImages[state.ImageID] = true
		label := name
		if label == "" {
			label = "local"
		}
		mark := ""
		if name == selected {
			mark = " (default for new runs)"
		}
		fmt.Fprintf(out, "%s%s  %s\n", label, mark, state.ImageID)
		if availability != nil {
			fmt.Fprintf(out, "  unavailable: %v\n", availability)
		}
		if state.Inventory != nil {
			for _, dep := range state.Inventory.Dependencies {
				if dep.Kind == "dotnet-sdk" || dep.Kind == "dotnet-runtime" {
					fmt.Fprintf(out, "  %s: %s\n", dep.Name, dep.Version)
				}
			}
		}
		additional := map[string]bool{}
		for root := range roots {
			canonical, err := filepath.EvalSymlinks(root)
			if err != nil {
				fmt.Fprintf(out, "  saved-work scan incomplete for a project: %v\n", err)
				continue
			}
			if err := scanRuntimeWork(ctx, canonical, state.ImageID, additional); err != nil {
				fmt.Fprintf(out, "  saved-work scan incomplete for a project: %v\n", err)
			}
		}
		extra := make([]string, 0, len(additional))
		for item := range additional {
			if !strings.HasPrefix(item, "run ") || !registered[strings.TrimPrefix(item, "run ")] {
				extra = append(extra, item)
			}
		}
		sort.Strings(extra)
		for _, item := range extra {
			fmt.Fprintf(out, "  unfinished %s (saved-work scan)\n", item)
		}
		for _, view := range views {
			if view.Journal == nil {
				continue
			}
			if view.Journal.ImageID == state.ImageID {
				fmt.Fprintf(out, "  run %s: %s (%s)\n", view.ID, view.State, view.Reference)
			}
			for _, epoch := range view.Journal.RuntimeMigrations {
				if epoch.PreviousImageID == state.ImageID {
					fmt.Fprintf(out, "  run %s: prior runtime retained (%s)\n", view.ID, view.Reference)
				}
			}
		}
	}
	for _, view := range views {
		if view.Journal != nil && !knownImages[view.Journal.ImageID] {
			fmt.Fprintf(out, "Run %s: %s (%s); pinned image %s has no current runtime record.\n", view.ID, view.State, view.Reference, view.Journal.ImageID)
		}
		if view.Journal != nil {
			for _, epoch := range view.Journal.RuntimeMigrations {
				if !knownImages[epoch.PreviousImageID] {
					fmt.Fprintf(out, "Run %s: previous image %s has no current runtime record.\n", view.ID, epoch.PreviousImageID)
				}
			}
		}
		if !view.Available {
			fmt.Fprintf(out, "Run %s is unavailable; its runtime association could not be verified.\n", view.ID)
		}
	}
	fmt.Fprintln(out, "Run associations cover the registered catalogue and discoverable unfinished saved work; archived and unknown projects are excluded.")
	return nil
}

// runtimeMigrationCommand migrates only a stopped unpublished implementation
// hold, without changing its workspace or credentials.
func runtimeMigrationCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("runtime migrate", flag.ContinueOnError)
	flags.SetOutput(out)
	id := flags.String("run", "", "saved run ID")
	name := flags.String("to", "", "target runtime name")
	dry := flags.Bool("dry-run", false, "preview without writing")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == "" || *name == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: sdlc runtime migrate --run ID --to NAME [--dry-run]")
	}
	targetName := *name
	if targetName == "local" {
		targetName = ""
	}
	target, err := runtimeimage.NewNamed(io.Discard, io.Discard, targetName)
	if err != nil {
		return err
	}
	var lease *os.File
	if !*dry {
		lease, err = leaseRuntimeRun(ctx, target.Directory, out)
		if err != nil {
			return err
		}
		defer lease.Close()
	}
	state, err := target.Status(ctx)
	if err != nil {
		return err
	}
	action, err := inspectRunAction(ctx, *id)
	if err != nil {
		return err
	}
	if action.View.Live || action.View.FeatureOwned {
		return fmt.Errorf("runtime migration requires a stopped standalone run")
	}
	lock, err := probeRunController(action.View.Directory)
	if err != nil {
		return err
	}
	if lock == nil {
		return fmt.Errorf("runtime migration requires an existing controller lock")
	}
	defer lock.Close()
	original, err := workrun.Load(action.View.Directory)
	if err != nil {
		return err
	}
	if original.ID != action.View.ID || original.UpdatedAt.Format(time.RFC3339Nano) != action.Checkpoint {
		return fmt.Errorf("run checkpoint changed; retry migration")
	}
	if err := target.EnsureNoDependentContainers(ctx, original.ImageID); err != nil {
		return fmt.Errorf("old runtime still has dependent containers: %w", err)
	}
	if original.Workspace != filepath.Join(action.View.Directory, "workspace") {
		return fmt.Errorf("run workspace identity is invalid")
	}
	if err := workrun.VerifyFormatterSDKHold(action.View.Directory, original, state); err != nil {
		return err
	}
	if err := workrun.VerifyRuntimeMigrationInputs(action.View.Directory, original); err != nil {
		return err
	}
	next, err := workrun.PrepareRuntimeMigration(original, targetName, state)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Run %s: %s -> %s; workspace and prior native session retained; checks and review must be repeated.\n", original.ID, original.ImageID, state.ImageID)
	if *dry {
		return nil
	}
	if err := workrun.SaveRuntimeMigration(action.View.Directory, original, &next); err != nil {
		return err
	}
	fmt.Fprintf(out, "Runtime migration saved. Resume with sdlc resume --run %s\n", original.ID)
	return nil
}
