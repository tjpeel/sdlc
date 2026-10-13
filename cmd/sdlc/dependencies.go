package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/workseries"
)

const dependenciesUsage = "Usage: sdlc dependencies --reference REFERENCE --ticket NUMBERED_FILE --depends-on PARENT_FILE [--depends-on PARENT_FILE] [--dry-run] [--json]\nUse --depends-on '' to clear the dependency set. Repeat the exact command to recover an interrupted amendment."

type dependencySelections []string

func (s *dependencySelections) String() string         { return fmt.Sprint([]string(*s)) }
func (s *dependencySelections) Set(value string) error { *s = append(*s, value); return nil }

func dependenciesCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("dependencies", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "saved feature reference")
	ticket := flags.String("ticket", "", "unstarted ticket filename")
	root := flags.String("run-root", "", "recorded project root")
	dry := flags.Bool("dry-run", false, "preview without writing")
	structured := flags.Bool("json", false, "JSON result")
	var dependencies dependencySelections
	flags.Var(&dependencies, "depends-on", "complete dependency set (repeatable; empty clears)")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dependenciesUsage)
		return err
	} else if err != nil {
		return err
	}
	if *reference == "" || *ticket == "" || flags.NArg() != 0 || len(dependencies) == 0 {
		return errors.New(dependenciesUsage)
	}
	selected := []string{}
	for _, value := range dependencies {
		if value == "" {
			if len(dependencies) != 1 {
				return errors.New("empty --depends-on cannot be combined with parent filenames")
			}
			continue
		}
		selected = append(selected, value)
	}
	if *root == "" {
		current, err := os.Getwd()
		if err != nil {
			return err
		}
		*root = current
	}
	work, err := project.InspectWork(ctx, *root, *reference)
	if err != nil {
		return err
	}
	preview, err := workseries.AmendDependencies(ctx, work.Root, *reference, *ticket, selected, *dry, func(state workseries.State) error {
		settings, err := loadSeriesSettings(state.Settings)
		if err != nil {
			return err
		}
		if err := amendmentDefinitionIsNotInput(state.Plan, settings); err != nil {
			return err
		}
		driver := &seriesDriver{plan: state.Plan, settings: settings}
		if err := driver.validateSettings(); err != nil {
			return err
		}
		return driver.checkInputs()
	})
	if err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(output).Encode(preview)
	}
	verb := "Saved"
	if preview.DryRun {
		verb = "Preview"
	} else if !preview.Changed {
		verb = "Unchanged"
	}
	_, err = fmt.Fprintf(output, "%s dependencies for %s: %v -> %v\nFeature definition: %s -> %s\n", verb, preview.Ticket, preview.From, preview.To, preview.Before, preview.After)
	return err
}

func amendmentDefinitionIsNotInput(plan workseries.Plan, settings seriesSettings) error {
	relative := filepath.Join(".sdlc", "work", plan.Reference, "plan.json")
	definition, definitionErr := os.Lstat(filepath.Join(plan.Root, relative))
	for path := range settings.InputHashes {
		selected := filepath.Join(plan.Root, filepath.FromSlash(path))
		if filepath.Clean(selected) == filepath.Join(plan.Root, relative) {
			return errors.New("dependency definition is a frozen requirement or check input; amendment refused")
		}
		if definitionErr == nil {
			if info, err := os.Lstat(selected); err == nil && os.SameFile(definition, info) {
				return errors.New("dependency definition aliases a frozen input; amendment refused")
			}
		}
	}
	return nil
}
