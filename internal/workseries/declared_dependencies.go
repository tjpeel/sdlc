package workseries

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/project"
)

type DependencyMismatch struct {
	Ticket   string   `json:"ticket"`
	Declared []string `json:"declared"`
	Planned  []string `json:"planned"`
}

func equalDependencies(a, b []string) bool {
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

// Ordinary links do not become dependencies. Missing legacy metadata stays unclassified.
func DeclaredDependencyMismatches(ctx context.Context, p Plan, selected ...string) ([]DependencyMismatch, error) {
	declarations, err := project.TicketBlockerDeclarations(ctx, p.Root, p.Reference, selected...)
	if err != nil {
		return nil, err
	}
	known := ticketMap(p.Tickets)
	result := []DependencyMismatch{}
	for _, t := range p.Tickets {
		if len(selected) > 0 && selected[0] != "" && t.File != selected[0] {
			continue
		}
		d := declarations[t.File]
		if !d.Present || d.Completed {
			continue
		}
		for _, parent := range d.Parents {
			if _, ok := known[parent]; !ok {
				return nil, fmt.Errorf("%s declares unknown blocker %s", t.File, parent)
			}
		}
		if !equalDependencies(d.Parents, t.DependsOn) {
			result = append(result, DependencyMismatch{Ticket: t.File, Declared: d.Parents, Planned: t.DependsOn})
		}
	}
	return result, nil
}
func ValidateDeclaredDependencies(ctx context.Context, p Plan, selected string) error {
	mismatches, err := DeclaredDependencyMismatches(ctx, p, selected)
	if err != nil {
		return err
	}
	for _, m := range mismatches {
		if selected != "" && selected != m.Ticket {
			continue
		}
		return fmt.Errorf("%s Blocked by metadata declares %v but scheduling plan has %v; before first feature execution use sdlc dependencies --reference %q --from-tickets; for a saved feature use the per-ticket dependencies --ticket amendment", m.Ticket, m.Declared, m.Planned, p.Reference)
	}
	return nil
}

type DependencyImportPreview struct {
	Plan       Plan                 `json:"plan"`
	Mismatches []DependencyMismatch `json:"mismatches"`
	DryRun     bool                 `json:"dry_run"`
	Changed    bool                 `json:"changed"`
}

// ImportDeclaredDependencies owns only the initial scheduling handoff. Saved
// features retain the existing audited per-ticket amendment and run protections.
func ImportDeclaredDependencies(ctx context.Context, root, reference, base string, dry bool) (DependencyImportPreview, error) {
	preview := DependencyImportPreview{DryRun: dry}
	p, err := Discover(ctx, root, reference, base)
	if err != nil {
		return preview, err
	}
	root = p.Root
	preview.Plan = p
	beforeDefinition := p.DefinitionSHA
	declarations, err := project.TicketBlockerDeclarations(ctx, p.Root, reference)
	if err != nil {
		return preview, err
	}
	preview.Mismatches, err = DeclaredDependencyMismatches(ctx, p)
	if err != nil {
		return preview, err
	}
	for i, t := range p.Tickets {
		d := declarations[t.File]
		if d.Present && !d.Completed {
			p.Tickets[i].DependsOn = exactStrings(d.Parents)
		}
	}
	if err = validatePlan(&p); err != nil {
		return preview, err
	}
	p.DefinitionSHA = definitionSHA(p)
	preview.Plan = p
	path := filepath.Join(root, ".sdlc", "work", reference, "plan.json")
	_, statErr := os.Lstat(path)
	preview.Changed = !os.IsNotExist(statErr) && len(preview.Mismatches) > 0 || os.IsNotExist(statErr)
	savedDir := filepath.Join(root, ".sdlc", "work", reference, "series")
	for _, name := range []string{"journal.json", amendmentPending, completionPending} {
		if _, e := os.Lstat(filepath.Join(savedDir, name)); !os.IsNotExist(e) {
			return preview, errors.New("saved feature state exists; use the per-ticket dependencies amendment instead of --from-tickets")
		}
	}
	// A read-only preview needs no new series directory or lock file.
	if dry {
		return preview, nil
	}
	dir, err := Directory(root, reference, true)
	if err != nil {
		return preview, err
	}
	lockPath := filepath.Join(dir, "series.lock")
	if info, e := os.Lstat(lockPath); e == nil && (!info.Mode().IsRegular() || !amendmentOwned(info) || info.Mode().Perm() != 0600) {
		return preview, errors.New("unsafe feature lock")
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return preview, err
	}
	defer lock.Close()
	for _, name := range []string{"journal.json", amendmentPending, completionPending} {
		if _, e := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(e) {
			return preview, errors.New("saved feature state exists; use the per-ticket dependencies amendment instead of --from-tickets")
		}
	}
	current, err := Discover(ctx, root, reference, base)
	if err != nil {
		return preview, err
	}
	currentDeclarations, err := project.TicketBlockerDeclarations(ctx, root, reference)
	if err != nil {
		return preview, err
	}
	if current.DefinitionSHA != beforeDefinition || !reflect.DeepEqual(declarations, currentDeclarations) {
		return preview, errors.New("ticket dependencies changed during import; retry")
	}
	if !preview.Changed {
		return preview, nil
	}
	data, err := completeAmendmentPlan(p)
	if err != nil {
		return preview, err
	}
	if err = writeAmendmentFile(path, data); err != nil {
		return preview, err
	}
	return preview, nil
}
