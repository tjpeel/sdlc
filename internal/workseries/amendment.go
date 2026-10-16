package workseries

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const amendmentPending = "dependency-amendment.pending.json"
const maximumAmendment = 3 * 1024 * 1024

type DependencyAmendmentAudit struct {
	Ticket string    `json:"ticket"`
	Before string    `json:"before_definition"`
	After  string    `json:"after_definition"`
	From   []string  `json:"from"`
	To     []string  `json:"to"`
	At     time.Time `json:"at"`
}
type DependencyAmendmentPreview struct {
	Ticket   string   `json:"ticket"`
	From     []string `json:"from"`
	To       []string `json:"to"`
	Before   string   `json:"before_definition"`
	After    string   `json:"after_definition"`
	DryRun   bool     `json:"dry_run"`
	Recovery bool     `json:"recovery"`
	Changed  bool     `json:"changed"`
}
type amendmentTransaction struct {
	Version        int      `json:"version"`
	Ticket         string   `json:"ticket"`
	Dependencies   []string `json:"dependencies"`
	OldJournal     []byte   `json:"old_journal"`
	NewJournal     []byte   `json:"new_journal"`
	OldPlan        []byte   `json:"old_plan"`
	NewPlan        []byte   `json:"new_plan"`
	OldPlanPresent bool     `json:"old_plan_present"`
	OldJournalHash string   `json:"old_journal_sha256"`
	NewJournalHash string   `json:"new_journal_sha256"`
	OldPlanHash    string   `json:"old_plan_sha256"`
	NewPlanHash    string   `json:"new_plan_sha256"`
}

func exactStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
func amendmentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func amendmentJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid dependency amendment state")
	}
	return nil
}
func validateAmendmentAudit(s *State) error {
	if len(s.DependencyAmendments) > 64 {
		return errors.New("dependency amendment audit exceeds bound")
	}
	for i, a := range s.DependencyAmendments {
		if _, ok := ticketMap(s.Plan.Tickets)[a.Ticket]; !ok || a.At.IsZero() || !validAmendmentHash(a.Before) || !validAmendmentHash(a.After) || a.Before == a.After {
			return errors.New("invalid dependency amendment audit")
		}
		if i > 0 && s.DependencyAmendments[i-1].After != a.Before {
			return errors.New("dependency amendment audit chain changed")
		}
	}
	if n := len(s.DependencyAmendments); n > 0 && s.DependencyAmendments[n-1].After != s.Plan.DefinitionSHA {
		return errors.New("dependency amendment audit differs from saved definition")
	}
	return nil
}
func validAmendmentHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && len(value) == 64
}

func readAmendmentFile(path string, maximum int64, optional bool) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && optional {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !amendmentOwned(info) || info.Mode().Perm() != 0600 || info.Size() > maximum {
		return nil, false, fmt.Errorf("unsafe dependency amendment file: %s", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, errors.New("dependency amendment file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || len(data) > int(maximum) || len(data) != int(info.Size()) {
		return nil, false, errors.New("dependency amendment file changed while reading")
	}
	return data, true, nil
}
func ownedAmendmentDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !amendmentOwned(info) {
		return errors.New("dependency amendment requires owned real directories")
	}
	return nil
}
func amendmentPaths(root, reference string) (string, string, error) {
	directory, err := Directory(root, reference, false)
	if err != nil {
		return "", "", err
	}
	if err := privateSeriesDir(directory); err != nil {
		return "", "", err
	}
	for _, path := range []string{root, filepath.Join(root, ".sdlc"), filepath.Join(root, ".sdlc", "work"), filepath.Join(root, ".sdlc", "work", reference), directory} {
		if err := ownedAmendmentDirectory(path); err != nil {
			return "", "", err
		}
	}
	return directory, filepath.Join(root, ".sdlc", "work", reference, "plan.json"), nil
}
func amendmentUnstarted(directory string, state State, ticket string) error {
	if _, ok := ticketMap(state.Plan.Tickets)[ticket]; !ok {
		return errors.New("dependency amendment requires an existing safe ticket filename")
	}
	if _, exists := state.Results[ticket]; exists {
		return errors.New("dependency amendment refuses a reserved or started ticket")
	}
	runs := filepath.Join(state.Plan.Root, ".sdlc", "work", state.Plan.Reference, "runs")
	if _, err := os.Lstat(runs); !os.IsNotExist(err) {
		if err := ownedAmendmentDirectory(runs); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(runs, ticket[:len(ticket)-3])); !os.IsNotExist(err) {
			return errors.New("dependency amendment refuses existing ticket run files")
		}
	}
	derived := filepath.Join(directory, "derived")
	if _, err := os.Lstat(derived); !os.IsNotExist(err) {
		if err := ownedAmendmentDirectory(derived); err != nil {
			return err
		}
		entries, err := os.ReadDir(derived)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return errors.New("dependency amendment refuses retained derived-base preparation")
		}
	}
	return nil
}

func completeAmendmentPlan(plan Plan) ([]byte, error) {
	metadata := sidecar{Version: 2}
	metadata.Tickets = map[string]struct {
		DependsOn []string `json:"depends_on"`
		Priority  int      `json:"priority"`
		Touches   []string `json:"touches"`
	}{}
	for _, t := range plan.Tickets {
		metadata.Tickets[t.File] = struct {
			DependsOn []string `json:"depends_on"`
			Priority  int      `json:"priority"`
			Touches   []string `json:"touches"`
		}{t.DependsOn, t.Priority, t.Touches}
	}
	data, err := json.Marshal(metadata)
	if err != nil || len(data)+1 > maximumMetadata {
		return nil, errors.New("amended definition exceeds 64 KiB")
	}
	return append(data, '\n'), nil
}
func amendedState(old State, ticket string, dependencies []string, at time.Time) (State, error) {
	next := old
	next.Plan.Tickets = append([]Ticket(nil), old.Plan.Tickets...)
	next.DependencyAmendments = append([]DependencyAmendmentAudit(nil), old.DependencyAmendments...)
	var from []string
	found := false
	for i := range next.Plan.Tickets {
		if next.Plan.Tickets[i].File == ticket {
			from = exactStrings(next.Plan.Tickets[i].DependsOn)
			next.Plan.Tickets[i].DependsOn = exactStrings(dependencies)
			found = true
		}
	}
	if !found {
		return State{}, errors.New("unknown dependency amendment ticket")
	}
	if err := validatePlan(&next.Plan); err != nil {
		return State{}, err
	}
	next.Plan.DefinitionSHA = definitionSHA(next.Plan)
	if old.Plan.DefinitionSHA == next.Plan.DefinitionSHA {
		return old, nil
	}
	next.UpdatedAt = at
	next.DependencyAmendments = append(next.DependencyAmendments, DependencyAmendmentAudit{Ticket: ticket, From: from, To: exactStrings(dependencies), Before: old.Plan.DefinitionSHA, After: next.Plan.DefinitionSHA, At: at})
	if err := validateState(&next); err != nil {
		return State{}, err
	}
	return next, nil
}
func validateAmendmentTransaction(tx amendmentTransaction, root, reference string) (State, State, error) {
	var old, next State
	if tx.Version != 1 || len(tx.OldJournal) > maxJournal || len(tx.NewJournal) > maxJournal || len(tx.OldPlan) > maximumMetadata || len(tx.NewPlan) > maximumMetadata || amendmentHash(tx.OldJournal) != tx.OldJournalHash || amendmentHash(tx.NewJournal) != tx.NewJournalHash || amendmentHash(tx.OldPlan) != tx.OldPlanHash || amendmentHash(tx.NewPlan) != tx.NewPlanHash || amendmentJSON(tx.OldJournal, &old) != nil || amendmentJSON(tx.NewJournal, &next) != nil || validateState(&old) != nil || validateState(&next) != nil || old.Plan.Root != root || old.Plan.Reference != reference || len(next.DependencyAmendments) != len(old.DependencyAmendments)+1 {
		return old, next, errors.New("invalid pending dependency amendment")
	}
	at := next.DependencyAmendments[len(next.DependencyAmendments)-1].At
	expected, err := amendedState(old, tx.Ticket, tx.Dependencies, at)
	if err != nil || !reflect.DeepEqual(expected, next) {
		return old, next, errors.New("pending dependency amendment changed frozen state")
	}
	definition, err := completeAmendmentPlan(expected.Plan)
	if err != nil || !bytes.Equal(definition, tx.NewPlan) {
		return old, next, errors.New("pending dependency amendment definition changed")
	}
	if !tx.OldPlanPresent && len(tx.OldPlan) != 0 {
		return old, next, errors.New("invalid absent original definition")
	}
	original, err := amendmentOriginalPlan(old.Plan, tx.OldPlan, tx.OldPlanPresent)
	if err != nil || !reflect.DeepEqual(original, old.Plan) {
		return old, next, errors.New("original amendment definition differs from saved plan")
	}
	return old, next, nil
}

func amendmentOriginalPlan(saved Plan, data []byte, present bool) (Plan, error) {
	p := saved
	p.Tickets = []Ticket{}
	var metadata *sidecar
	var err error
	if present {
		metadata, err = decodeSidecar(data)
		if err != nil {
			return p, err
		}
	}
	known := ticketMap(saved.Tickets)
	if metadata != nil {
		for name := range metadata.Tickets {
			if _, ok := known[name]; !ok {
				return p, errors.New("original definition names unknown ticket")
			}
		}
	}
	for _, ticket := range saved.Tickets {
		t := Ticket{File: ticket.File, DependsOn: []string{}, Touches: []string{}}
		if metadata != nil {
			if item, ok := metadata.Tickets[t.File]; ok {
				t.Priority = item.Priority
				t.DependsOn = append([]string(nil), item.DependsOn...)
				t.Touches = append([]string(nil), item.Touches...)
				if metadata.Version == 2 {
					t.DependsOn, t.Touches = exactStrings(item.DependsOn), exactStrings(item.Touches)
				}
			}
		}
		p.Tickets = append(p.Tickets, t)
	}
	if err := validatePlan(&p); err != nil {
		return p, err
	}
	p.DefinitionSHA = definitionSHA(p)
	return p, nil
}

// CheckDependencyAmendment fences other feature operations until both files
// have been durably saved by the authorized recovery command.
func CheckDependencyAmendment(root, reference string) error {
	if err := CheckTicketCompletion(root, reference); err != nil {
		return err
	}
	return checkDependencyOnly(root, reference)
}
func checkDependencyOnly(root, reference string) error {
	path := filepath.Join(root, ".sdlc", "work", reference, "series", amendmentPending)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("dependency amendment is pending; repeat its dependencies command before running tickets")
	}
	return nil
}

// OwnTicketPreparation serializes direct creation of a ticket run with an
// amendment. The caller releases ownership after saving the new run journal.
func OwnTicketPreparation(root, reference string) (*os.File, error) {
	series := filepath.Join(root, ".sdlc", "work", reference, "series")
	if _, err := os.Lstat(series); os.IsNotExist(err) {
		return nil, nil
	}
	directory, _, err := amendmentPaths(root, reference)
	if err != nil {
		return nil, err
	}
	lock, err := ownAmendmentFeature(directory)
	if err != nil {
		return nil, err
	}
	if _, err := Load(directory); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}
func ownAmendmentFeature(directory string) (*os.File, error) {
	path := filepath.Join(directory, "series.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !amendmentOwned(info) || info.Mode().Perm() != 0600 {
		return nil, errors.New("dependency amendment requires the owned saved feature lock")
	}
	lock, err := filelock.AcquireExisting(path)
	if err != nil {
		return nil, fmt.Errorf("cannot own feature for dependency amendment: %w", err)
	}
	current, err := os.Lstat(path)
	opened, openedErr := lock.Stat()
	if err != nil || openedErr != nil || !os.SameFile(info, opened) || !os.SameFile(current, opened) {
		lock.Close()
		return nil, errors.New("feature lock changed while acquiring ownership")
	}
	return lock, nil
}

// AmendDependencies is an offline operation; validate checks frozen local inputs
// and settings without obtaining any account, runtime or provider lease.
func AmendDependencies(ctx context.Context, root, reference, ticket string, dependencies []string, dryRun bool, validate func(State) error) (DependencyAmendmentPreview, error) {
	preview := DependencyAmendmentPreview{Ticket: ticket, To: exactStrings(dependencies), DryRun: dryRun}
	if err := ctx.Err(); err != nil {
		return preview, err
	}
	directory, planPath, err := amendmentPaths(root, reference)
	if err != nil {
		return preview, err
	}
	lock, err := ownAmendmentFeature(directory)
	if err != nil {
		return preview, fmt.Errorf("cannot own feature for dependency amendment: %w", err)
	}
	defer lock.Close()
	pendingPath := filepath.Join(directory, amendmentPending)
	pending, recovery, err := readAmendmentFile(pendingPath, maximumAmendment, true)
	if err != nil {
		return preview, err
	}
	var tx amendmentTransaction
	var old, next State
	if recovery {
		if amendmentJSON(pending, &tx) != nil || tx.Ticket != ticket || !reflect.DeepEqual(tx.Dependencies, dependencies) {
			return preview, errors.New("repeat the exact pending dependencies command to recover")
		}
		old, next, err = validateAmendmentTransaction(tx, root, reference)
		if err != nil {
			return preview, err
		}
		current, err := Discover(ctx, root, reference, old.Plan.Base)
		if err != nil || (!reflect.DeepEqual(current, old.Plan) && !reflect.DeepEqual(current, next.Plan)) {
			return preview, errors.New("feature definition changed during amendment recovery")
		}
	} else {
		journal, _, err := readAmendmentFile(filepath.Join(directory, "journal.json"), maxJournal, false)
		if err != nil {
			return preview, err
		}
		if amendmentJSON(journal, &old) != nil || validateState(&old) != nil || old.Plan.Root != root || old.Plan.Reference != reference {
			return preview, errors.New("invalid saved feature for dependency amendment")
		}
		current, err := Discover(ctx, root, reference, old.Plan.Base)
		if err != nil || !reflect.DeepEqual(current, old.Plan) {
			return preview, errors.New("current feature definition changed; dependency amendment refused")
		}
		plan, present, err := readAmendmentFile(planPath, maximumMetadata, true)
		if err != nil {
			return preview, err
		}
		next, err = amendedState(old, ticket, dependencies, time.Now().UTC())
		if err != nil {
			return preview, err
		}
		newPlan, err := completeAmendmentPlan(next.Plan)
		if err != nil {
			return preview, err
		}
		newJournal, err := json.Marshal(next)
		if err != nil || len(newJournal)+1 > maxJournal {
			return preview, errors.New("amended checkpoint exceeds 1 MiB")
		}
		newJournal = append(newJournal, '\n')
		tx = amendmentTransaction{Version: 1, Ticket: ticket, Dependencies: exactStrings(dependencies), OldJournal: journal, NewJournal: newJournal, OldPlan: plan, NewPlan: newPlan, OldPlanPresent: present, OldJournalHash: amendmentHash(journal), NewJournalHash: amendmentHash(newJournal), OldPlanHash: amendmentHash(plan), NewPlanHash: amendmentHash(newPlan)}
	}
	if err := amendmentUnstarted(directory, old, ticket); err != nil {
		return preview, err
	}
	if validate == nil {
		return preview, errors.New("dependency amendment requires frozen input/settings validation")
	}
	if err := validate(old); err != nil {
		return preview, err
	}
	preview.Before, preview.After, preview.Recovery = old.Plan.DefinitionSHA, next.Plan.DefinitionSHA, recovery
	preview.Changed = preview.Before != preview.After
	preview.From = exactStrings(ticketMap(old.Plan.Tickets)[ticket].DependsOn)
	if err := verifyAmendmentFiles(directory, planPath, tx); err != nil {
		return preview, err
	}
	if dryRun || !preview.Changed {
		return preview, nil
	}
	if _, _, err := validateAmendmentTransaction(tx, root, reference); err != nil {
		return preview, err
	}
	if !recovery {
		data, err := json.Marshal(tx)
		if err != nil || len(data) > maximumAmendment {
			return preview, errors.New("dependency amendment transaction exceeds bound")
		}
		if err := amendmentWrite(pendingPath, data); err != nil {
			return preview, err
		}
	}
	if err := ctx.Err(); err != nil {
		return preview, err
	}
	if err := verifyAmendmentFiles(directory, planPath, tx); err != nil {
		return preview, err
	}
	if err := validate(old); err != nil {
		return preview, err
	}
	if err := amendmentWrite(planPath, tx.NewPlan); err != nil {
		return preview, err
	}
	if err := ctx.Err(); err != nil {
		return preview, err
	}
	if err := verifyAmendmentFiles(directory, planPath, tx); err != nil {
		return preview, err
	}
	if err := validate(old); err != nil {
		return preview, err
	}
	if err := amendmentWrite(filepath.Join(directory, "journal.json"), tx.NewJournal); err != nil {
		return preview, err
	}
	if err := verifyAmendmentFiles(directory, planPath, tx); err != nil {
		return preview, err
	}
	if err := amendmentUnstarted(directory, old, ticket); err != nil {
		return preview, err
	}
	if err := validate(old); err != nil {
		return preview, err
	}
	if err := os.Remove(pendingPath); err != nil {
		return preview, err
	}
	return preview, syncAmendmentDirectory(directory)
}
func verifyAmendmentFiles(directory, planPath string, tx amendmentTransaction) error {
	journal, _, err := readAmendmentFile(filepath.Join(directory, "journal.json"), maxJournal, false)
	if err != nil {
		return err
	}
	if !bytes.Equal(journal, tx.OldJournal) && !bytes.Equal(journal, tx.NewJournal) {
		return errors.New("feature checkpoint changed during dependency amendment")
	}
	plan, present, err := readAmendmentFile(planPath, maximumMetadata, true)
	if err != nil {
		return err
	}
	if !(present == tx.OldPlanPresent && bytes.Equal(plan, tx.OldPlan)) && !(present && bytes.Equal(plan, tx.NewPlan)) {
		return errors.New("feature definition changed during dependency amendment")
	}
	return nil
}

var amendmentWrite = writeAmendmentFile

func writeAmendmentFile(path string, data []byte) error {
	if err := ownedAmendmentDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || !amendmentOwned(info) || info.Mode().Perm() != 0600 {
			return errors.New("unsafe dependency amendment destination")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dependency-amendment-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncAmendmentDirectory(filepath.Dir(path))
}
func syncAmendmentDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
