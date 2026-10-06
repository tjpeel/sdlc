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
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

type inputSelections []string

func (s *inputSelections) String() string { return fmt.Sprint([]string(*s)) }
func (s *inputSelections) Set(value string) error {
	if value == "" {
		return errors.New("--add requires a relative file path")
	}
	*s = append(*s, value)
	return nil
}

func inputsCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("inputs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "recorded run ID or unique prefix")
	dry := flags.Bool("dry-run", false, "offline preview")
	structured := flags.Bool("json", false, "JSON output")
	var selected inputSelections
	flags.Var(&selected, "add", "supplementary relative input (repeatable)")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc inputs --run RUN_ID [--add RELATIVE_PATH] [--dry-run] [--json]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 {
		return errors.New("inputs requires --run RUN_ID")
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	views, err := runstatus.New(runtime.Directory).List(time.Now().UTC())
	if err != nil {
		return err
	}
	view, err := dashboard.Select(views, *id)
	if err != nil {
		return err
	}
	if !view.Available || view.Preparation || view.Journal == nil {
		return errors.New("run checkpoint is unavailable or still in preparation")
	}
	directory, err := workrun.RunDirectory(view.Root, view.Reference, filepath.Base(view.Ticket), view.ID, false)
	if err != nil {
		return err
	}
	if directory != view.Directory {
		return errors.New("recorded run directory does not match its identity")
	}
	if len(selected) > 0 {
		release, err := lockInputRun(ctx, view, !*dry)
		if err != nil {
			return err
		}
		defer release()
	}
	journal, err := workrun.Load(directory)
	if err != nil {
		return err
	}
	if journal.ID != view.ID || journal.Plan.Root != view.Root || journal.Plan.Reference != view.Reference || journal.Plan.Ticket != view.Ticket || !journal.UpdatedAt.Equal(view.UpdatedAt) {
		return errors.New("run checkpoint changed; select the run again")
	}
	if len(selected) > 0 {
		if err := validateSupplementSeries(ctx, view, journal); err != nil {
			return err
		}
	}
	var additions []workrun.Input
	if len(selected) > 0 {
		if view.Live || !(view.Stopped || view.Stale) {
			return errors.New("run controller is still active")
		}
		expanded := []string(selected)
		if journal.PendingInputs == nil {
			expanded, err = project.ResolveRequirements(ctx, journal.Plan.Root, journal.Plan.Reference, expanded)
			if err != nil {
				return err
			}
		}
		additions, err = workrun.AttachInputs(ctx, directory, &journal, expanded, *dry)
		if err != nil {
			return err
		}
	}
	// Discovery during listing is advisory only; it never updates the manifest.
	var missing []string
	var requirementIssue string
	if len(selected) == 0 {
		closure, e := project.ResolveRequirements(ctx, journal.Plan.Root, journal.Plan.Reference, []string{journal.Plan.Ticket})
		if e != nil {
			requirementIssue = e.Error()
		} else {
			seen := map[string]bool{}
			for _, in := range journal.Plan.Inputs {
				seen[in.Path] = true
			}
			for _, path := range closure {
				if !seen[path] {
					missing = append(missing, path)
				}
			}
		}
	}
	if *structured {
		return json.NewEncoder(output).Encode(struct {
			RunID      string                        `json:"run_id"`
			Offline    bool                          `json:"offline"`
			DryRun     bool                          `json:"dry_run"`
			Inputs     []workrun.Input               `json:"inputs"`
			Additions  []workrun.Input               `json:"additions,omitempty"`
			Recoveries []workrun.InputRecovery       `json:"input_recoveries,omitempty"`
			Pending    *workrun.PendingInputRecovery `json:"pending_inputs,omitempty"`
			Missing    []string                      `json:"uncaptured_requirements,omitempty"`
			Issue      string                        `json:"requirement_issue,omitempty"`
		}{journal.ID, true, *dry, journal.Plan.Inputs, additions, journal.InputRecoveries, journal.PendingInputs, missing, requirementIssue})
	}
	fmt.Fprintf(output, "Run %s | project %s\nRecorded inputs:\n", journal.ID, dashboard.SafeText(journal.Plan.Root))
	for _, in := range journal.Plan.Inputs {
		fmt.Fprintf(output, "  %s  %s\n", in.SHA256, dashboard.SafeText(in.Path))
	}
	for _, r := range journal.InputRecoveries {
		fmt.Fprintf(output, "Recovery %s (previous checkpoint %s):\n", r.AddedAt.Format(time.RFC3339Nano), r.PreviousCheckpoint.Format(time.RFC3339Nano))
		for _, in := range r.Inputs {
			fmt.Fprintf(output, "  %s  %s\n", in.SHA256, dashboard.SafeText(in.Path))
		}
	}
	if journal.PendingInputs != nil {
		fmt.Fprintln(output, "Unfinished recovery; staged inputs:")
		var completion strings.Builder
		fmt.Fprintf(&completion, "sdlc inputs --run %s", journal.ID)
		for _, in := range journal.PendingInputs.Inputs {
			fmt.Fprintf(output, "  %s  %s\n", in.SHA256, dashboard.SafeText(in.Path))
			fmt.Fprintf(&completion, " --add %s", quoteInputPath(in.Path))
		}
		fmt.Fprintf(output, "Complete the saved recovery:\n  %s\n", completion.String())
	}

	if *dry {
		for _, in := range additions {
			fmt.Fprintf(output, "Would add: %s  %s\n", in.SHA256, dashboard.SafeText(in.Path))
		}
	}
	for _, path := range missing {
		fmt.Fprintf(output, "Linked requirement not captured: %s\n  sdlc inputs --run %s --add %s\n", dashboard.SafeText(path), journal.ID, quoteInputPath(path))
	}
	if requirementIssue != "" {
		fmt.Fprintf(output, "Requirement issue: %s\n", dashboard.SafeText(requirementIssue))
	}
	if len(selected) > 0 {
		fmt.Fprintf(output, "Run remains waiting for its recorded answer. Next: sdlc answer --run %s\n", journal.ID)
	}
	return nil
}

// The feature lock precedes the run lock. Existing feature state is read-only:
// supplements belong to this run and cannot propagate to another ticket.
func lockInputRun(ctx context.Context, view runstatus.View, create bool) (func(), error) {
	var locks []*os.File
	release := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Close()
		}
	}
	seriesPath := filepath.Join(view.Root, ".sdlc", "work", view.Reference, "series")
	if _, err := os.Lstat(seriesPath); err == nil {
		directory, err := workseries.Directory(view.Root, view.Reference, false)
		if err != nil {
			return nil, err
		}
		state, err := workseries.Load(directory)
		if err != nil {
			return nil, err
		}
		member := false
		for ticket, result := range state.Results {
			if result.RunID == view.ID {
				if ticket != filepath.Base(view.Ticket) || (result.Directory != "" && result.Directory != view.Directory) {
					return nil, errors.New("feature membership conflicts with selected run")
				}
				member = true
			}
		}
		{ // Hold existing feature ownership even for an unadopted run: a feature controller may adopt it concurrently.
			_ = member
			path := filepath.Join(directory, "series.lock")
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return nil, errors.New("feature lock is unsafe")
			}
			lock, err := filelock.Acquire(path)
			if err != nil {
				return nil, fmt.Errorf("cannot acquire feature ownership: %w", err)
			}
			locks = append(locks, lock)
			opened, err := lock.Stat()
			current, e := os.Lstat(path)
			if err != nil || e != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) {
				release()
				return nil, errors.New("feature lock changed while opening")
			}
			frozen, e := workseries.Load(directory)
			if e != nil {
				release()
				return nil, e
			}
			if !frozen.UpdatedAt.Equal(state.UpdatedAt) {
				release()
				return nil, errors.New("feature checkpoint changed; select the run again")
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	lock, err := probeRunController(view.Directory)
	if err != nil {
		release()
		return nil, err
	}
	if lock == nil && create {
		lock, err = filelock.Acquire(filepath.Join(view.Directory, "run.lock"))
		if err != nil {
			release()
			return nil, fmt.Errorf("cannot acquire run ownership: %w", err)
		}
	}
	if lock != nil {
		locks = append(locks, lock)
	}
	return release, nil
}

// validateSeriesRunInputs keeps the feature's frozen originals exact while
// permitting only the run's own audited supplements.
func validateSeriesRunInputs(ctx context.Context, journal workrun.Journal, originalInputs []string, frozenHashes map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if journal.PendingInputs != nil {
		return errors.New("run input recovery is unfinished")
	}
	if len(journal.Plan.Inputs) < len(originalInputs) {
		return errors.New("run removed a frozen feature input")
	}
	for i, path := range originalInputs {
		if journal.Plan.Inputs[i].Path != path {
			return errors.New("run changed the frozen original input order")
		}
	}
	originals := map[string]bool{}
	for _, path := range originalInputs {
		if originals[path] || frozenHashes[path] == "" {
			return errors.New("invalid frozen feature inputs")
		}
		originals[path] = true
	}
	audited := map[string]string{}
	for _, recovery := range journal.InputRecoveries {
		if recovery.PreviousCheckpoint.IsZero() || recovery.AddedAt.IsZero() {
			return errors.New("invalid input recovery audit")
		}
		for _, in := range recovery.Inputs {
			if originals[in.Path] || audited[in.Path] != "" {
				return errors.New("supplementary input audit replaces or duplicates a frozen input")
			}
			audited[in.Path] = in.SHA256
		}
	}
	seen := map[string]bool{}
	for _, in := range journal.Plan.Inputs {
		if seen[in.Path] {
			return errors.New("duplicate run input")
		}
		seen[in.Path] = true
		expected := audited[in.Path]
		if originals[in.Path] {
			expected = frozenHashes[in.Path]
		}
		if expected == "" || expected != in.SHA256 {
			return errors.New("run inputs differ from frozen inputs without a matching recovery audit")
		}
		// Use Capture's existing manifest verifier through a bounded local copy hash.
		if err := workrun.VerifyCapturedInputs(journal.Plan.Root, []workrun.Input{in}); err != nil {
			return err
		}
		if err := workrun.VerifyCapturedInputs(journal.Workspace, []workrun.Input{in}); err != nil {
			return err
		}
	}
	for path := range originals {
		if !seen[path] {
			return errors.New("run removed a frozen feature input")
		}
	}
	for path := range audited {
		if !seen[path] {
			return errors.New("run omitted an audited supplement")
		}
	}
	return nil
}

func quoteInputPath(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'" }

func validateSupplementSeries(ctx context.Context, view runstatus.View, j workrun.Journal) error {
	path := filepath.Join(view.Root, ".sdlc", "work", view.Reference, "series")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	state, err := workseries.Load(path)
	if err != nil {
		return err
	}
	result, member := state.Results[filepath.Base(view.Ticket)]
	if !member || result.RunID != view.ID {
		return nil
	}
	settings, err := loadSeriesSettings(state.Settings)
	if err != nil {
		return err
	}
	driver := seriesDriver{plan: state.Plan, settings: settings}
	if err := driver.validateSettings(); err != nil {
		return err
	}
	if err := driver.checkInputs(); err != nil {
		return err
	}
	roles, exists := settings.Roles[filepath.Base(view.Ticket)]
	if !exists || j.ImageID != settings.ImageID || !reflect.DeepEqual(j.Plan.Roles, roles) || !reflect.DeepEqual(j.Plan.PublicationIdentity, settings.Identity) || j.Plan.Repository != settings.Repository || j.Plan.GitHubProfile != settings.GitHubProfile || j.Plan.SigningProfile != settings.SigningProfile || j.Plan.SigningImage != settings.SigningImage || j.Plan.DaemonImage != settings.DaemonImage || j.Instructions != settings.Instructions || !reflect.DeepEqual(j.Plan.Checks, settings.Config.Checks) || j.Plan.DockerTests != settings.DockerTests || !reflect.DeepEqual(j.Plan.Headroom, settings.Headroom) {
		return errors.New("feature run differs from frozen settings")
	}
	if len(j.Plan.CheckInputs) != len(settings.Config.InputFiles) {
		return errors.New("feature run check inputs differ from frozen settings")
	}
	for i, path := range settings.Config.InputFiles {
		if j.Plan.CheckInputs[i].Path != path || j.Plan.CheckInputs[i].SHA256 != settings.InputHashes[path] {
			return errors.New("feature run check inputs differ from frozen settings")
		}
	}
	// The unfinished transaction is validated by workrun.AttachInputs. Originals
	// and previous successful supplements must still match before recovery writes.
	j.PendingInputs = nil
	return validateSeriesRunInputs(ctx, j, settings.ticketInputs(state.Plan.Reference, filepath.Base(view.Ticket)), settings.InputHashes)
}
