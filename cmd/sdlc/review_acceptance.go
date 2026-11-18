package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func reviewAcceptanceCommand(ctx context.Context, args []string, output io.Writer) error {
	f := flag.NewFlagSet("accept-review-findings", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("run", "", "run ID")
	path := f.String("reason-file", "", "file containing the owner's reason")
	dry := f.Bool("dry-run", false, "preview without recording")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc accept-review-findings --run RUN_ID --reason-file FILE [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *path == "" || f.NArg() != 0 {
		return fmt.Errorf("accept-review-findings requires --run and --reason-file")
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
	if err := workrun.ValidateReviewAcceptance(*j, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(output, "Review finding acceptance for %s, PR %s, head %s, attempt %d\n%s\n", action.View.ID, j.Publication.URL, j.Publication.HeadSHA, j.Attempt, string(data))
	for _, finding := range j.Outcome.Findings {
		fmt.Fprintf(output, "%s %s:%d: %s\n", dashboard.SafeText(finding.Priority), dashboard.SafeText(finding.Path), finding.Line, dashboard.SafeText(finding.Scenario))
		fmt.Fprintln(output, "Recommendation:", dashboard.SafeText(finding.Recommendation))
	}
	for _, limitation := range j.Outcome.Limitations {
		fmt.Fprintln(output, "Review limitation:", dashboard.SafeText(limitation))
	}
	if *dry {
		fmt.Fprintln(output, "Preview only; nothing recorded.")
		return nil
	}
	if err := workrun.GrantReviewAcceptance(action.View.Directory, j.UpdatedAt, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(output, "Override recorded. Resume with: sdlc resume --run %s\n", action.View.ID)
	return nil
}
