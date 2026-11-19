package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func resumeModelCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("resume-model", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("run", "", "saved run")
	model := f.String("model", "", "implementation model")
	effort := f.String("effort", "", "reasoning effort")
	dry := f.Bool("dry-run", false, "preview only")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, "Usage: sdlc resume-model --run RUN_ID --model MODEL --effort LEVEL [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *model == "" || *effort == "" || f.NArg() != 0 {
		return fmt.Errorf("resume-model requires --run, --model and --effort")
	}
	action, err := inspectRunAction(ctx, *id)
	if err != nil {
		return err
	}
	j := action.View.Journal
	prior := workrun.EffectiveImplementationModel(*j)
	selected := workrun.Model{Provider: prior.Provider, Name: *model, Effort: *effort}
	if err := workrun.ValidateResumeModel(*j, selected); err != nil {
		return err
	}
	fmt.Fprintf(out, "Implementation model for %s: %s/%s (%s) -> %s/%s (%s).\n", j.ID, prior.Provider, prior.Name, prior.Effort, selected.Provider, selected.Name, selected.Effort)
	fmt.Fprintln(out, "The existing native session, provider, reviewer, workspace, history and verification gates remain unchanged. Model and effort are requested settings; provider support and effective effort are not guaranteed.")
	if *dry {
		fmt.Fprintln(out, "Preview only; nothing recorded.")
		return nil
	}
	if err := workrun.RecordResumeModel(action.View.Directory, j.UpdatedAt, selected); err != nil {
		return err
	}
	if j.State == "waiting_for_human" {
		fmt.Fprintf(out, "Selection recorded. Answer the pending question with: sdlc answer --run %s\n", j.ID)
	} else {
		fmt.Fprintf(out, "Selection recorded. Resume with: sdlc resume --run %s\n", j.ID)
	}
	return nil
}
