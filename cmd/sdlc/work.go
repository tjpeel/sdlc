package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tjpeel/sdlc/internal/project"
)

const workUsage = "Usage: sdlc work --reference REFERENCE\nRun from a project repository to list local tickets in numeric filename order."

func workCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("work", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "work folder under .sdlc/work")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, workUsage)
			return err
		}
		return fmt.Errorf("invalid work arguments; run sdlc work --help")
	}
	if *reference == "" || flags.NArg() != 0 {
		return fmt.Errorf("work requires --reference REFERENCE and accepts no positional arguments")
	}
	directory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot locate the current directory")
	}
	result, err := project.InspectWork(ctx, directory, *reference)
	if err != nil {
		return err
	}
	var summary bytes.Buffer
	fmt.Fprintf(&summary, "Project: %q\nWork reference: %q\n", result.Root, result.Reference)
	fmt.Fprintf(&summary, "Ticket order (%d):\n", len(result.Tickets))
	for index, ticket := range result.Tickets {
		fmt.Fprintf(&summary, "  %d. %q\n", index+1, ticket)
	}
	fmt.Fprintln(&summary, "Ticket discovery complete. Ticket content and dependencies have not been checked.")
	fmt.Fprintln(&summary, "Select one ticket with sdlc run --reference REFERENCE --ticket NUMBERED_FILE.")
	_, err = output.Write(summary.Bytes())
	return err
}
