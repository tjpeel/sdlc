package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

const ticketUsage = `Usage: sdlc ticket status --reference REFERENCE [--ticket NUMBERED_FILE] [--refresh] [--json]
       sdlc ticket complete --reference REFERENCE --ticket NUMBERED_FILE --pr PR_URL [--dry-run] [--json]
Status is local unless --refresh is supplied. Completion verifies a merged PR and integration ancestry, updates the journal and descriptive ticket metadata, and preserves previous run history.`

type ticketSavedRun struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type ticketStatus struct {
	DeclaredDependencies []string         `json:"declared_dependencies,omitempty"`
	PlannedDependencies  []string         `json:"planned_dependencies,omitempty"`
	DependencyMismatch   bool             `json:"dependency_mismatch"`
	SavedRuns            []ticketSavedRun `json:"saved_runs,omitempty"`
	Ticket               string           `json:"ticket"`
	State                string           `json:"state"`
	DeclaredStatus       string           `json:"declared_status"`
	DeclaredBlockers     string           `json:"declared_blockers"`
	BlockedBy            []string         `json:"blocked_by"`
	MetadataStale        bool             `json:"metadata_stale"`
	PR                   string           `json:"pr,omitempty"`
	HeadSHA              string           `json:"head_sha,omitempty"`
	MergeSHA             string           `json:"merge_sha,omitempty"`
	RemoteState          string           `json:"remote_state,omitempty"`
	NeedsCompletion      bool             `json:"needs_completion"`
}

var ticketGH = func(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("GitHub inspection failed (check gh auth status and repository access): %w", err)
	}
	return data, nil
}

type ticketRemotePR struct {
	Number            int       `json:"number"`
	URL               string    `json:"url"`
	State             string    `json:"state"`
	BaseRefName       string    `json:"baseRefName"`
	HeadRefName       string    `json:"headRefName"`
	HeadRefOid        string    `json:"headRefOid"`
	IsCrossRepository bool      `json:"isCrossRepository"`
	MergedAt          time.Time `json:"mergedAt"`
	MergeCommit       struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func inspectTicketPR(ctx context.Context, root, repository string, number int) (ticketRemotePR, error) {
	var p ticketRemotePR
	data, err := ticketGH(ctx, root, "pr", "view", fmt.Sprint(number), "--repo", repository, "--json", "number,url,state,baseRefName,headRefName,headRefOid,isCrossRepository,mergedAt,mergeCommit")
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, errors.New("invalid GitHub PR inspection response")
	}
	if p.Number != number || !strings.EqualFold(p.URL, fmt.Sprintf("https://github.com/%s/pull/%d", repository, number)) {
		return p, errors.New("GitHub returned a different PR")
	}
	return p, nil
}
func verifiedTicketMerge(ctx context.Context, root, repository string, number int) (workseries.MergedTicket, error) {
	var e workseries.MergedTicket
	p, err := inspectTicketPR(ctx, root, repository, number)
	if err != nil {
		return e, err
	}
	if p.State != "MERGED" || p.IsCrossRepository || p.MergeCommit.OID == "" || p.MergedAt.IsZero() {
		return e, errors.New("completion requires a merged same-repository PR")
	}
	// Freeze the current integration SHA, then compare exact commits rather than a moving branch.
	data, err := ticketGH(ctx, root, "api", "repos/"+repository+"/git/ref/heads/"+url.PathEscape(p.BaseRefName))
	if err != nil {
		return e, err
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if json.Unmarshal(data, &ref) != nil || len(ref.Object.SHA) != 40 {
		return e, errors.New("invalid integration branch response")
	}
	data, err = ticketGH(ctx, root, "api", "repos/"+repository+"/compare/"+p.MergeCommit.OID+"..."+ref.Object.SHA)
	if err != nil {
		return e, err
	}
	var comparison struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &comparison) != nil || (comparison.Status != "ahead" && comparison.Status != "identical") {
		return e, errors.New("merged commit is not an ancestor of the current integration branch")
	}
	e = workseries.MergedTicket{Repository: repository, Number: number, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repository, number), Branch: p.HeadRefName, Base: p.BaseRefName, IntegrationSHA: ref.Object.SHA, HeadSHA: p.HeadRefOid, MergeSHA: p.MergeCommit.OID, MergedAt: p.MergedAt}
	return e, nil
}
func ticketCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(ticketUsage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(output, ticketUsage)
		return err
	}
	action := args[0]
	if action != "status" && action != "complete" {
		return errors.New(ticketUsage)
	}
	flags := flag.NewFlagSet("ticket "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "work reference")
	ticket := flags.String("ticket", "", "exact numbered filename")
	root := flags.String("run-root", "", "project root")
	pr := flags.String("pr", "", "merged PR URL")
	dry := flags.Bool("dry-run", false, "verify and preview completion without writing")
	structured := flags.Bool("json", false, "JSON output")
	refresh := flags.Bool("refresh", false, "inspect recorded PRs on GitHub")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, ticketUsage)
		return err
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 || *reference == "" || (action == "complete" && (*ticket == "" || *pr == "" || *refresh)) || (action == "status" && (*pr != "" || *dry)) {
		return errors.New(ticketUsage)
	}
	if *root == "" {
		var err error
		*root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	work, err := project.InspectWork(ctx, *root, *reference)
	if err != nil {
		return err
	}
	*root = work.Root
	dir := filepath.Join(*root, ".sdlc", "work", *reference, "series")
	_, dirErr := os.Lstat(dir)
	if dirErr == nil {
		dir, dirErr = workseries.Directory(*root, *reference, false)
	}
	var state workseries.State
	pending := false
	if dirErr == nil {
		pending = workseries.CheckTicketCompletion(*root, *reference) != nil
		if pending && action == "status" {
			return errors.New("ticket completion pending; repeat ticket complete to recover it before inspecting status")
		}
		if !pending {
			if _, statErr := os.Lstat(filepath.Join(dir, "journal.json")); statErr != nil {
				if !os.IsNotExist(statErr) {
					return statErr
				}
			} else {
				state, err = workseries.Load(dir)
				if err != nil {
					return err
				}
			}
		}
	} else if !os.IsNotExist(dirErr) {
		return dirErr
	}
	if action == "complete" {
		repository, number, err := workrun.ParsePRURL(*pr)
		if err != nil {
			return err
		}
		if dirErr != nil || (!pending && state.Version == 0) {
			return errors.New("ticket complete requires a saved feature checkpoint; inspect the feature plan before running completion")
		}
		var settings struct{ Repository string }
		if !pending {
			if len(state.Settings) > 0 && json.Unmarshal(state.Settings, &settings) != nil {
				return errors.New("invalid feature settings")
			}
			expected := settings.Repository
			if expected == "" {
				expected, err = originRepository(ctx, *root)
				if err != nil {
					return err
				}
			}
			if !strings.EqualFold(expected, repository) {
				return errors.New("PR belongs to a different repository")
			}
		}
		evidence, err := verifiedTicketMerge(ctx, *root, repository, number)
		if err != nil {
			return err
		}
		preview, err := workseries.CompleteTicket(ctx, *root, *reference, *ticket, evidence, *dry)
		if err != nil {
			return err
		}
		if *structured {
			return json.NewEncoder(output).Encode(preview)
		}
		evidence = preview.Evidence
		verb := "Recorded"
		if *dry {
			verb = "Would record"
		}
		if !preview.Changed {
			verb = "Already recorded"
		}
		_, err = fmt.Fprintf(output, "%s %s as merged: %s\nHead: %s\nMerge: %s\nIntegration: %s\nTicket metadata and frozen input hashes are reconciled; original runs are retained.\n", verb, *ticket, evidence.URL, evidence.HeadSHA, evidence.MergeSHA, evidence.IntegrationSHA)
		return err
	}
	base := "main"
	if state.Plan.Base != "" {
		base = state.Plan.Base
	}
	plan, err := workseries.Discover(ctx, *root, *reference, base)
	if err != nil {
		return err
	}
	dependencyMismatches, err := workseries.DeclaredDependencyMismatches(ctx, plan, *ticket)
	if err != nil {
		return err
	}
	mismatchByTicket := map[string]workseries.DependencyMismatch{}
	for _, m := range dependencyMismatches {
		mismatchByTicket[m.Ticket] = m
	}
	rows := []ticketStatus{}
	found := *ticket == ""
	for _, t := range plan.Tickets {
		if *ticket != "" && t.File != *ticket {
			continue
		}
		found = true
		data, err := workseries.ReadTicketMetadata(*root, *reference, t.File)
		if err != nil {
			return err
		}
		declared, blockers := workseries.TicketMetadata(data)
		r, recorded := state.Results[t.File]
		saved := []ticketSavedRun{}
		if !recorded {
			runs, err := inspectUnlinkedTicketRuns(*root, *reference, t.File)
			if err != nil {
				return err
			}
			for _, j := range runs {
				saved = append(saved, ticketSavedRun{ID: j.ID, State: j.State})
			}
			if len(runs) == 1 {
				j := runs[0]
				r = workseries.Result{RunID: j.ID, State: j.State, PRNumber: j.Publication.Number, URL: j.Publication.URL, HeadSHA: j.Publication.HeadSHA}
			}
			if len(runs) > 1 {
				r.State = "multiple_saved_runs"
			}
		}
		effective := r.State
		if effective == "" {
			effective = "pending"
		}
		blocked := []string{}
		for _, parent := range t.DependsOn {
			s := state.Results[parent].State
			if s != "merged" && s != "ready" {
				blocked = append(blocked, parent)
			}
		}
		if effective == "pending" && len(blocked) > 0 {
			effective = "waiting_for_dependencies"
		}
		done := effective == "merged"
		row := ticketStatus{SavedRuns: saved, Ticket: t.File, State: effective, DeclaredStatus: declared, DeclaredBlockers: blockers, BlockedBy: blocked, MetadataStale: (done && declared != "completed") || (!done && declared == "completed") || (len(blocked) > 0 && strings.HasPrefix(blockers, "None")), PR: r.URL, HeadSHA: r.HeadSHA, MergeSHA: r.MergeSHA}
		if mismatch, ok := mismatchByTicket[t.File]; ok {
			row.DependencyMismatch = true
			row.DeclaredDependencies = mismatch.Declared
			row.PlannedDependencies = mismatch.Planned
			row.MetadataStale = true
		}
		row.NeedsCompletion = done && row.MetadataStale
		if *refresh && r.PRNumber > 0 {
			repository, _, err := workrun.ParsePRURL(r.URL)
			if err != nil {
				return err
			}
			remote, err := inspectTicketPR(ctx, *root, repository, r.PRNumber)
			if err != nil {
				return err
			}
			row.RemoteState = remote.State
			row.NeedsCompletion = remote.State == "MERGED" && (r.State != "merged" || row.MetadataStale)
		}
		rows = append(rows, row)
	}
	if !found {
		return errors.New("unknown ticket")
	}
	if *structured {
		return json.NewEncoder(output).Encode(rows)
	}
	for _, r := range rows {
		if _, err = fmt.Fprintf(output, "%s: %s; ticket status: %s; blockers: %v", r.Ticket, r.State, r.DeclaredStatus, r.BlockedBy); err != nil {
			return err
		}
		if len(r.SavedRuns) > 0 {
			fmt.Fprintf(output, "; saved runs: %v", r.SavedRuns)
		}
		if r.RemoteState != "" {
			fmt.Fprintf(output, "; GitHub: %s", r.RemoteState)
		}
		if r.DependencyMismatch {
			fmt.Fprintf(output, "; dependency mismatch: declared %v, planned %v", r.DeclaredDependencies, r.PlannedDependencies)
		}
		if r.NeedsCompletion {
			fmt.Fprint(output, "; use ticket complete --pr to reconcile")
		}
		if r.MetadataStale {
			fmt.Fprint(output, "; ticket metadata is stale")
		}
		fmt.Fprintln(output)
	}
	return nil
}

var ticketLoadRun = workrun.Load

// Individual runs need not be owned by a feature checkpoint. Do not guess between
// multiple retained runs; report all of them until completion is reconciled.
func inspectUnlinkedTicketRuns(root, reference, ticket string) ([]workrun.Journal, error) {
	result := []workrun.Journal{}
	parent := filepath.Join(root, ".sdlc", "work", reference, "runs")
	for _, path := range []string{parent, filepath.Join(parent, strings.TrimSuffix(ticket, ".md"))} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe saved ticket run directory")
		}
	}
	entries, err := os.ReadDir(filepath.Join(parent, strings.TrimSuffix(ticket, ".md")))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory, err := workrun.RunDirectory(root, reference, ticket, entry.Name(), false)
		if err != nil {
			return nil, err
		}
		if _, err = os.Lstat(filepath.Join(directory, "journal.json")); os.IsNotExist(err) {
			result = append(result, workrun.Journal{ID: entry.Name(), State: "preparing"})
			continue
		} else if err != nil {
			return nil, err
		}
		j, err := ticketLoadRun(directory)
		if err != nil {
			return nil, err
		}
		if j.Plan.Root != root || j.Plan.Reference != reference || filepath.Base(j.Plan.Ticket) != ticket || j.ID != entry.Name() {
			return nil, errors.New("saved run does not belong to selected ticket")
		}
		result = append(result, j)
	}
	return result, nil
}
