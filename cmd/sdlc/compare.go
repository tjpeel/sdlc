package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/workrun"
)

// compareCommand starts a separate run from the recorded source and inputs.
// It fails closed when the recorded source or captured requirements are unavailable.
func compareCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "source run")
	branch := flags.String("branch", "", "new branch")
	provider := flags.String("provider", "", "implementation provider")
	model := flags.String("model", "", "implementation model")
	effort := flags.String("effort", "", "implementation effort")
	reviewModel := flags.String("review-model", "", "review model")
	reviewEffort := flags.String("review-effort", "", "review effort")
	dry := flags.Bool("dry-run", false, "preview")
	var amendments selectedInputs
	flags.Var(&amendments, "amendment", "registered amendment key; repeatable")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc compare --run RUN_ID --branch BRANCH [--provider NAME] [--model MODEL] [--effort LEVEL] [--review-model MODEL] [--review-effort LEVEL] [--amendment KEY] [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *branch == "" || flags.NArg() != 0 {
		return errors.New("compare requires --run and a distinct --branch")
	}
	action, err := inspectRunAction(ctx, *id, false, true)
	if err != nil {
		return err
	}
	old := action.View.Journal.Plan
	// Prior amendments are not implicitly selected for a new comparison.
	priorAmendments := map[string]bool{}
	for _, in := range old.Amendments {
		priorAmendments[in.Path] = true
	}
	baseInputs := make([]workrun.Input, 0, len(old.Inputs))
	for _, in := range old.Inputs {
		if !priorAmendments[in.Path] {
			baseInputs = append(baseInputs, in)
		}
	}
	old.Inputs, old.Amendments = baseInputs, nil
	if *branch == old.Branch {
		return errors.New("comparison requires a different branch")
	}
	if err := workrun.VerifyCapturedInputs(action.View.Journal.Workspace, old.Inputs); err != nil {
		return fmt.Errorf("source requirements unavailable or changed: %w", err)
	}
	if len(old.CheckInputs) > 0 {
		if err := workrun.VerifyCapturedInputs(filepath.Join(action.View.Directory, "check-inputs"), old.CheckInputs); err != nil {
			return fmt.Errorf("source check inputs unavailable or changed: %w", err)
		}
	}
	var amendmentInputs []workrun.Input
	seen := map[string]bool{}
	for _, key := range amendments {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(key) || seen[key] {
			return errors.New("invalid or duplicate amendment key")
		}
		seen[key] = true
		rel := filepath.ToSlash(filepath.Join(".sdlc", "work", old.Reference, "amendments", key+".md"))
		dir := filepath.Join(old.Root, ".sdlc", "work", old.Reference, "amendments")
		if err := validateAmendmentDirectory(old.Root, dir); err != nil {
			return err
		}
		path := filepath.Join(old.Root, filepath.FromSlash(rel))
		data, err := readHumanAnswer(path)
		if err != nil {
			return fmt.Errorf("registered amendment %s: %w", key, err)
		}
		if err := validateAmendmentExisting(path, string(data)); err != nil {
			return err
		}
		prefix := "Approved specification amendment " + key + "\nTarget captured requirement: "
		if !strings.HasPrefix(string(data), prefix) {
			return errors.New("invalid registered amendment")
		}
		target, _, ok := strings.Cut(strings.TrimPrefix(string(data), prefix), "\n\n")
		if !ok {
			return errors.New("invalid registered amendment target")
		}
		found := false
		for _, in := range old.Inputs {
			if in.Path == target {
				found = true
				break
			}
		}
		if !found {
			return errors.New("amendment target is not a source-run requirement")
		}
		sum := sha256.Sum256(data)
		amendmentInputs = append(amendmentInputs, workrun.Input{Path: rel, SHA256: fmt.Sprintf("%x", sum)})
	}
	argv := []string{"--reference", old.Reference, "--ticket", filepath.Base(old.Ticket), "--base", old.Base, "--branch", *branch, "--repo", old.Repository}
	for _, in := range old.Inputs {
		if in.Path != old.Ticket {
			argv = append(argv, "--input", in.Path)
		}
	}
	for _, in := range amendmentInputs {
		argv = append(argv, "--input", in.Path)
	}
	for _, v := range []struct{ flag, value string }{{"--provider", *provider}, {"--model", *model}, {"--effort", *effort}, {"--review-model", *reviewModel}, {"--review-effort", *reviewEffort}} {
		if v.value != "" {
			argv = append(argv, v.flag, v.value)
		}
	}
	if *dry {
		argv = append(argv, "--dry-run")
	}
	options, err := parseRunOptions(argv)
	if err != nil {
		return err
	}
	options.root = old.Root
	options.comparison = &old
	options.frozenInputs = true
	options.comparisonID = action.View.ID
	options.comparisonWorkspace = action.View.Journal.Workspace
	options.comparisonAmendments = amendmentInputs
	return runSelectedCommand(ctx, options, output)
}

func validateComparison(ctx context.Context, launch project.LaunchResult, old workrun.Plan, amendments []workrun.Input) error {
	if launch.Root != old.Root {
		return errors.New("comparison source run belongs to another project")
	}
	if launch.Config.CheckSelection != old.CheckSelection || !reflect.DeepEqual(launch.Config.Checks, old.Checks) || launch.Config.DockerTests != old.DockerTests {
		return errors.New("comparison verification settings differ from source run")
	}
	if !sameSeriesInputPaths(append(append([]workrun.Input{}, old.Inputs...), amendments...), launch.Inputs) || !sameSeriesInputPaths(old.CheckInputs, launch.Config.InputFiles) {
		return errors.New("comparison input selection differs from source run")
	}
	if err := workrun.VerifyCapturedInputs(old.Root, amendments); err != nil {
		return err
	}
	return nil
}

func comparisonHashes(p workrun.Plan, amendments []workrun.Input) (map[string]string, error) {
	hashes := map[string]string{}
	all := append(append(append([]workrun.Input{}, p.Inputs...), p.CheckInputs...), amendments...)
	for _, in := range all {
		if current, ok := hashes[in.Path]; ok && current != in.SHA256 {
			return nil, fmt.Errorf("comparison input %s has conflicting captured hashes", in.Path)
		}
		hashes[in.Path] = in.SHA256
	}
	return hashes, nil
}
