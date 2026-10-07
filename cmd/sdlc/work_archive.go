package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/savedwork"
	"github.com/tjpeel/sdlc/internal/textview"
)

const workArchiveUsage = "Usage: sdlc work archive --reference REFERENCE [--dry-run]\nMoves the entire stopped work reference into .sdlc/work/.archive under a unique name.\nPreserves every file and removes its dashboard registrations. Active controllers prevent archiving.\nThe original reference name becomes available for fresh scoping.\nArchived work is excluded from active references and runtime protection; replacing the runtime can prevent later resumption."

func workArchiveCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("work archive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "work reference to archive")
	dryRun := flags.Bool("dry-run", false, "preview without moving files")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, workArchiveUsage)
		return err
	} else if err != nil {
		return err
	}
	if *reference == "" || flags.NArg() != 0 {
		return fmt.Errorf("archive requires --reference REFERENCE, without positional arguments")
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
	result, err := savedwork.Archive(ctx, runtime.Directory, root, *reference, *dryRun)
	if err != nil {
		return err
	}
	var b strings.Builder
	heading := "Work reference archived"
	if *dryRun {
		heading = "Work reference archive preview"
	}
	fmt.Fprintln(&b, textview.Heading(heading))
	fmt.Fprintf(&b, "Reference: %q\nArchive: %q\n%d saved runs; complete reference contents retained.\n", dashboard.SafeText(*reference), dashboard.SafeText(result.Destination), len(result.Runs))
	if *dryRun {
		fmt.Fprintln(&b, "Nothing moved. Repeat without --dry-run to archive this reference.")
	} else {
		fmt.Fprintln(&b, "The reference name is free for fresh scoping. Archived runs are no longer dashboard entries or runtime update blockers.")
		fmt.Fprintln(&b, "The original run paths cannot resume; keep the archive as evidence. A runtime update can replace its required image.")
	}
	_, err = io.WriteString(output, b.String())
	return err
}
