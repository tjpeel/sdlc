package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

type dependencyBuildOptions struct {
	Name, Source, SkillsSource, AgentsSource string
	DryRun, Force                            bool
}

func runtimeBuildDependencies(ctx context.Context, manager runtimeUpdateManager, checker runtimeupdates.Checker, stateDirectory string, options dependencyBuildOptions, output io.Writer) error {
	if options.Name == "" {
		return fmt.Errorf("runtime build --dependencies requires --name")
	}
	source := options.Source
	if source == "" {
		state, err := manager.Status(ctx)
		if err != nil {
			return fmt.Errorf("specify --source for the first named dependency build")
		}
		source = state.Source
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	recipe, err := readRuntimeRecipe(source)
	if err != nil {
		return err
	}
	catalogues := map[string]string{}
	for _, item := range []struct{ kind, source, key string }{{"skills", options.SkillsSource, "SKILLS_REVISION"}, {"agents", options.AgentsSource, "AGENTS_REVISION"}} {
		if item.source == "" {
			continue
		}
		revision, err := runtimeimage.CatalogueRevision(ctx, item.source, item.kind)
		if err != nil {
			return fmt.Errorf("local %s: %w", item.kind, err)
		}
		catalogues[item.key] = revision
	}
	if _, err := fmt.Fprintln(output, "Resolving supported dependency releases; retaining selected local catalogue commits..."); err != nil {
		return err
	}
	checkContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	plan, err := checker.PlanBuild(checkContext, recipe, catalogues)
	cancel()
	if err != nil {
		return fmt.Errorf("dependency build planning failed; runtimes unchanged: %w", err)
	}
	if err := plan.PrintBuild(output); err != nil {
		return err
	}
	if options.DryRun {
		_, err := fmt.Fprintln(output, "Preview complete; no images pulled, runtimes selected or pins saved.")
		return err
	}
	revision, err := runtimeSourceRevision(source, "")
	if err != nil {
		return err
	}
	candidate, err := manager.BuildWithOptions(ctx, source, runtimeimage.BuildOptions{
		SkillsSource: options.SkillsSource, AgentsSource: options.AgentsSource,
		ExpectedSkillsRevision: catalogues["SKILLS_REVISION"], ExpectedAgentsRevision: catalogues["AGENTS_REVISION"],
		SourceRevision: revision, Pins: &plan.Pins, Refresh: true, KeepPreviousImage: options.Force,
		ValidatePrevious: runtimeBuildSavedWorkGuard(stateDirectory, source, options.Force, output),
		ValidateCandidate: func(ctx context.Context, candidate runtimeimage.State) error {
			if err := validateRuntimePacks(candidate, plan.ExpectedRuntimes); err != nil {
				return err
			}
			updates, err := manager.PackageUpdates(ctx, candidate)
			if err != nil {
				return fmt.Errorf("candidate Debian package verification failed: %w", err)
			}
			if candidate.Inventory == nil || len(updates) != len(candidate.Inventory.Packages) {
				return fmt.Errorf("candidate Debian package verification is incomplete")
			}
			for _, dependency := range updates {
				if dependency.Candidate == "" || dependency.Update {
					return fmt.Errorf("candidate Debian packages unavailable or still need updates")
				}
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	return printRuntime(output, candidate)
}
