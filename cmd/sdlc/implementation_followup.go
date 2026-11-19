package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/tjpeel/sdlc/internal/workrun"
	"io"
	"strings"
)

func implementationFollowupCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("accept-implementation-followup", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("run", "", "saved run")
	path := f.String("file", "", "exact follow-up text")
	dry := f.Bool("dry-run", false, "preview only")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, "Usage: sdlc accept-implementation-followup --run RUN_ID --file FILE [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *path == "" || f.NArg() != 0 {
		return fmt.Errorf("accept-implementation-followup requires --run and --file")
	}
	data, err := readHumanAnswer(*path)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(data))
	a, err := resolveRunAction(ctx, *id, false)
	if err != nil {
		return err
	}
	j := a.View.Journal
	if err := workrun.ValidateRejectedImplementationFollowup(a.View.Directory, *j, text); err != nil {
		return err
	}
	fmt.Fprintf(out, "Accept outstanding implementation follow-up for %s at candidate %s.\n%s\n", j.ID, j.Evidence.Head, text)
	fmt.Fprintln(out, "The recorded human answer is retained. The follow-up remains outstanding and must appear in the PR body; checks, feedback, CI and independent review remain required.")
	if *dry {
		fmt.Fprintln(out, "Preview only; nothing recorded.")
		return nil
	}
	if err := workrun.RecordImplementationFollowup(a.View.Directory, j.UpdatedAt, text); err != nil {
		return err
	}
	fmt.Fprintf(out, "Follow-up recorded. Resume with: sdlc resume --run %s\n", j.ID)
	return nil
}
