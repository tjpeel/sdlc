package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/signing"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

// A generation is retained in controller-private state. Its identity includes
// every frozen setting, integration revision and ordered parent boundary.
// These seams substitute only external execution boundaries in offline tests.
type combinedOperations struct {
	Base           func(context.Context, string) (workseries.Target, error)
	Validate       func(context.Context, workseries.Target, []workseries.Dependency) error
	Snapshot       func(context.Context, workseries.Target, string) (workrun.BranchSnapshot, func(), error)
	ParentSnapshot func(context.Context, workseries.Dependency, string) (workrun.BranchSnapshot, error)
	Check          func(context.Context, workrun.DockerChecker, string, [][]string) error
	Recover        func(context.Context, workrun.DockerChecker) error
	Publish        func(context.Context, combinedGeneration, string) (workrun.Publication, error)
}

type combinedGeneration struct {
	Identity       string
	CheckDirectory string
	Target         workseries.Target
	Plan           workrun.Plan
	Revision       workrun.Revision
	Evidence       workrun.CheckEvidence
	Publication    workrun.Publication
	Failure        string
	Stage          string
}

func saveCombined(path string, generation combinedGeneration) error {
	data, err := json.Marshal(generation)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".generation-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (d *seriesDriver) validateCombinedParents(ctx context.Context, integration workseries.Target, parents []workseries.Dependency) error {
	if d.combinedOps != nil && d.combinedOps.Validate != nil {
		return d.combinedOps.Validate(ctx, integration, parents)
	}
	current, err := d.Base(ctx, integration.Base)
	if err != nil || current.SHA != integration.SHA {
		return fmt.Errorf("integration changed during combined preparation")
	}
	for _, parent := range parents {
		plan := d.publicationPlan()
		plan.Branch, plan.Base, plan.BaseSHA = parent.Branch, parent.Base, parent.BaseSHA
		observed, err := d.publisher(plan).Observe(ctx, plan, workrun.Publication{Number: parent.PRNumber, HeadSHA: parent.HeadSHA})
		if err != nil || observed.HeadSHA != parent.HeadSHA || observed.Base != parent.Base || observed.BaseSHA != parent.BaseSHA {
			return fmt.Errorf("combined dependency publication changed")
		}
		if parent.State == "ready" {
			current, err := d.Base(ctx, parent.Branch)
			if err != nil || current.SHA != parent.HeadSHA || observed.State != "OPEN" {
				return fmt.Errorf("combined dependency branch changed")
			}
			checks, err := d.publisher(plan).Checks(ctx, plan, workrun.Publication{Number: parent.PRNumber, HeadSHA: parent.HeadSHA, BaseSHA: parent.BaseSHA})
			if err != nil || checks.Status != "passed" {
				return fmt.Errorf("combined dependency is no longer ready on its exact published revision")
			}
		} else if parent.State != "merged" || observed.State != "MERGED" || observed.MergeSHA != parent.MergeSHA {
			return fmt.Errorf("combined dependency merge changed")
		}
	}
	return nil
}

func (d *seriesDriver) Combine(ctx context.Context, ticket workseries.Ticket, integration workseries.Target, parents []workseries.Dependency) (workseries.Target, error) {
	if err := d.checkInputs(); err != nil {
		return workseries.Target{}, err
	}
	if err := d.validateCombinedParents(ctx, integration, parents); err != nil {
		return workseries.Target{}, err
	}
	identityBytes, err := json.Marshal(struct {
		Ticket      workseries.Ticket
		Integration workseries.Target
		Parents     []workseries.Dependency
		Settings    seriesSettings
	}{ticket, integration, parents, d.settings})
	if err != nil {
		return workseries.Target{}, err
	}
	sum := sha256.Sum256(identityBytes)
	identity := hex.EncodeToString(sum[:])
	parentDirectory := filepath.Join(d.directory, "derived")
	if err := os.MkdirAll(parentDirectory, 0700); err != nil {
		return workseries.Target{}, err
	}
	directory := filepath.Join(parentDirectory, identity)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return workseries.Target{}, err
	}
	path := filepath.Join(directory, "generation.json")
	generation := combinedGeneration{Identity: identity, Stage: "preparing"}
	nugetCache, err := project.ResolveNugetCacheBudget(d.settings.Config.NugetCache)
	if err != nil {
		return workseries.Target{}, err
	}
	checker := workrun.DockerChecker{NugetCache: &nugetCache, WorkerName: "sdlc-check-" + identity[:24], InputDirectory: "", Runtime: d.runtime, ImageID: d.settings.ImageID, DockerTests: d.settings.DockerTests, DaemonImage: d.settings.DaemonImage, DaemonMode: d.settings.DaemonMode, UseDefaultDaemonImage: d.settings.DaemonImage == ""}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &generation) != nil || generation.Identity != identity {
			return workseries.Target{}, fmt.Errorf("invalid retained combined generation")
		}
		if generation.Failure != "" {
			return workseries.Target{}, fmt.Errorf("retained combined base failed: %s; repair the affected parent through its existing run, then resume the feature with a new ready parent revision", generation.Failure)
		}
		if generation.Stage == "published" {
			current, err := d.Base(ctx, generation.Target.Base)
			if err != nil || current.SHA != generation.Target.SHA {
				return workseries.Target{}, fmt.Errorf("immutable derived branch changed")
			}
			return generation.Target, nil
		}
	} else if !os.IsNotExist(err) {
		return workseries.Target{}, err
	}
	fail := func(err error) (workseries.Target, error) {
		if ctx.Err() == nil && (errors.Is(err, workrun.ErrCombinedConflict) || errors.Is(err, workrun.ErrCheckFailed)) {
			generation.Failure = err.Error()
			if saveErr := saveCombined(path, generation); saveErr != nil {
				return workseries.Target{}, saveErr
			}
		}
		return workseries.Target{}, err
	}
	if generation.Stage == "preparing" {
		// A preparation crash may leave incomplete objects, but no checks or native
		// job have started. Preserve the attempt and create another private workspace.
		attempt, err := os.MkdirTemp(directory, "prepare-")
		if err != nil {
			return fail(err)
		}
		snapshot, cleanup, err := d.snapshot(ctx, integration, attempt)
		if err != nil {
			return fail(err)
		}
		defer cleanup()
		snapshots := []workrun.BranchSnapshot{}
		for _, parent := range parents {
			if parent.State == "merged" {
				continue
			}
			private, err := os.MkdirTemp(attempt, "parent-")
			if err != nil {
				return fail(err)
			}
			var snapshot workrun.BranchSnapshot
			if d.combinedOps != nil && d.combinedOps.ParentSnapshot != nil {
				snapshot, err = d.combinedOps.ParentSnapshot(ctx, parent, private)
			} else {
				snapshot, err = d.publisher(d.publicationPlan()).Snapshot(ctx, d.publicationPlan(), parent.Branch, parent.HeadSHA, private)
			}
			if err != nil {
				return fail(err)
			}
			snapshots = append(snapshots, snapshot)
		}
		workspace := filepath.Join(attempt, "workspace")
		revision, err := workrun.CombineSnapshots(ctx, workspace, snapshot, snapshots)
		if err != nil {
			return fail(err)
		}
		generation.Revision = revision
		generation.Plan = d.publicationPlan()
		generation.Plan.NugetCache = &nugetCache
		generation.Plan.Base, generation.Plan.BaseSHA, generation.Plan.SourceSHA = integration.Base, integration.SHA, integration.SHA
		generation.Plan.Branch = "sdlc-derived/" + identity
		generation.Plan.Derived = &workrun.DerivedBoundary{}
		generation.Plan.Root = workspace
		generation.Plan.SigningImage = d.settings.SigningImage
		for _, parent := range parents {
			generation.Plan.Derived.Parents = append(generation.Plan.Derived.Parents, workrun.DerivedParent{Ticket: parent.Ticket, RunID: parent.RunID, Branch: parent.Branch, Base: parent.Base, BaseSHA: parent.BaseSHA, HeadSHA: parent.HeadSHA, State: parent.State, MergeSHA: parent.MergeSHA, Number: parent.PRNumber})
		}
		bundle, err := os.ReadFile(filepath.Join(attempt, "source.bundle"))
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "source.bundle"), bundle, 0600); err != nil {
			return fail(err)
		}
		generation.CheckDirectory = filepath.Join(attempt, "check-inputs")
		generation.Plan.CheckInputs, err = workrun.CaptureCombinedCheckInputs(d.plan.Root, generation.CheckDirectory, d.settings.Config.InputFiles)
		if err != nil {
			return fail(err)
		}
		for _, input := range generation.Plan.CheckInputs {
			if d.settings.InputHashes[input.Path] != input.SHA256 {
				return fail(fmt.Errorf("combined check input differs from frozen feature settings"))
			}
		}
		generation.Stage = "checking"
		if err := saveCombined(path, generation); err != nil {
			return workseries.Target{}, err
		}
	}
	if generation.Stage == "checks_started" {
		recover := checker.RecoverCombinedCheck
		if d.combinedOps != nil && d.combinedOps.Recover != nil {
			recover = func(ctx context.Context) error { return d.combinedOps.Recover(ctx, checker) }
		}
		if err := recover(ctx); err != nil {
			return workseries.Target{}, err
		}
		generation.Stage = "checking"
		if err := saveCombined(path, generation); err != nil {
			return workseries.Target{}, err
		}
	}
	if generation.Stage == "checking" {
		// Never repeat a check whose completion is uncertain after a controller crash.
		generation.Stage = "checks_started"
		if err := saveCombined(path, generation); err != nil {
			return workseries.Target{}, err
		}
		commands := d.settings.Config.Checks
		if d.settings.Config.CheckSelection == "runtime" {
			discovered, err := project.DiscoverChecks(generation.Plan.Root)
			if err != nil {
				return fail(err)
			}
			commands = discovered.Checks
		}
		if err := workrun.VerifyCapturedInputs(generation.CheckDirectory, generation.Plan.CheckInputs); err != nil {
			return fail(err)
		}
		checker.InputDirectory = generation.CheckDirectory
		check := func() error { return checker.Check(ctx, generation.Plan.Root, commands, d.output) }
		if d.combinedOps != nil && d.combinedOps.Check != nil {
			check = func() error { return d.combinedOps.Check(ctx, checker, generation.Plan.Root, commands) }
		}
		if err := check(); err != nil {
			return fail(err)
		}
		generation.Plan.Checks = commands
		if err := d.checkInputs(); err != nil {
			return fail(err)
		}
		generation.Evidence = workrun.CheckEvidence{Passed: true, Head: generation.Revision.Head, Tree: generation.Revision.Tree}
		generation.Stage = "checked"
		if err := saveCombined(path, generation); err != nil {
			return workseries.Target{}, err
		}
	}
	if generation.Stage == "checks_started" {
		return fail(fmt.Errorf("combined check completion is unknown after interruption; inspect retained generation"))
	}
	if generation.Stage == "checked" {
		if err := d.validateCombinedParents(ctx, integration, parents); err != nil {
			return fail(err)
		}
		var publication workrun.Publication
		var err error
		if d.combinedOps != nil && d.combinedOps.Publish != nil {
			publication, err = d.combinedOps.Publish(ctx, generation, directory)
		} else {
			profile, profileErr := runSigningProfile(d.runtime, generation.Plan)
			if profileErr != nil {
				return fail(profileErr)
			}
			publisher := d.publisher(generation.Plan)
			publisher.SigningKey = (signing.Resolver{Profile: profile, Image: generation.Plan.SigningImage}).Resolve
			publication, err = publisher.PublishDerived(ctx, generation.Plan, directory, generation.Revision, generation.Evidence)
		}
		if err != nil {
			return workseries.Target{}, err
		}
		generation.Publication = publication
		generation.Target = workseries.Target{Base: generation.Plan.Branch, SHA: publication.HeadSHA, Ancestors: integration.Ancestors, Dependencies: parents}
		generation.Target.Context, err = json.Marshal(workrun.DependencyContext{Identity: identity, Owner: d.directory, Ticket: ticket.File, IntegrationBranch: integration.Base, IntegrationSHA: integration.SHA, SourceSHA: generation.Revision.Head, SignedSHA: publication.HeadSHA, Tree: generation.Revision.Tree, Parents: generation.Plan.Derived.Parents, Evidence: generation.Evidence})
		if err != nil {
			return workseries.Target{}, err
		}
		generation.Stage = "published"
		if err := saveCombined(path, generation); err != nil {
			return workseries.Target{}, err
		}
	}
	if err := d.validateCombinedParents(ctx, integration, parents); err != nil {
		return workseries.Target{}, err
	}
	return generation.Target, nil
}

func (d *seriesDriver) Invalidate(ctx context.Context, ticket workseries.Ticket, result workseries.Result) error {
	if result.Directory == "" {
		return nil
	}
	directory, journal, err := d.loadRun(ticket.File, result)
	if err != nil {
		return err
	}
	if err := workrun.InvalidateDependency(directory); err != nil {
		return err
	}
	lock, err := filelock.Acquire(filepath.Join(directory, "run.lock"))
	if err != nil {
		return nil // The marker fences the active controller at its next operation.
	}
	defer lock.Close()
	current, err := workrun.Load(directory)
	if err != nil || current.UpdatedAt != journal.UpdatedAt {
		return fmt.Errorf("child changed while invalidating dependency")
	}
	journal.DependencyInvalid = true
	return workrun.Save(directory, &journal)
}

func (d *seriesDriver) validateDependencyTarget(ctx context.Context, target workseries.Target) error {
	if len(target.Dependencies) == 0 {
		return nil
	}
	integration := workseries.Target{Base: d.plan.Base, SHA: target.SHA}
	if len(target.Context) != 0 {
		var dependency workrun.DependencyContext
		if json.Unmarshal(target.Context, &dependency) != nil || dependency.SignedSHA != target.SHA {
			return fmt.Errorf("invalid dependency target context")
		}
		integration.SHA = dependency.IntegrationSHA
	}
	if err := d.validateCombinedParents(ctx, integration, target.Dependencies); err != nil {
		return err
	}
	current, err := d.Base(ctx, target.Base)
	if err != nil || current.SHA != target.SHA {
		return fmt.Errorf("tested dependency base changed before child execution")
	}
	return nil
}
