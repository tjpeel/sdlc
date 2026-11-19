package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func providerTakeoverCommand(ctx context.Context, args []string, output io.Writer) error {
	f := flag.NewFlagSet("provider-takeover", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	id := f.String("run", "", "saved run ID")
	provider := f.String("provider", "", "new implementation provider")
	model := f.String("model", "", "new implementation model")
	effort := f.String("effort", "", "new implementation effort")
	reviewer := f.String("review-provider", "", "explicitly selected saved reviewer provider")
	reasonPath := f.String("reason-file", "", "operator decision file")
	dry := f.Bool("dry-run", false, "preview without saving")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc provider-takeover --run RUN_ID --provider PROVIDER --review-provider PROVIDER --reason-file FILE [--model MODEL] [--effort EFFORT] [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *provider == "" || *reviewer == "" || *reasonPath == "" || f.NArg() != 0 {
		return fmt.Errorf("provider-takeover requires run, provider, review-provider and reason-file")
	}
	data, err := readHumanAnswer(*reasonPath)
	if err != nil {
		return err
	}
	action, err := resolveRunAction(ctx, *id, false)
	if err != nil {
		return err
	}
	j := action.View.Journal
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	defaults, err := workrun.LoadModels(runtime.Directory)
	if err != nil {
		return err
	}
	roles, err := workrun.ResolveModels(defaults, *provider, *model, *effort, "", "")
	if err != nil {
		return err
	}
	selected := roles.Implementation
	if err := workrun.ValidateProviderTakeover(*j, selected, *reviewer, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(output, "Provider takeover for %s; PR %s, head %s\n", j.ID, j.Publication.URL, j.Publication.HeadSHA)
	fmt.Fprintf(output, "Implementation: %s/%s -> %s/%s (%s). Independent review: %s/%s in a separate new session.\n", j.Plan.Roles.Implementation.Provider, j.Plan.Roles.Implementation.Name, selected.Provider, selected.Name, selected.Effort, j.Plan.Roles.Review.Provider, j.Plan.Roles.Review.Name)
	if selected.Provider == j.Plan.Roles.Review.Provider {
		fmt.Fprintln(output, "Same-provider review was explicitly selected; independence is provided by a separate session and publication snapshot.")
	}
	fmt.Fprintln(output, "Operator reason:", string(data))
	fmt.Fprintln(output, "Prior session, feedback and review history are retained. Resume must recheck source, inputs, publication and CI; it requires fresh checks and independent review.")
	if *dry {
		fmt.Fprintln(output, "Preview only; nothing recorded.")
		return nil
	}
	if err := workrun.GrantProviderTakeover(action.View.Directory, j.UpdatedAt, selected, *reviewer, string(data)); err != nil {
		return err
	}
	fmt.Fprintf(output, "Takeover recorded. Resume with: sdlc resume --run %s\n", j.ID)
	return nil
}
