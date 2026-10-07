package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
)

const workUsage = "Usage: sdlc work --reference REFERENCE [--json] | work --references [--json]\n       sdlc work archive --reference REFERENCE [--dry-run]\nRun from a project repository to list local references and tickets, or archive a stopped reference."

func referencesCommand(ctx context.Context, args []string, output io.Writer) error {
	return browseWorkCommand(ctx, "references", args, output)
}

func ticketsCommand(ctx context.Context, args []string, output io.Writer) error {
	return browseWorkCommand(ctx, "tickets", args, output)
}

func browseWorkCommand(ctx context.Context, name string, args []string, output io.Writer) error {
	usage := "Usage: sdlc " + name
	if name == "tickets" {
		usage += " REFERENCE"
	}
	usage += " [--json]"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	structured := flags.Bool("json", false, "structured metadata")
	// Accept --json on either side of the reference, without splitting its name.
	var options, positional []string
	literal := false
	for _, arg := range args {
		if !literal && arg == "--" {
			literal = true
		} else if !literal && strings.HasPrefix(arg, "-") {
			options = append(options, arg)
		} else {
			positional = append(positional, arg)
		}
	}
	if err := flags.Parse(options); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, usage)
			return err
		}
		return fmt.Errorf("invalid %s arguments; %s", name, usage)
	}
	if flags.NArg() != 0 || (name == "references" && len(positional) != 0) || (name == "tickets" && (len(positional) != 1 || positional[0] == "")) {
		return fmt.Errorf("%s", usage)
	}
	workArgs := []string{"--references"}
	if name == "tickets" {
		workArgs = []string{"--reference", positional[0]}
	}
	if *structured {
		workArgs = append(workArgs, "--json")
	}
	return workCommand(ctx, workArgs, output)
}

func workCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "archive" {
		return workArchiveCommand(ctx, args[1:], output)
	}
	flags := flag.NewFlagSet("work", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "work folder under .sdlc/work")
	references := flags.Bool("references", false, "list local work references without reading ticket bodies")
	jsonOutput := flags.Bool("json", false, "structured metadata")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, workUsage)
			return err
		}
		return fmt.Errorf("invalid work arguments; run sdlc work --help")
	}
	if flags.NArg() != 0 || ((*reference != "") == *references) {
		return fmt.Errorf("work requires either --reference REFERENCE or --references, without positional arguments")
	}
	directory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot locate the current directory")
	}
	if *references {
		result, err := project.References(ctx, directory)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(output).Encode(result)
		}
		fmt.Fprintf(output, "Project: %q\n", result.Root)
		for _, ref := range result.References {
			fmt.Fprintf(output, "%q: %d tickets", ref.Name, len(ref.Tickets))
			if ref.Error != "" {
				fmt.Fprintf(output, " · unavailable: %s", ref.Error)
			}
			fmt.Fprintln(output)
		}
		return nil
	}
	result, err := project.InspectWork(ctx, directory, *reference)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(struct {
			Version   int      `json:"version"`
			Root      string   `json:"root"`
			Reference string   `json:"reference"`
			Tickets   []string `json:"tickets"`
		}{1, result.Root, result.Reference, result.Tickets})
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
