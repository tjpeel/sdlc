package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"io"
	"path/filepath"
	"time"
)

func diagnoseCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "saved run ID or unique prefix")
	structured := flags.Bool("json", false, "allowlisted saved-state JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc diagnose --run RUN_ID [--json]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 {
		return errors.New("diagnose requires --run RUN_ID")
	}
	if err := ctx.Err(); err != nil {
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
	view, err := dashboard.Select(views, *id)
	if err != nil {
		return err
	}
	if !view.Available || view.Preparation || view.Journal == nil {
		return errors.New("saved run checkpoint is unavailable")
	}
	directory, err := workrun.RunDirectory(view.Root, view.Reference, filepath.Base(view.Ticket), view.ID, false)
	if err != nil || directory != view.Directory {
		return errors.New("recorded run directory does not match its identity")
	}
	j, err := workrun.Load(directory)
	if err != nil {
		return errors.New("saved run checkpoint could not be loaded")
	}
	if j.ID != view.ID || j.Plan.Root != view.Root || j.Plan.Reference != view.Reference || j.Plan.Ticket != view.Ticket || !j.UpdatedAt.Equal(view.UpdatedAt) {
		return errors.New("run checkpoint changed; select the run again")
	}
	view.Journal = &j
	snapshot, err := runstatus.DiagnosticSnapshot(view)
	if err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(output).Encode(snapshot)
	}
	fmt.Fprintf(output, "Run %s | saved state %s | resume %s\nSaved checks passed: %t | saved CI: %s | saved review: %s\n", snapshot.RunID, snapshot.State, snapshot.ResumeState, snapshot.Evidence.Passed, snapshot.CI, snapshot.Review)
	return printDiagnosticAction(output, snapshot.NextAction, snapshot.RunID)
}

func printDiagnosticAction(output io.Writer, action runstatus.NextAction, id string) error {
	if _, err := fmt.Fprintln(output, "Next:", action.Instruction); err != nil {
		return err
	}
	if action.Command != "" {
		if _, err := fmt.Fprintln(output, "  "+action.Command); err != nil {
			return err
		}
	}
	if action.Command == "" {
		return nil
	}
	_, err := fmt.Fprintf(output, "Saved-state dump: sdlc diagnose --run %s --json\n", id)
	return err
}

// All stopped controllers use the same fixed next-action projection as diagnose.
func printRunAttention(output io.Writer, j workrun.Journal) error {
	return printDiagnosticAction(output, runstatus.NextRunAction(j, false), j.ID)
}
