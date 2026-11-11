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
	"reflect"
	"strings"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func featureRuntimeMigrationCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("runtime migrate-feature", flag.ContinueOnError)
	flags.SetOutput(out)
	reference := flags.String("reference", "", "saved work reference")
	targetFlag := flags.String("to", "", "target runtime name; local selects the local runtime")
	dry := flags.Bool("dry-run", false, "preview without changing saved checkpoints")
	rollback := flags.Bool("rollback", false, "restore a pending transaction's original checkpoint bytes")
	jsonOutput := flags.Bool("json", false, "print a structured preview")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *reference == "" || *targetFlag == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: sdlc runtime migrate-feature --reference REF --to NAME [--dry-run] [--rollback] [--json]")
	}
	name := *targetFlag
	if name == "local" {
		name = ""
	}
	if err := runtimeimage.ValidateName(name); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var root string
	if *rollback {
		root, err = seriesGit(ctx, cwd, "rev-parse", "--show-toplevel")
		if err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
		if err != nil {
			return err
		}
	} else {
		work, inspectErr := project.InspectWork(ctx, cwd, *reference)
		if inspectErr != nil {
			return inspectErr
		}
		root = work.Root
	}
	var target runtimeimage.State
	var manager runtimeimage.Manager
	if *rollback {
		target, err = workseries.PendingFeatureRuntimeTarget(root, *reference)
		if err != nil {
			return err
		}
		if target.Name != name {
			return fmt.Errorf("rollback target differs from the pending migration")
		}
	} else {
		manager, err = runtimeimage.NewNamed(io.Discard, io.Discard, name)
		if err != nil {
			return err
		}
		if !*dry {
			lease, leaseErr := leaseRuntimeRun(ctx, manager.Directory, out)
			if leaseErr != nil {
				return leaseErr
			}
			defer lease.Close()
		}
		target, err = manager.Status(ctx)
		if err != nil {
			return err
		}
		if err := checkSeriesRuntime(ctx, manager, target.ImageID); err != nil {
			return err
		}
	}
	resolvedSDK := map[string]string{}
	var verify workseries.RuntimeMigrationVerify
	if !*rollback {
		verify = func(ctx context.Context, state workseries.State, journals []workrun.Journal) error {
			current, err := workseries.Discover(ctx, state.Plan.Root, state.Plan.Reference, state.Plan.Base)
			if err != nil || !reflect.DeepEqual(current, state.Plan) {
				return fmt.Errorf("feature definition changed; restore the frozen definition before migration")
			}
			settings, err := loadSeriesSettings(state.Settings)
			if err != nil {
				return err
			}
			driver := &seriesDriver{runtime: manager, plan: state.Plan, settings: settings, output: io.Discard}
			if err := driver.validateSettings(); err != nil {
				return err
			}
			if err := driver.checkInputs(); err != nil {
				return err
			}
			for _, j := range journals {
				ticket := filepath.Base(j.Plan.Ticket)
				runDir, err := workrun.RunDirectory(state.Plan.Root, state.Plan.Reference, ticket, j.ID, false)
				if err != nil {
					return err
				}
				if j.Plan.CheckSelection == "runtime" {
					return fmt.Errorf("ticket %s uses runtime-discovered checks; migrate only after freezing its check selection", ticket)
				}
				if err := workrun.VerifyRuntimeMigrationInputs(runDir, j); err != nil {
					return err
				}
				sdk, err := verifyFeatureRuntimeSDK(ctx, manager, target.ImageID, j.Workspace)
				if err != nil {
					return fmt.Errorf("ticket %s runtime SDK check: %w", ticket, err)
				}
				resolvedSDK[j.ID] = sdk
				// Preserve the exact route already recorded by the started ticket. The
				// feature-wide default remains selected for tickets that have not started.
				copySettings := settings
				copySettings.TicketHeadroom = map[string]headroom.Config{}
				for key, value := range settings.TicketHeadroom {
					copySettings.TicketHeadroom[key] = value
				}
				if recorded, exists := copySettings.TicketHeadroom[ticket]; exists && recorded != j.Plan.Headroom {
					return fmt.Errorf("ticket %s provider route differs from its recorded exception", ticket)
				}
				copySettings.TicketHeadroom[ticket] = j.Plan.Headroom
				runDriver := *driver
				runDriver.settings = copySettings
				if err := runDriver.validateRunJournal(ticket, runDir, j); err != nil {
					return err
				}
				repository := workrun.DockerRepository{Runtime: manager, ImageID: target.ImageID}
				revision, err := repository.Inspect(ctx, j.Workspace)
				if err != nil || !revision.Clean || revision.Head != j.Evidence.Head || revision.Tree != j.Evidence.Tree {
					return fmt.Errorf("ticket %s candidate differs from its saved checked revision", ticket)
				}
				remoteSettings := copySettings
				remoteSettings.ImageID = target.ImageID
				remoteDriver := *driver
				remoteDriver.settings = remoteSettings
				publisher := remoteDriver.publisher(j.Plan)
				remote, err := publisher.Observe(ctx, j.Plan, j.Publication)
				if err != nil {
					return fmt.Errorf("ticket %s PR inspection failed: %w", ticket, err)
				}
				if remote.State != "OPEN" || remote.Base != j.Plan.Base || remote.BaseSHA != j.Publication.BaseSHA || remote.HeadSHA != j.Publication.HeadSHA {
					return fmt.Errorf("ticket %s published PR no longer matches its saved base and head", ticket)
				}
				branch, err := publisher.Branch(ctx, j.Plan, j.Plan.Branch)
				if err != nil {
					return fmt.Errorf("ticket %s branch inspection failed: %w", ticket, err)
				}
				if branch.Branch != j.Plan.Branch || branch.SHA != j.Publication.HeadSHA {
					return fmt.Errorf("ticket %s published branch no longer matches its saved head", ticket)
				}
			}
			return nil
		}
	}
	preview, err := workseries.MigrateFeatureRuntime(ctx, root, *reference, target, *dry, *rollback, verify)
	if err != nil {
		return err
	}
	for i := range preview.Runs {
		preview.Runs[i].ResolvedSDK = resolvedSDK[preview.Runs[i].RunID]
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(preview)
	}
	targetName := preview.TargetName
	if targetName == "" {
		targetName = "local"
	}
	action := "Migration completed"
	if preview.DryRun {
		action = "Migration preview"
	}
	if preview.Rollback {
		action = "Rollback completed"
		if preview.DryRun {
			action = "Rollback preview"
		}
	}
	fmt.Fprintf(out, "%s: feature %s -> %s (%s).\n", action, preview.Reference, targetName, preview.TargetImage)
	for _, run := range preview.Runs {
		fmt.Fprintf(out, "  %s | run %s | runtime %s -> %s | provider route %s retained | PR #%d base %s head %s unchanged.\n", run.Ticket, run.RunID, run.PreviousRuntime, targetName, run.ProviderRoute, run.Publication.Number, run.Publication.BaseSHA, run.Publication.HeadSHA)
		if run.ResolvedSDK != "" {
			fmt.Fprintf(out, "    Target resolves the saved SDK requirement to %s.\n", run.ResolvedSDK)
		}
		if !preview.Rollback {
			fmt.Fprintf(out, "    Resume stage: %s; previous checks and review are not current. Isolated checks must pass before the saved CI hold resumes. No PR is published by migration or recheck.\n", run.ResumeState)
		}
	}
	if preview.Recovery {
		fmt.Fprintln(out, "A pending transaction was recovered using its recorded checkpoints.")
	}
	return nil
}

// verifyFeatureRuntimeSDK runs no repository scripts and mounts the saved
// workspace read-only in a network-disabled disposable container.
func verifyFeatureRuntimeSDK(ctx context.Context, manager runtimeimage.Manager, image, workspace string) (string, error) {
	path := filepath.Join(workspace, "global.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return "", fmt.Errorf("SDK configuration is unavailable or unsafe")
	}
	if manager.Docker == nil {
		return "", fmt.Errorf("target runtime Docker is unavailable")
	}
	if strings.Contains(workspace, ",") {
		return "", fmt.Errorf("workspace path cannot be represented safely in a Docker mount")
	}
	data, err := manager.Docker.Output(ctx, "run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "32", "--memory", "256m", "--log-driver", "none", "--mount", "type=bind,src="+workspace+",dst=/workspace,readonly", "--workdir", "/workspace", "--entrypoint", "dotnet", image, "--version")
	if err != nil {
		return "", fmt.Errorf("target runtime cannot resolve the saved SDK: %w", err)
	}
	version := strings.TrimSpace(string(data))
	if version == "" || len(version) > 128 || strings.ContainsAny(version, "\r\n\t ") {
		return "", fmt.Errorf("target runtime returned an invalid SDK version")
	}
	return version, nil
}
