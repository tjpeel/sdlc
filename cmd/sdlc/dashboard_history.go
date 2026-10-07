package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/savedwork"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const dashboardForgetUsage = "Usage: sdlc dashboard remove (--run RUN_ID | --all) [--scope project|installation] [--dry-run | --yes]\nAlias: sdlc dashboard forget (identical behaviour)\nHides stopped dashboard registrations only. All saved work is retained and can still block runtime updates.\n--all previews by default; add --yes to clear the listed registrations. Scope defaults to installation.\nResuming a retained run registers it again. Active controllers cannot be hidden.\nUse storage purge to delete saved run files, or work archive to preserve a whole reference outside active work."

const dashboardExportUsage = "Usage: sdlc dashboard export --run RUN_ID --to PRIVATE_DIRECTORY\nWrites a private checkpoint report into an existing owned 0700 directory.\nReports exclude raw logs, transcripts, authentication settings, input contents and source code.\nKeep reports private: check commands and model summaries can still contain sensitive material.\nThis is a point-in-time summary, not a full resumable backup. Existing reports are never overwritten."

func dashboardExportCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("dashboard export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "selected run ID or unique prefix")
	destination := flags.String("to", "", "existing private report directory")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardExportUsage)
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *destination == "" || flags.NArg() != 0 {
		return fmt.Errorf("export requires --run RUN_ID and --to PRIVATE_DIRECTORY")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*destination)
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	views, err := runstatus.New(runtime.Directory).List(time.Now().UTC())
	if err != nil {
		return err
	}
	v, err := dashboard.Select(views, *id)
	if err != nil {
		return err
	}
	path, err := runstatus.ExportReport(v, absolute)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Private checkpoint report saved at %q.\nRaw run artifacts remain at %q; this summary cannot resume the run.\n", dashboard.SafeText(path), dashboard.SafeText(v.Directory))
	return err
}

func dashboardForgetCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("dashboard forget", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "stopped run ID or unique prefix")
	all := flags.Bool("all", false, "all stopped dashboard registrations in the selected scope")
	scope := flags.String("scope", "installation", "project or installation")
	dryRun := flags.Bool("dry-run", false, "preview without hiding registrations")
	yes := flags.Bool("yes", false, "confirm clearing all listed registrations")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardForgetUsage)
		return err
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 || ((*id != "") == *all) || (*dryRun && *yes) || (*scope != "project" && *scope != "installation") {
		return fmt.Errorf("forget requires exactly --run RUN_ID or --all, --scope project|installation, and at most one of --dry-run or --yes")
	}
	if *id != "" {
		if err := workrun.ValidateRunSelector(*id); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	registry := runstatus.New(runtime.Directory)
	views, err := registry.List(time.Now().UTC())
	if err != nil {
		return err
	}
	if *scope == "project" {
		root, err := currentWorkRoot(ctx)
		if err != nil {
			return err
		}
		filtered := views[:0]
		for _, view := range views {
			if sameProjectRoot(view.Root, root) {
				filtered = append(filtered, view)
			}
		}
		views = filtered
	}
	if !*all {
		v, err := dashboard.Select(views, *id)
		if err != nil {
			for _, view := range views {
				if strings.HasPrefix(view.ID, *id) {
					return err // Preserve ambiguity; never mutate a guessed match.
				}
			}
			return explainForgottenRun(ctx, *id, err, output)
		}
		views = []runstatus.View{v}
	}
	preview := *dryRun || (*all && !*yes)
	var failures []string
	var b strings.Builder
	for _, view := range views {
		if view.Live {
			if !*all {
				return fmt.Errorf("run %s reports a live controller; stop it before removing its dashboard entry", view.ID)
			}
			fmt.Fprintf(&b, "Kept active run %s; stop its controller before forgetting.\n", view.ID)
			continue
		}
		if preview {
			fmt.Fprintf(&b, "Would hide run %s. Saved work remains at %q.\n", view.ID, dashboard.SafeText(view.Directory))
			continue
		}
		if err := registry.Forget(view.ID); err != nil {
			failures = append(failures, view.ID+": "+err.Error())
			continue
		}
		fmt.Fprintf(&b, "Removed run %s from the dashboard only. Saved work remains at %q.\n", view.ID, dashboard.SafeText(view.Directory))
	}
	if preview {
		if *all {
			fmt.Fprintln(&b, "No registrations changed. Add --yes to clear all listed stopped registrations; live controllers are rechecked when applying.")
		} else {
			fmt.Fprintln(&b, "No registrations changed. Repeat without --dry-run to hide this entry; its controller is rechecked when applying.")
		}
	}
	fmt.Fprintln(&b, "Saved checkpoints can still block runtime updates. Use storage purge for permanent deletion, or work archive to retain a complete reference.")
	if _, err := io.WriteString(output, b.String()); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("some registrations could not be forgotten: %s", strings.Join(failures, "; "))
	}
	return nil
}

func explainForgottenRun(ctx context.Context, selector string, lookupErr error, output io.Writer) error {
	root, err := currentWorkRoot(ctx)
	if err != nil {
		return lookupErr
	}
	runs, err := savedwork.Discover(ctx, root)
	if err != nil {
		return fmt.Errorf("%w; cannot inspect retained work: %v", lookupErr, err)
	}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	id, err := workrun.SelectRunID(ids, selector)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.ID == id {
			_, err = fmt.Fprintf(output, "Run %s is already absent from this dashboard scope.\nSaved work remains at %q and can still block runtime updates.\nPreview deletion: sdlc storage purge --run %s\nPreserve the whole reference: sdlc work archive --reference %q\n", id, dashboard.SafeText(run.Directory), id, dashboard.SafeText(run.Reference))
			return err
		}
	}
	return lookupErr
}

// Own a separate descriptor so the dashboard closes its input without closing
// the host process's standard input. Bubble Tea restores terminal settings.
func openDashboardInput() (*os.File, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, fmt.Errorf("live dashboard requires terminal input")
	}
	return os.Open("/dev/tty")
}
