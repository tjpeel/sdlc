package workseries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tjpeel/sdlc/internal/filelock"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const completionPending = "ticket-completion.pending.json"

// MergedTicket is remote evidence verified by the controller, never a model claim.
type MergedTicket struct {
	Repository     string    `json:"repository"`
	Number         int       `json:"number"`
	URL            string    `json:"url"`
	Branch         string    `json:"branch"`
	Base           string    `json:"base"`
	IntegrationSHA string    `json:"integration_sha"`
	HeadSHA        string    `json:"head_sha"`
	MergeSHA       string    `json:"merge_sha"`
	MergedAt       time.Time `json:"merged_at"`
}
type TicketCompletionAudit struct {
	Ticket   string       `json:"ticket"`
	Evidence MergedTicket `json:"evidence"`
	Previous Result       `json:"previous"`
	Before   string       `json:"before_sha256"`
	After    string       `json:"after_sha256"`
	At       time.Time    `json:"at"`
}
type CompletionPreview struct {
	Ticket   string       `json:"ticket"`
	Evidence MergedTicket `json:"evidence"`
	Changed  bool         `json:"changed"`
	DryRun   bool         `json:"dry_run"`
	Recovery bool         `json:"recovery"`
}
type completionTransaction struct {
	Ticket                                       string
	Evidence                                     MergedTicket
	OldJournal, NewJournal, OldTicket, NewTicket []byte
}

var ticketStatusLine = regexp.MustCompile(`(?m)^\*\*Status:\*\*[^\r\n]*`)
var ticketBlockerLine = regexp.MustCompile(`(?m)^\*\*Blocked by:\*\*[^\r\n]*`)

// TicketMetadata is descriptive. The journal and declared dependency graph own scheduling.
func TicketMetadata(data []byte) (status, blockers string) {
	status = strings.TrimSpace(strings.TrimPrefix(string(ticketStatusLine.Find(data)), "**Status:**"))
	blockers = strings.TrimSpace(strings.TrimPrefix(string(ticketBlockerLine.Find(data)), "**Blocked by:**"))
	return
}
func completedTicketText(data []byte, url string) []byte {
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	for _, item := range []struct {
		re   *regexp.Regexp
		text string
	}{
		{ticketStatusLine, "**Status:** completed"},
		{ticketBlockerLine, "**Blocked by:** None (completed; merged PR: " + url + ")"},
	} {
		if item.re.Match(data) {
			data = item.re.ReplaceAllLiteral(data, []byte(item.text))
		} else {
			data = append(data, []byte(newline+item.text+newline)...)
		}
	}
	return data
}
func CheckTicketCompletion(root, reference string) error {
	path := filepath.Join(root, ".sdlc", "work", reference, "series", completionPending)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("ticket completion pending; repeat ticket complete before running tickets")
	}
	return nil
}
func validateMergedTicket(e MergedTicket) error {
	if e.Repository == "" || e.Number <= 0 || e.URL != fmt.Sprintf("https://github.com/%s/pull/%d", e.Repository, e.Number) || e.Base == "" || e.Branch == "" || !objectSHA(e.IntegrationSHA) || !objectSHA(e.HeadSHA) || !objectSHA(e.MergeSHA) || e.MergedAt.IsZero() {
		return errors.New("complete merged PR evidence is required")
	}
	return nil
}
func completionState(old State, ticket string, e MergedTicket, before, after []byte) (State, error) {
	// Round trip isolates maps and opaque frozen settings from the caller.
	data, err := json.Marshal(old)
	if err != nil {
		return State{}, err
	}
	var next State
	if err = json.Unmarshal(data, &next); err != nil {
		return State{}, err
	}
	found := false
	for _, t := range next.Plan.Tickets {
		if t.File == ticket {
			found = true
		}
	}
	if !found {
		return State{}, errors.New("unknown ticket")
	}
	if e.Base != old.Plan.Base {
		return State{}, errors.New("merged PR targets a different feature base")
	}
	previous := next.Results[ticket]
	if previous.State == "merged" && previous.PRNumber != 0 && (previous.PRNumber != e.Number || previous.HeadSHA != e.HeadSHA || previous.MergeSHA != e.MergeSHA) {
		return State{}, errors.New("ticket already records a different merged PR")
	}
	// No descendant can already have frozen an input which this operation changes.
	for file, r := range next.Results {
		if file != ticket && r.State != "merged" && r.RunID != "" {
			return State{}, fmt.Errorf("ticket %s has retained work; completion would change frozen inputs", file)
		}
	}
	relative := filepath.ToSlash(filepath.Join(".sdlc", "work", old.Plan.Reference, "tickets", ticket))
	var settings map[string]json.RawMessage
	if len(next.Settings) > 0 {
		if err = json.Unmarshal(next.Settings, &settings); err != nil {
			return State{}, err
		}
		var repository string
		if raw := settings["Repository"]; len(raw) > 0 {
			if json.Unmarshal(raw, &repository) != nil {
				return State{}, errors.New("invalid frozen repository")
			}
		}
		if repository != "" && !strings.EqualFold(repository, e.Repository) {
			return State{}, errors.New("merged PR belongs to a different repository")
		}
		if raw := settings["InputHashes"]; len(raw) > 0 {
			var hashes map[string]string
			if err = json.Unmarshal(raw, &hashes); err != nil {
				return State{}, err
			}
			if hash, ok := hashes[relative]; ok {
				if hash != amendmentHash(before) {
					return State{}, errors.New("ticket content changed outside frozen inputs; reconcile it before completion")
				}
				hashes[relative] = amendmentHash(after)
				settings["InputHashes"], _ = json.Marshal(hashes)
			}
		}
		next.Settings, err = json.Marshal(settings)
		if err != nil {
			return State{}, err
		}
	}
	if next.Results == nil {
		next.Results = map[string]Result{}
	}
	// Old native runs remain intact; externally completed work has no invented run identity.
	next.Results[ticket] = Result{State: "merged", Branch: e.Branch, Base: e.Base, BaseSHA: e.IntegrationSHA, HeadSHA: e.HeadSHA, MergeSHA: e.MergeSHA, URL: e.URL, PRNumber: e.Number}
	next.TicketCompletions = append(next.TicketCompletions, TicketCompletionAudit{Ticket: ticket, Evidence: e, Previous: previous, Before: amendmentHash(before), After: amendmentHash(after), At: time.Now().UTC()})
	next.UpdatedAt = time.Now().UTC()
	return next, validateState(&next)
}

// CompleteTicket serializes completion with feature execution and direct ticket preparation.
// A durable transaction lets the exact command recover either interrupted rename.
func CompleteTicket(ctx context.Context, root, reference, ticket string, e MergedTicket, dryRun bool) (CompletionPreview, error) {
	preview := CompletionPreview{Ticket: ticket, Evidence: e, DryRun: dryRun}
	if err := ctx.Err(); err != nil {
		return preview, err
	}
	if err := validateMergedTicket(e); err != nil {
		return preview, err
	}
	if filepath.Base(ticket) != ticket || !strings.HasSuffix(ticket, ".md") {
		return preview, errors.New("ticket must be an exact numbered filename")
	}
	dir, _, err := amendmentPaths(root, reference)
	if err != nil {
		return preview, err
	}
	lock, err := ownAmendmentFeature(dir)
	if err != nil {
		return preview, err
	}
	defer lock.Close()
	if err = checkDependencyOnly(root, reference); err != nil {
		return preview, err
	}
	runLocks := []*os.File{}
	defer func() {
		for _, l := range runLocks {
			l.Close()
		}
	}()
	lockRuns := func(state State) error {
		for _, r := range state.Results {
			if r.Directory == "" || r.State == "merged" {
				continue
			}
			if err := ownedAmendmentDirectory(r.Directory); err != nil {
				return err
			}
			lockPath := filepath.Join(r.Directory, "run.lock")
			info, statErr := os.Lstat(lockPath)
			if statErr == nil && (!info.Mode().IsRegular() || !amendmentOwned(info) || info.Mode().Perm() != 0600) {
				return errors.New("unsafe saved run lock")
			}
			if statErr != nil && !os.IsNotExist(statErr) {
				return statErr
			}
			l, err := filelock.Acquire(lockPath)
			if err == nil {
				current, currentErr := os.Lstat(lockPath)
				opened, openedErr := l.Stat()
				if currentErr != nil || openedErr != nil || !current.Mode().IsRegular() || !os.SameFile(current, opened) || (info != nil && !os.SameFile(info, opened)) {
					l.Close()
					return errors.New("run lock changed while acquiring ownership")
				}
			}
			if err != nil {
				return fmt.Errorf("ticket run is active; stop it before completion: %w", err)
			}
			runLocks = append(runLocks, l)
		}
		return nil
	}
	pending := filepath.Join(dir, completionPending)
	data, recovery, err := readAmendmentFile(pending, maximumAmendment, true)
	if err != nil {
		return preview, err
	}
	path := filepath.Join(root, ".sdlc", "work", reference, "tickets", ticket)
	// Validate every directory component and reject symlink ticket paths.
	if err = ownedAmendmentDirectory(filepath.Dir(path)); err != nil {
		return preview, err
	}
	current, err := readCompletionTicket(path)
	if err != nil {
		return preview, err
	}
	journalPath := filepath.Join(dir, "journal.json")
	journal, _, err := readAmendmentFile(journalPath, maxJournal, false)
	if err != nil {
		return preview, err
	}
	var tx completionTransaction
	if recovery {
		if amendmentJSON(data, &tx) != nil || tx.Ticket != ticket || !sameCompletionEvidence(tx.Evidence, e) {
			return preview, errors.New("different completion is pending; repeat its original command")
		}
		e = tx.Evidence
		preview.Evidence = e
		var old, next State
		if amendmentJSON(tx.OldJournal, &old) != nil || amendmentJSON(tx.NewJournal, &next) != nil || validateState(&old) != nil || validateState(&next) != nil || old.Plan.Root != root || old.Plan.Reference != reference || !reflect.DeepEqual(old.Plan, next.Plan) || !bytes.Equal(tx.NewTicket, completedTicketText(tx.OldTicket, e.URL)) {
			return preview, errors.New("invalid completion transaction")
		}
		if err = lockRuns(old); err != nil {
			return preview, err
		}
		expected, err := completionState(old, ticket, e, tx.OldTicket, tx.NewTicket)
		if err != nil {
			return preview, err
		}
		// Timestamps are recorded in the transaction, not regenerated during recovery.
		expected.UpdatedAt = next.UpdatedAt
		expected.TicketCompletions[len(expected.TicketCompletions)-1].At = next.TicketCompletions[len(next.TicketCompletions)-1].At
		if !reflect.DeepEqual(expected, next) {
			return preview, errors.New("completion transaction changed unrelated state")
		}
		if (!bytes.Equal(journal, tx.OldJournal) && !bytes.Equal(journal, tx.NewJournal)) || (!bytes.Equal(current, tx.OldTicket) && !bytes.Equal(current, tx.NewTicket)) {
			return preview, errors.New("completion inputs changed during recovery")
		}
	} else {
		old, err := Load(dir)
		if err != nil {
			return preview, err
		}
		if old.Plan.Root != root || old.Plan.Reference != reference {
			return preview, errors.New("feature identity mismatch")
		}
		if err = lockRuns(old); err != nil {
			return preview, err
		}
		discovered, err := Discover(ctx, root, reference, old.Plan.Base)
		if err != nil || !reflect.DeepEqual(discovered, old.Plan) {
			return preview, errors.New("feature plan changed; completion refused")
		}
		after := completedTicketText(current, e.URL)
		r := old.Results[ticket]
		// A repeated operation still must not hide a drifted frozen ticket hash.
		var settings struct{ InputHashes map[string]string }
		if len(old.Settings) > 0 && json.Unmarshal(old.Settings, &settings) != nil {
			return preview, errors.New("invalid frozen settings")
		}
		relative := filepath.ToSlash(filepath.Join(".sdlc", "work", reference, "tickets", ticket))
		if hash, ok := settings.InputHashes[relative]; ok && hash != amendmentHash(current) {
			return preview, errors.New("ticket content changed outside frozen inputs; reconcile it before completion")
		}
		if r.State == "merged" && r.PRNumber == e.Number && r.HeadSHA == e.HeadSHA && r.MergeSHA == e.MergeSHA && bytes.Equal(current, after) {
			preview.Evidence.IntegrationSHA = r.BaseSHA
			return preview, nil
		}
		next, err := completionState(old, ticket, e, current, after)
		if err != nil {
			return preview, err
		}
		nextData, err := json.Marshal(next)
		if err != nil {
			return preview, err
		}
		if len(nextData)+1 > maxJournal {
			return preview, errors.New("completion journal exceeds supported journal size")
		}
		tx = completionTransaction{Ticket: ticket, Evidence: e, OldJournal: journal, NewJournal: append(nextData, '\n'), OldTicket: current, NewTicket: after}
	}
	preview.Changed = true
	preview.Recovery = recovery
	if dryRun {
		return preview, nil
	}
	if !recovery {
		data, err = json.Marshal(tx)
		if err != nil {
			return preview, err
		}
		if len(data) > maximumAmendment {
			return preview, errors.New("completion transaction exceeds supported size")
		}
		if err = writeAmendmentFile(pending, data); err != nil {
			return preview, err
		}
	}
	if err = completionTicketWrite(path, tx.NewTicket); err != nil {
		return preview, err
	}
	if err = completionWrite(journalPath, tx.NewJournal); err != nil {
		return preview, err
	}
	if err = os.Remove(pending); err != nil {
		return preview, err
	}
	return preview, syncAmendmentDirectory(dir)
}

// ReadTicketMetadata rejects redirected private work paths before reading metadata.
func ReadTicketMetadata(root, reference, ticket string) ([]byte, error) {
	if !safeReference(reference) || filepath.Base(ticket) != ticket {
		return nil, errors.New("invalid ticket identity")
	}
	current := root
	if err := realDir(current); err != nil {
		return nil, err
	}
	for _, part := range []string{".sdlc", "work", reference, "tickets"} {
		current = filepath.Join(current, part)
		if err := ownedAmendmentDirectory(current); err != nil {
			return nil, err
		}
	}
	return readCompletionTicket(filepath.Join(current, ticket))
}

func readCompletionTicket(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !amendmentOwned(info) || info.Size() > maximumAmendment {
		return nil, errors.New("ticket must be an owned regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("ticket changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maximumAmendment+1))
	if err != nil {
		return nil, err
	}
	if len(data) != int(info.Size()) || len(data) > maximumAmendment {
		return nil, errors.New("ticket changed while reading")
	}
	return data, nil
}

var completionWrite = writeAmendmentFile
var completionTicketWrite = writeCompletionTicket

func writeCompletionTicket(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !amendmentOwned(info) {
		return errors.New("unsafe ticket destination")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ticket-completion-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(info.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncAmendmentDirectory(filepath.Dir(path))
}

func sameCompletionEvidence(a, b MergedTicket) bool {
	a.IntegrationSHA = ""
	b.IntegrationSHA = ""
	return reflect.DeepEqual(a, b)
}

func validateCompletionAudit(s *State) error {
	known := ticketMap(s.Plan.Tickets)
	for _, a := range s.TicketCompletions {
		t, ok := known[a.Ticket]
		if !ok || validateMergedTicket(a.Evidence) != nil || !validResult(s.Plan, t, a.Previous) || !validAmendmentHash(a.Before) || !validAmendmentHash(a.After) || a.At.IsZero() {
			return errors.New("invalid ticket completion audit")
		}
	}
	return nil
}
