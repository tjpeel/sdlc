package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/savedwork"
	"github.com/tjpeel/sdlc/internal/textview"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const storagePurgeUsage = "Usage: sdlc storage purge (--run RUN_ID | --all) [--yes | --dry-run]\nPermanently deletes stopped saved runs in the current repository, including runs absent from the dashboard.\nAlso abandons affected feature checkpoints; tickets and specifications remain.\nWithout --yes, shows a preview. Active controllers prevent removal.\n--all selects all saved runs and feature checkpoints in this repository, excluding .archive.\nUse dashboard remove to hide entries while retaining saved work, or work archive to retain a whole reference."

func currentWorkRoot(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return checkoutRoot(ctx, cwd)
}

func storagePurgeCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("storage purge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "saved run ID or unique prefix")
	all := flags.Bool("all", false, "all saved runs and feature checkpoints in the current repository")
	yes := flags.Bool("yes", false, "confirm permanent removal")
	dryRun := flags.Bool("dry-run", false, "preview without changing files")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, storagePurgeUsage)
		return err
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 || ((*id != "") == *all) || (*yes && *dryRun) {
		return fmt.Errorf("purge requires exactly --run RUN_ID or --all, and at most one of --yes or --dry-run")
	}
	if *id != "" {
		if err := workrun.ValidateRunSelector(*id); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := currentWorkRoot(ctx)
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	preview := !*yes
	result, err := savedwork.Remove(ctx, runtime.Directory, root, savedwork.RemoveOptions{Run: *id, All: *all, DryRun: preview})
	if err != nil {
		return err
	}
	var b strings.Builder
	heading := "Saved work removed"
	if preview {
		heading = "Saved work removal preview"
	}
	fmt.Fprintln(&b, textview.Heading(heading))
	fmt.Fprintf(&b, "Project: %q\n", dashboard.SafeText(root))
	for _, run := range result.Runs {
		fmt.Fprintf(&b, "Run: %s · %s / %s\n  Directory: %q\n", run.ID, dashboard.SafeText(run.Reference), dashboard.SafeText(run.Ticket), dashboard.SafeText(run.Directory))
	}
	for _, series := range result.Series {
		fmt.Fprintf(&b, "Feature checkpoint abandoned: %q\n", dashboard.SafeText(series))
	}
	fmt.Fprintf(&b, "%d saved runs; %d feature checkpoints. Tickets, specifications, archives and account settings retained.\n", len(result.Runs), len(result.Series))
	if preview {
		fmt.Fprintln(&b, "Nothing removed. Repeat this command with --yes to delete the listed saved work permanently.")
	} else {
		fmt.Fprintln(&b, "Removed work cannot be resumed. Other saved references can still prevent runtime replacement.")
	}
	_, err = io.WriteString(output, b.String())
	return err
}
