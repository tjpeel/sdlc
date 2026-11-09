package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func runtimeRemoveCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("runtime remove", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() { fmt.Fprintln(out, "Usage: sdlc runtime remove [--force] NAME"); flags.PrintDefaults() }
	force := flags.Bool("force", false, "allow removal when stopped saved work references this runtime")
	// Accept the conventional flag position after the runtime name as well.
	if len(args) == 2 && args[1] == "--force" && !strings.HasPrefix(args[0], "-") {
		args = []string{args[1], args[0]}
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: sdlc runtime remove [--force] NAME")
	}
	name := flags.Arg(0)
	if name == "local" {
		name = ""
	}
	if err := runtimeimage.ValidateName(name); err != nil {
		return err
	}
	manager, err := runtimeimage.NewNamed(io.Discard, io.Discard, name)
	if err != nil {
		return err
	}
	lease, err := leaseRuntimeRun(ctx, manager.Directory, out)
	if err != nil {
		return err
	}
	defer lease.Close()
	selected, err := runtimeimage.ReadDefaultName(manager.Directory)
	if err != nil {
		return err
	}
	if name == selected {
		return fmt.Errorf("cannot remove the default runtime; select another runtime first")
	}
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	views, err := runstatus.New(manager.Directory).List(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("cannot inspect saved runs: %w", err)
	}
	var referenced []string
	for _, view := range views {
		if view.Journal != nil && workrun.ReferencesRuntimeImage(*view.Journal, state.ImageID) {
			if view.Live {
				return fmt.Errorf("cannot remove runtime while run %s has an active controller", view.ID)
			}
			referenced = append(referenced, view.ID)
		}
	}
	if len(referenced) != 0 && !*force {
		return fmt.Errorf("runtime is referenced by saved runs %v; use --force to remove it", referenced)
	}
	err = runtimeSavedWorkGuard(manager.Directory, "")(ctx, state)
	var saved *runtimeSavedWorkError
	if err != nil && (!*force || !errors.As(err, &saved)) {
		return err
	}
	if err != nil || len(referenced) != 0 {
		if err != nil {
			referenced = append(referenced, saved.ids...)
		}
		fmt.Fprintf(out, "Removing runtime used by stopped saved work: %v. Those runs may no longer resume; their files are retained.\n", referenced)
	}
	if err := manager.Remove(ctx, state); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed runtime %s (%s).\n", flags.Arg(0), state.ImageID)
	return nil
}
