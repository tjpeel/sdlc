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

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

const provisionalTicketUsage = "Usage: sdlc ticket provisional --reference REF --ticket NUMBERED_FILE [--revoke] [--dry-run] [--json] [--accept-review-limitations] [--reconcile-descendants]"

func ticketProvisionalCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("ticket provisional", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "work reference")
	ticket := flags.String("ticket", "", "numbered ticket file")
	root := flags.String("run-root", "", "project root")
	revoke := flags.Bool("revoke", false, "revoke the provisional grant")
	dry := flags.Bool("dry-run", false, "preview without writing")
	structured := flags.Bool("json", false, "JSON preview")
	accept := flags.Bool("accept-review-limitations", false, "acknowledge review limitations shown by dry run")
	reconcile := flags.Bool("reconcile-descendants", false, "fence started descendants for reconciliation")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, provisionalTicketUsage)
		return err
	} else if err != nil {
		return err
	}
	if *reference == "" || *ticket == "" || flags.NArg() != 0 {
		return errors.New(provisionalTicketUsage)
	}
	if *root == "" {
		var err error
		*root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	var err error
	if *revoke {
		top, e := seriesGit(ctx, *root, "rev-parse", "--show-toplevel")
		if e != nil {
			return e
		}
		*root, err = filepath.EvalSymlinks(strings.TrimSpace(top))
		if err != nil {
			return err
		}
	} else {
		work, e := project.InspectWork(ctx, *root, *reference)
		if e != nil {
			return e
		}
		*root = work.Root
	}
	var verify workseries.ProvisionalVerifier
	var invalidate workseries.ProvisionalInvalidator
	if !*revoke {
		dir, err := workseries.Directory(*root, *reference, false)
		if err != nil {
			return err
		}
		state, err := workseries.Load(dir)
		if err != nil {
			return err
		}
		settings, err := loadSeriesSettings(state.Settings)
		if err != nil {
			return err
		}
		manager, err := runtimeimage.NewNamed(io.Discard, io.Discard, settings.RuntimeName)
		if err != nil {
			return err
		}
		if !*dry {
			lease, err := leaseRuntimeRun(ctx, manager.Directory, out)
			if err != nil {
				return err
			}
			defer lease.Close()
		}
		current, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if current.ImageID != settings.ImageID {
			return fmt.Errorf("feature runtime image changed")
		}
		if err := checkSeriesRuntime(ctx, manager, current.ImageID); err != nil {
			return err
		}
		verify = func(ctx context.Context, state workseries.State, j workrun.Journal, proof *workrun.ProvisionalParentProof) error {
			currentPlan, err := workseries.Discover(ctx, state.Plan.Root, state.Plan.Reference, state.Plan.Base)
			if err != nil || !reflect.DeepEqual(currentPlan, state.Plan) {
				return fmt.Errorf("feature definition differs from its saved plan")
			}
			frozen, err := loadSeriesSettings(state.Settings)
			if err != nil {
				return err
			}
			if frozen.ImageID != current.ImageID || frozen.RuntimeName != settings.RuntimeName {
				return fmt.Errorf("feature runtime changed during admission")
			}
			d := &seriesDriver{runtime: manager, plan: state.Plan, settings: frozen, output: io.Discard}
			invalidate = d.Invalidate
			if err := d.validateSettings(); err != nil {
				return err
			}
			if err := d.checkInputs(); err != nil {
				return err
			}
			dir, err := workrun.RunDirectory(state.Plan.Root, state.Plan.Reference, filepath.Base(j.Plan.Ticket), j.ID, false)
			if err != nil {
				return err
			}
			if err := d.validateRunJournal(filepath.Base(j.Plan.Ticket), dir, j); err != nil {
				return err
			}
			if err := workrun.VerifyRuntimeMigrationInputs(dir, j); err != nil {
				return err
			}
			repository := workrun.DockerRepository{Runtime: manager, ImageID: frozen.ImageID}
			revision, err := repository.Inspect(ctx, j.Workspace)
			if err != nil || !revision.Clean || revision.Head != proof.CandidateHead || revision.Tree != proof.CandidateTree {
				return fmt.Errorf("parent candidate changed after its checks")
			}
			publisher := d.publisher(j.Plan)
			check := func() error {
				if err := publisher.ValidateFeedbackBoundary(ctx, j.Plan, proof.Publication); err != nil {
					return err
				}
				branch, err := publisher.Branch(ctx, j.Plan, proof.Branch)
				if err != nil {
					return fmt.Errorf("parent branch inspection failed: %w", err)
				}
				if branch.Branch != proof.Branch || branch.SHA != proof.Publication.HeadSHA {
					return fmt.Errorf("parent branch differs from the saved head")
				}
				return nil
			}
			if err := check(); err != nil {
				return err
			}
			checks, err := publisher.Checks(ctx, j.Plan, proof.Publication)
			if err != nil {
				return fmt.Errorf("parent CI inspection failed: %w", err)
			}
			status, identities, err := workrun.ProvisionalCIIdentity(j, checks)
			if err != nil {
				return err
			}
			if proof.Kind == "reviewed" && status != "passed" {
				return fmt.Errorf("reviewed provisional parent requires current passed CI")
			}
			proof.CIStatus, proof.CIIdentities, proof.CIHistorical = status, identities, false
			comments, err := publisher.Feedback(ctx, j.Plan, proof.Publication)
			if err != nil {
				return err
			}
			if err := workrun.ValidateProvisionalFeedback(j, comments); err != nil {
				return err
			}
			return check()
		}
	}
	preview, err := workseries.SetProvisionalParentWithReconciliation(ctx, *root, *reference, *ticket, *revoke, *dry, *accept, *reconcile, verify, bindProvisionalInvalidator(&invalidate))
	if err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(out).Encode(preview)
	}
	if preview.Proof.Kind == "reviewed" && preview.Proof.ReviewLimitations != "null" && preview.Proof.ReviewLimitations != "[]" {
		var limitations []string
		if err := json.Unmarshal([]byte(preview.Proof.ReviewLimitations), &limitations); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "Review limitations:"); err != nil {
			return err
		}
		for _, limitation := range limitations {
			if _, err := fmt.Fprintf(out, "- %s\n", limitation); err != nil {
				return err
			}
		}
	}
	if len(preview.ReconcileDescendants) > 0 {
		if _, err := fmt.Fprintf(out, "Descendants to fence and reconcile: %s\n", strings.Join(preview.ReconcileDescendants, ", ")); err != nil {
			return err
		}
	}
	action := "Provisional parent granted"
	if *revoke {
		action = "Provisional parent revoked"
	}
	if *dry {
		action = "Provisional parent preview"
	}
	_, err = fmt.Fprintf(out, "%s: %s, run %s, PR #%d at %s. Parent CI and readiness remain open.\n", action, preview.Ticket, preview.Proof.RunID, preview.Proof.Publication.Number, preview.Proof.Publication.HeadSHA)
	if err != nil {
		return err
	}
	label := "saved parent CI"
	if preview.Proof.CIHistorical {
		label = "historical parent CI (not current)"
	}
	_, err = fmt.Fprintf(out, "  %s on %s: %s", label, preview.Proof.CIHead, preview.Proof.CIStatus)
	if err != nil {
		return err
	}
	if preview.Proof.CIIdentities != "" {
		_, err = fmt.Fprintf(out, " | %s", preview.Proof.CIIdentities)
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, ". Child checks and CI are independent.")
	return err
}

// The verifier installs the runtime-backed invalidator after admission begins.
// Bind the variable, not its initial nil value, before passing it to admission.
func bindProvisionalInvalidator(invalidate *workseries.ProvisionalInvalidator) workseries.ProvisionalInvalidator {
	return func(ctx context.Context, ticket workseries.Ticket, result workseries.Result) error {
		if invalidate == nil || *invalidate == nil {
			return errors.New("descendant fencing is unavailable")
		}
		return (*invalidate)(ctx, ticket, result)
	}
}
