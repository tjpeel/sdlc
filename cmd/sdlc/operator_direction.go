package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func operatorDirectionCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("operator-direction", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("run", "", "saved run")
	path := f.String("file", "", "operator direction file")
	dry := f.Bool("dry-run", false, "preview only")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, "Usage: sdlc operator-direction --run RUN_ID --file FILE [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *path == "" || f.NArg() != 0 {
		return fmt.Errorf("operator-direction requires --run and --file")
	}
	data, err := readHumanAnswer(*path)
	if err != nil {
		return err
	}
	action, err := resolveRunAction(ctx, *id, false)
	if err != nil {
		return err
	}
	j := action.View.Journal
	if err := workrun.ValidateOperatorDirection(*j, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(out, "Operator direction for %s, after attempt %d.\n%s\n", j.ID, j.Attempt, string(data))
	fmt.Fprintln(out, "The direction will accompany the next implementation turn and later independent review. Existing session, workspace, feedback and verification gates remain in place.")
	if *dry {
		fmt.Fprintln(out, "Preview only; nothing recorded.")
		return nil
	}
	if err := workrun.RecordOperatorDirection(action.View.Directory, j.UpdatedAt, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(out, "Direction recorded. Resume with: sdlc resume --run %s\n", j.ID)
	return nil
}
