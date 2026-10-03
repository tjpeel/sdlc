package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

func runtimeCommand(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || (args[0] != "build" && args[0] != "status") {
		return fmt.Errorf("unknown runtime command; run sdlc --help")
	}
	flags := flag.NewFlagSet("runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var source string
	var offline bool
	if args[0] == "build" {
		flags.StringVar(&source, "source", "", "SDLC clone (uses saved source when omitted)")
	} else {
		flags.BoolVar(&offline, "offline", false, "verify the local image and list its inventory without checking upstream updates")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("runtime %s accepts only its named options", args[0])
	}
	manager, err := runtimeimage.New(output, diagnostics)
	if err != nil {
		return err
	}
	if args[0] == "status" {
		return runtimeStatus(ctx, manager, runtimeupdates.New(), offline, output)
	}
	state, err := manager.Build(ctx, source)
	if err != nil {
		return err
	}
	return printRuntime(output, state)
}

func printRuntime(output io.Writer, state runtimeimage.State) error {
	_, err := fmt.Fprintf(output, "Shared image: %s\nImage ID: %s\nSource revision: %s\n%s\n", runtimeimage.Image, state.ImageID, state.Revision, state.Tools)
	return err
}

type runtimeStatusManager interface {
	Status(context.Context) (runtimeimage.State, error)
	PackageUpdates(context.Context, runtimeimage.State) ([]runtimeimage.PackageUpdate, error)
}

func runtimeStatus(ctx context.Context, manager runtimeStatusManager, checker runtimeupdates.Checker, offline bool, output io.Writer) error {
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if err := printRuntime(output, state); err != nil {
		return err
	}
	if state.Inventory == nil {
		if _, err := fmt.Fprintln(output, "Dependency inventory unavailable for this older image; rebuild with sdlc runtime build to record all dependency versions and catalogue revisions."); err != nil {
			return err
		}
		if !offline {
			return fmt.Errorf("dependency check incomplete: this image needs a build inventory")
		}
		return nil
	}
	if !offline {
		if _, err := fmt.Fprintln(output, "Checking public upstream metadata (45-second limit)..."); err != nil {
			return err
		}
	}
	checkContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	report, err := checker.Check(checkContext, *state.Inventory, offline, func(ctx context.Context) ([]runtimeimage.PackageUpdate, error) {
		return manager.PackageUpdates(ctx, state)
	})
	if err != nil {
		return err
	}
	if err := report.Print(output); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if report.Incomplete() {
		return fmt.Errorf("dependency check incomplete; see unavailable results above")
	}
	return nil
}
