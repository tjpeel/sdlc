package workseries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/featureguard"
	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const maxRuntimeMigrationTransaction = 64 * 1024 * 1024

type RuntimeMigrationEntry struct {
	Ticket  string `json:"ticket"`
	RunID   string `json:"run_id"`
	Old     []byte `json:"old"`
	New     []byte `json:"new"`
	OldHash string `json:"old_hash"`
	NewHash string `json:"new_hash"`
}

type runtimeMigrationTransaction struct {
	Version        int                     `json:"version"`
	ID             string                  `json:"id"`
	Root           string                  `json:"root"`
	Reference      string                  `json:"reference"`
	TargetName     string                  `json:"target_name"`
	TargetImage    string                  `json:"target_image"`
	Target         runtimeimage.State      `json:"target"`
	OldFeature     []byte                  `json:"old_feature"`
	NewFeature     []byte                  `json:"new_feature"`
	OldFeatureHash string                  `json:"old_feature_hash"`
	NewFeatureHash string                  `json:"new_feature_hash"`
	Entries        []RuntimeMigrationEntry `json:"entries"`
}

type RuntimeMigrationPreview struct {
	Reference   string                `json:"reference"`
	TargetName  string                `json:"target_name"`
	TargetImage string                `json:"target_image"`
	Runs        []RuntimeMigrationRun `json:"runs"`
	Recovery    bool                  `json:"recovery"`
	Rollback    bool                  `json:"rollback"`
	DryRun      bool                  `json:"dry_run"`
}

type RuntimeMigrationRun struct {
	Ticket          string              `json:"ticket"`
	RunID           string              `json:"run_id"`
	Publication     workrun.Publication `json:"publication"`
	ResumeState     string              `json:"resume_state"`
	PreviousRuntime string              `json:"previous_runtime"`
	ProviderRoute   string              `json:"provider_route"`
	ResolvedSDK     string              `json:"resolved_sdk,omitempty"`
}

func runtimeMigrationRunPreview(e RuntimeMigrationEntry, j workrun.Journal) RuntimeMigrationRun {
	route := "direct"
	if j.Plan.Headroom.Enabled() {
		route = j.Plan.Headroom.Mode
	}
	previous := j.Plan.RuntimeName
	if previous == "" {
		previous = "local"
	}
	return RuntimeMigrationRun{Ticket: e.Ticket, RunID: e.RunID, Publication: j.Publication, ResumeState: "runtime_recheck", PreviousRuntime: previous, ProviderRoute: route}
}

// PendingFeatureRuntimeTarget reads the recorded target for rollback preview.
// The transaction is validated again under all locks before any write.
func PendingFeatureRuntimeTarget(root, reference string) (runtimeimage.State, error) {
	dir, _, err := amendmentPaths(root, reference)
	if err != nil {
		return runtimeimage.State{}, err
	}
	data, present, err := readAmendmentFile(filepath.Join(dir, featureguard.RuntimeMigrationPending), maxRuntimeMigrationTransaction, true)
	if err != nil {
		return runtimeimage.State{}, err
	}
	if !present {
		return runtimeimage.State{}, errors.New("no pending feature runtime migration")
	}
	var tx runtimeMigrationTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&tx) != nil || decoder.Decode(new(any)) != io.EOF || tx.Root != root || tx.Reference != reference || tx.TargetName != tx.Target.Name || tx.TargetImage != tx.Target.ImageID {
		return runtimeimage.State{}, errors.New("invalid pending feature runtime migration")
	}
	return tx.Target, nil
}

// RuntimeMigrationVerify validates the frozen feature settings, inputs, target
// runtime and exact PR boundaries. It must be read-only and must not start work.
type RuntimeMigrationVerify func(context.Context, State, []workrun.Journal) error

func decodeRuntimeFeature(data []byte) (State, error) {
	var s State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || decoder.Decode(new(any)) != io.EOF || validateState(&s) != nil {
		return State{}, errors.New("invalid runtime migration feature checkpoint")
	}
	return s, nil
}

func runtimeSettings(data []byte, name, image string, entries []RuntimeMigrationEntry, journals []workrun.Journal) (json.RawMessage, string, string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, "", "", errors.New("invalid frozen feature settings")
	}
	var oldName, oldImage string
	if raw := fields["runtime_name"]; len(raw) > 0 && json.Unmarshal(raw, &oldName) != nil {
		return nil, "", "", errors.New("invalid frozen runtime name")
	}
	if json.Unmarshal(fields["ImageID"], &oldImage) != nil || oldImage == "" {
		return nil, "", "", errors.New("missing frozen runtime image")
	}
	if name == "" {
		delete(fields, "runtime_name")
	} else {
		fields["runtime_name"], _ = json.Marshal(name)
	}
	fields["ImageID"], _ = json.Marshal(image)
	var featureHeadroom headroom.Config
	if raw := fields["Headroom"]; len(raw) > 0 && json.Unmarshal(raw, &featureHeadroom) != nil {
		return nil, "", "", errors.New("invalid frozen feature provider route")
	}
	if err := featureHeadroom.Validate(); err != nil {
		return nil, "", "", err
	}
	overrides := map[string]headroom.Config{}
	if raw := fields["TicketHeadroom"]; len(raw) > 0 && json.Unmarshal(raw, &overrides) != nil {
		return nil, "", "", errors.New("invalid ticket provider route exceptions")
	}
	if overrides == nil {
		overrides = map[string]headroom.Config{}
	}
	if len(entries) != len(journals) {
		return nil, "", "", errors.New("migration run set changed")
	}
	for i, e := range entries {
		route := journals[i].Plan.Headroom
		if err := route.Validate(); err != nil {
			return nil, "", "", err
		}
		if existing, ok := overrides[e.Ticket]; ok && existing != route {
			return nil, "", "", fmt.Errorf("ticket %s provider route differs from its recorded exception", e.Ticket)
		}
		if route != featureHeadroom {
			overrides[e.Ticket] = route
		}
	}
	if len(overrides) > 0 {
		fields["TicketHeadroom"], _ = json.Marshal(overrides)
	}
	result, err := json.Marshal(fields)
	return result, oldName, oldImage, err
}

func runtimeRunDir(root, reference string, e RuntimeMigrationEntry) (string, error) {
	if filepath.Base(e.Ticket) != e.Ticket || !strings.HasSuffix(e.Ticket, ".md") || !runIDPattern(e.RunID) {
		return "", errors.New("invalid migration run identity")
	}
	return workrun.RunDirectory(root, reference, e.Ticket, e.RunID, false)
}

func ownRuntimeRun(directory string) (*os.File, error) {
	path := filepath.Join(directory, "run.lock")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || !amendmentOwned(before) || before.Mode().Perm() != 0600 {
		return nil, errors.New("migration requires an owned saved run lock")
	}
	lock, err := filelock.AcquireExisting(path)
	if err != nil {
		return nil, fmt.Errorf("cannot own stopped run: %w", err)
	}
	current, err := os.Lstat(path)
	opened, openErr := lock.Stat()
	if err != nil || openErr != nil || !os.SameFile(before, current) || !os.SameFile(before, opened) {
		lock.Close()
		return nil, errors.New("run lock changed while acquiring ownership")
	}
	return lock, nil
}

func migrationEntries(state State) ([]RuntimeMigrationEntry, error) {
	entries := []RuntimeMigrationEntry{}
	for _, ticket := range state.Plan.Tickets {
		key := filepath.Base(ticket.File)
		r, exists := state.Results[key]
		if !exists {
			continue
		}
		if r.RunID == "" {
			return nil, fmt.Errorf("ticket %s has no saved run identity", key)
		}
		entries = append(entries, RuntimeMigrationEntry{Ticket: key, RunID: r.RunID})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ticket < entries[j].Ticket })
	if len(entries) == 0 {
		return nil, errors.New("feature has no started ticket runs")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.RunID] {
			return nil, errors.New("feature has duplicate run identities")
		}
		seen[e.RunID] = true
	}
	return entries, nil
}

func verifyRuntimeRunInventory(root, reference string, entries []RuntimeMigrationEntry) error {
	base := filepath.Join(root, ".sdlc", "work", reference, "runs")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, e := range entries {
		expected[strings.TrimSuffix(e.Ticket, ".md")+"/"+e.RunID] = true
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe feature run inventory")
		}
		runs, err := os.ReadDir(filepath.Join(base, d.Name()))
		if err != nil {
			return err
		}
		for _, run := range runs {
			if !run.IsDir() || run.Type()&os.ModeSymlink != 0 || !expected[d.Name()+"/"+run.Name()] {
				return errors.New("feature has unrecorded or unsafe run data; reconcile it before migration")
			}
		}
	}
	return nil
}

func acquireRuntimeRuns(root, reference string, entries []RuntimeMigrationEntry) ([]*os.File, error) {
	locks := []*os.File{}
	for _, e := range entries {
		dir, err := runtimeRunDir(root, reference, e)
		if err == nil {
			var lock *os.File
			lock, err = ownRuntimeRun(dir)
			if err == nil {
				locks = append(locks, lock)
			}
		}
		if err != nil {
			for i := len(locks) - 1; i >= 0; i-- {
				locks[i].Close()
			}
			return nil, err
		}
	}
	return locks, nil
}

func releaseRuntimeRuns(locks []*os.File) {
	for i := len(locks) - 1; i >= 0; i-- {
		locks[i].Close()
	}
}

func readRuntimeJournal(path string) ([]byte, error) {
	data, present, err := readAmendmentFile(path, 1024*1024, false)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("runtime migration journal missing")
	}
	return data, nil
}

func runtimeOriginalJournals(root, reference string, entries []RuntimeMigrationEntry) ([]workrun.Journal, error) {
	result := make([]workrun.Journal, 0, len(entries))
	for _, e := range entries {
		dir, err := runtimeRunDir(root, reference, e)
		if err != nil {
			return nil, err
		}
		j, err := workrun.Load(dir)
		if err != nil {
			return nil, err
		}
		if j.ID != e.RunID || j.Plan.Root != root || j.Plan.Reference != reference || filepath.Base(j.Plan.Ticket) != e.Ticket {
			return nil, errors.New("saved run does not match its feature identity")
		}
		result = append(result, j)
	}
	return result, nil
}

func runtimePreparedJournal(j workrun.Journal, target runtimeimage.State) ([]byte, error) {
	next, err := workrun.PreparePublishedFeatureRuntimeMigration(j, target.Name, target)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "sdlc-runtime-migration-validate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return nil, err
	}
	if err := workrun.Save(scratch, &next); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(scratch, "journal.json"))
}

func decodeRuntimeJournal(data []byte, dir string, e RuntimeMigrationEntry, root, reference string) (workrun.Journal, error) {
	var j workrun.Journal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&j) != nil || decoder.Decode(new(any)) != io.EOF || j.Version != 1 || j.ID != e.RunID || j.Plan.Root != root || j.Plan.Reference != reference || filepath.Base(j.Plan.Ticket) != e.Ticket || filepath.Clean(j.Workspace) != filepath.Join(dir, "workspace") {
		return workrun.Journal{}, errors.New("invalid migration run snapshot")
	}
	if err := workrun.ValidateRuntimeMigrations(&j); err != nil {
		return workrun.Journal{}, err
	}
	return j, nil
}

func makeRuntimeMigration(ctx context.Context, dir string, state State, target runtimeimage.State, entries []RuntimeMigrationEntry) (runtimeMigrationTransaction, State, []workrun.Journal, error) {
	oldFeature, err := readRuntimeJournal(filepath.Join(dir, "journal.json"))
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	oldState, err := decodeRuntimeFeature(oldFeature)
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	if !reflect.DeepEqual(oldState, state) {
		return runtimeMigrationTransaction{}, State{}, nil, errors.New("feature changed while acquiring migration ownership")
	}
	if err := verifyRuntimeRunInventory(state.Plan.Root, state.Plan.Reference, entries); err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	journals, err := runtimeOriginalJournals(state.Plan.Root, state.Plan.Reference, entries)
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	settings, oldName, oldImage, err := runtimeSettings(oldState.Settings, target.Name, target.ImageID, entries, journals)
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	if oldImage == target.ImageID {
		return runtimeMigrationTransaction{}, State{}, nil, errors.New("feature already uses the target runtime image")
	}
	next := oldState
	data, err := json.Marshal(oldState)
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	if err := json.Unmarshal(data, &next); err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	next.Settings = settings
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return runtimeMigrationTransaction{}, State{}, nil, err
		}
		e := &entries[i]
		j := journals[i]
		if j.ImageID != oldImage || j.Plan.RuntimeName != oldName {
			return runtimeMigrationTransaction{}, State{}, nil, fmt.Errorf("ticket %s does not use the frozen feature runtime", e.Ticket)
		}
		result := oldState.Results[e.Ticket]
		if result.State == "merged" || result.Branch != "" && result.Branch != j.Plan.Branch || result.Base != "" && result.Base != j.Plan.Base || result.BaseSHA != "" && result.BaseSHA != j.Plan.BaseSHA || result.HeadSHA != "" && result.HeadSHA != j.Publication.HeadSHA || result.PRNumber != 0 && result.PRNumber != j.Publication.Number || result.URL != "" && result.URL != j.Publication.URL {
			return runtimeMigrationTransaction{}, State{}, nil, fmt.Errorf("ticket %s feature and run identities conflict", e.Ticket)
		}
		runDir, err := runtimeRunDir(state.Plan.Root, state.Plan.Reference, *e)
		if err != nil {
			return runtimeMigrationTransaction{}, State{}, nil, err
		}
		if result.Directory != "" && filepath.Clean(result.Directory) != runDir {
			return runtimeMigrationTransaction{}, State{}, nil, errors.New("saved run directory changed")
		}
		e.Old, err = readRuntimeJournal(filepath.Join(runDir, "journal.json"))
		if err != nil {
			return runtimeMigrationTransaction{}, State{}, nil, err
		}
		e.New, err = runtimePreparedJournal(j, target)
		if err != nil {
			return runtimeMigrationTransaction{}, State{}, nil, err
		}
		e.OldHash, e.NewHash = amendmentHash(e.Old), amendmentHash(e.New)
		nextResult := next.Results[e.Ticket]
		nextResult.Directory = runDir
		nextResult.Branch = j.Plan.Branch
		nextResult.Base = j.Plan.Base
		nextResult.BaseSHA = j.Plan.BaseSHA
		nextResult.HeadSHA = j.Publication.HeadSHA
		nextResult.URL = j.Publication.URL
		nextResult.PRNumber = j.Publication.Number
		nextResult.State = "blocked"
		nextResult.HoldStage = ""
		nextResult.StopReason = "Runtime migrated; resume the original run to repeat isolated checks without publishing"
		next.Results[e.Ticket] = nextResult
	}
	next.UpdatedAt = time.Now().UTC()
	if err := validateState(&next); err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	newFeature, err := json.Marshal(next)
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	newFeature = append(newFeature, '\n')
	if len(newFeature) > maxJournal {
		return runtimeMigrationTransaction{}, State{}, nil, errors.New("migrated feature journal exceeds its bound")
	}
	id, err := workrun.NewID()
	if err != nil {
		return runtimeMigrationTransaction{}, State{}, nil, err
	}
	tx := runtimeMigrationTransaction{Version: 1, ID: id, Root: state.Plan.Root, Reference: state.Plan.Reference, TargetName: target.Name, TargetImage: target.ImageID, Target: target, OldFeature: oldFeature, NewFeature: newFeature, OldFeatureHash: amendmentHash(oldFeature), NewFeatureHash: amendmentHash(newFeature), Entries: entries}
	return tx, oldState, journals, nil
}

func validateRuntimeTransaction(tx runtimeMigrationTransaction, root, reference string) (State, []workrun.Journal, error) {
	if tx.Version != 1 || !runIDPattern(tx.ID) || tx.Root != root || tx.Reference != reference || tx.TargetName != tx.Target.Name || tx.TargetImage != tx.Target.ImageID || tx.TargetImage == "" || amendmentHash(tx.OldFeature) != tx.OldFeatureHash || amendmentHash(tx.NewFeature) != tx.NewFeatureHash {
		return State{}, nil, errors.New("invalid feature runtime migration transaction")
	}
	oldState, err := decodeRuntimeFeature(tx.OldFeature)
	if err != nil {
		return State{}, nil, err
	}
	newState, err := decodeRuntimeFeature(tx.NewFeature)
	if err != nil {
		return State{}, nil, err
	}
	if oldState.Plan.Root != root || oldState.Plan.Reference != reference || !reflect.DeepEqual(oldState.Plan, newState.Plan) {
		return State{}, nil, errors.New("runtime migration feature identity changed")
	}
	expected, err := migrationEntries(oldState)
	if err != nil || len(expected) != len(tx.Entries) {
		return State{}, nil, errors.New("runtime migration run set changed")
	}
	oldJournals := make([]workrun.Journal, 0, len(expected))
	newJournals := make([]workrun.Journal, 0, len(expected))
	for i, e := range tx.Entries {
		if e.Ticket != expected[i].Ticket || e.RunID != expected[i].RunID || len(e.Old) > 1024*1024 || len(e.New) > 1024*1024 || amendmentHash(e.Old) != e.OldHash || amendmentHash(e.New) != e.NewHash {
			return State{}, nil, errors.New("runtime migration run snapshot changed")
		}
		runDir, err := runtimeRunDir(root, reference, e)
		if err != nil {
			return State{}, nil, err
		}
		old, err := decodeRuntimeJournal(e.Old, runDir, e, root, reference)
		if err != nil {
			return State{}, nil, err
		}
		next, err := decodeRuntimeJournal(e.New, runDir, e, root, reference)
		if err != nil {
			return State{}, nil, err
		}
		computed, err := workrun.PreparePublishedFeatureRuntimeMigration(old, tx.TargetName, tx.Target)
		if err != nil {
			return State{}, nil, err
		}
		if len(next.RuntimeMigrations) != 1 {
			return State{}, nil, errors.New("runtime migration audit missing")
		}
		computed.RuntimeMigrations[0].At = next.RuntimeMigrations[0].At
		computed.UpdatedAt = next.UpdatedAt
		computedJSON, computedErr := json.Marshal(computed)
		nextJSON, nextErr := json.Marshal(next)
		if computedErr != nil || nextErr != nil || !bytes.Equal(computedJSON, nextJSON) {
			return State{}, nil, errors.New("runtime migration run transformation differs from its validated plan")
		}

		oldJournals = append(oldJournals, old)
		newJournals = append(newJournals, next)
	}
	settings, oldName, oldImage, err := runtimeSettings(oldState.Settings, tx.TargetName, tx.TargetImage, tx.Entries, oldJournals)
	if err != nil {
		return State{}, nil, err
	}
	expectedState := oldState
	clone, err := json.Marshal(oldState)
	if err != nil {
		return State{}, nil, err
	}
	if err := json.Unmarshal(clone, &expectedState); err != nil {
		return State{}, nil, err
	}
	expectedState.Settings = settings
	expectedState.UpdatedAt = newState.UpdatedAt
	for i, e := range tx.Entries {
		old, next := oldJournals[i], newJournals[i]
		if old.ImageID != oldImage || old.Plan.RuntimeName != oldName || next.Publication != old.Publication {
			return State{}, nil, errors.New("runtime migration source or publication changed")
		}
		result := expectedState.Results[e.Ticket]
		dir, err := runtimeRunDir(root, reference, e)
		if err != nil {
			return State{}, nil, err
		}
		result.Directory = dir
		result.Branch = old.Plan.Branch
		result.Base = old.Plan.Base
		result.BaseSHA = old.Plan.BaseSHA
		result.HeadSHA = old.Publication.HeadSHA
		result.URL = old.Publication.URL
		result.PRNumber = old.Publication.Number
		result.State = "blocked"
		result.HoldStage = ""
		result.StopReason = "Runtime migrated; resume the original run to repeat isolated checks without publishing"
		expectedState.Results[e.Ticket] = result
	}
	if !reflect.DeepEqual(expectedState, newState) {
		return State{}, nil, errors.New("runtime migration feature transformation differs from its validated plan")
	}
	return oldState, oldJournals, nil
}

func verifyRuntimeTransactionFiles(dir string, tx runtimeMigrationTransaction) error {
	current, err := readRuntimeJournal(filepath.Join(dir, "journal.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(current, tx.OldFeature) && !bytes.Equal(current, tx.NewFeature) {
		return errors.New("feature journal changed during runtime migration")
	}
	for _, e := range tx.Entries {
		runDir, err := runtimeRunDir(tx.Root, tx.Reference, e)
		if err != nil {
			return err
		}
		current, err = readRuntimeJournal(filepath.Join(runDir, "journal.json"))
		if err != nil {
			return err
		}
		if !bytes.Equal(current, e.Old) && !bytes.Equal(current, e.New) {
			return fmt.Errorf("ticket %s changed during runtime migration", e.Ticket)
		}
	}
	return nil
}

func verifyRuntimeTransactionApplied(dir string, tx runtimeMigrationTransaction, rollback bool) error {
	expected := tx.NewFeature
	if rollback {
		expected = tx.OldFeature
	}
	current, err := readRuntimeJournal(filepath.Join(dir, "journal.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return errors.New("feature migration write was not confirmed")
	}
	for _, e := range tx.Entries {
		runDir, err := runtimeRunDir(tx.Root, tx.Reference, e)
		if err != nil {
			return err
		}
		expected = e.New
		if rollback {
			expected = e.Old
		}
		current, err = readRuntimeJournal(filepath.Join(runDir, "journal.json"))
		if err != nil {
			return err
		}
		if !bytes.Equal(current, expected) {
			return errors.New("ticket migration write was not confirmed")
		}
	}
	return nil
}

func verifyRuntimeArchiveMode(dir string, tx runtimeMigrationTransaction, rollback bool) error {
	other := "rolled-back"
	if rollback {
		other = "completed"
	}
	path := filepath.Join(dir, "runtime-migration-"+tx.ID+"-"+other+".json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("runtime migration was already committed in the other direction; repeat that recovery action")
}

var runtimeMigrationWrite = writeAmendmentFile

func applyRuntimeTransaction(ctx context.Context, dir string, tx runtimeMigrationTransaction, rollback bool) error {
	for _, e := range tx.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		runDir, err := runtimeRunDir(tx.Root, tx.Reference, e)
		if err != nil {
			return err
		}
		data := e.New
		if rollback {
			data = e.Old
		}
		if err := runtimeMigrationWrite(filepath.Join(runDir, "journal.json"), data); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	feature := tx.NewFeature
	if rollback {
		feature = tx.OldFeature
	}
	return runtimeMigrationWrite(filepath.Join(dir, "journal.json"), feature)
}

func finishRuntimeTransaction(dir string, tx runtimeMigrationTransaction, rollback bool) error {
	pending := filepath.Join(dir, featureguard.RuntimeMigrationPending)
	suffix := "completed"
	if rollback {
		suffix = "rolled-back"
	}
	archive := filepath.Join(dir, "runtime-migration-"+tx.ID+"-"+suffix+".json")
	if err := os.Link(pending, archive); errors.Is(err, os.ErrExist) {
		before, _, e := readAmendmentFile(pending, maxRuntimeMigrationTransaction, false)
		if e != nil {
			return e
		}
		after, _, e := readAmendmentFile(archive, maxRuntimeMigrationTransaction, false)
		if e != nil || !bytes.Equal(before, after) {
			return errors.New("runtime migration archive differs from pending transaction")
		}
	} else if err != nil {
		return err
	}
	if err := syncAmendmentDirectory(dir); err != nil {
		return err
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	return syncAmendmentDirectory(dir)
}

// MigrateFeatureRuntime holds feature and run locks for its whole operation.
// Interrupted transactions fence ordinary access and can be finished or rolled
// back by repeating the same command. The verifier is required for both the
// preview and forward application; rollback restores only recorded bytes.
func MigrateFeatureRuntime(ctx context.Context, root, reference string, target runtimeimage.State, dryRun, rollback bool, verify RuntimeMigrationVerify) (RuntimeMigrationPreview, error) {
	preview := RuntimeMigrationPreview{Reference: reference, TargetName: target.Name, TargetImage: target.ImageID, DryRun: dryRun, Rollback: rollback}
	dir, _, err := amendmentPaths(root, reference)
	if err != nil {
		return preview, err
	}
	featureLock, err := ownAmendmentFeature(dir)
	if err != nil {
		return preview, err
	}
	defer featureLock.Close()
	pendingPath := filepath.Join(dir, featureguard.RuntimeMigrationPending)
	pending, recovery, err := readAmendmentFile(pendingPath, maxRuntimeMigrationTransaction, true)
	if err != nil {
		return preview, err
	}
	var tx runtimeMigrationTransaction
	var oldState State
	var journals []workrun.Journal
	if recovery {
		decoder := json.NewDecoder(bytes.NewReader(pending))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&tx) != nil || decoder.Decode(new(any)) != io.EOF || tx.TargetName != target.Name || tx.TargetImage != target.ImageID {
			return preview, errors.New("repeat the pending migration with its original target to recover")
		}
		preview.Recovery = true
	} else {
		if rollback {
			return preview, errors.New("no pending feature runtime migration to roll back")
		}
		state, err := Load(dir)
		if err != nil {
			return preview, err
		}
		entries, err := migrationEntries(state)
		if err != nil {
			return preview, err
		}
		locks, err := acquireRuntimeRuns(root, reference, entries)
		if err != nil {
			return preview, err
		}
		defer releaseRuntimeRuns(locks)
		tx, oldState, journals, err = makeRuntimeMigration(ctx, dir, state, target, entries)
		if err != nil {
			return preview, err
		}
		if _, _, err := validateRuntimeTransaction(tx, root, reference); err != nil {
			return preview, err
		}
		if verify == nil {
			return preview, errors.New("feature runtime migration requires frozen-input and PR verification")
		}
		if err := verify(ctx, oldState, journals); err != nil {
			return preview, err
		}
		for i, e := range tx.Entries {
			preview.Runs = append(preview.Runs, runtimeMigrationRunPreview(e, journals[i]))
		}
		if dryRun {
			return preview, nil
		}
		data, err := json.Marshal(tx)
		if err != nil || len(data) > maxRuntimeMigrationTransaction {
			return preview, errors.New("feature runtime migration transaction exceeds its bound")
		}
		if err := runtimeMigrationWrite(pendingPath, data); err != nil {
			return preview, err
		}
		if err := verifyRuntimeRunInventory(root, reference, tx.Entries); err != nil {
			return preview, err
		}
		if err := verifyRuntimeTransactionFiles(dir, tx); err != nil {
			return preview, err
		}
		if err := applyRuntimeTransaction(ctx, dir, tx, false); err != nil {
			return preview, err
		}
		if err := verify(ctx, oldState, journals); err != nil {
			return preview, err
		}
		if err := verifyRuntimeTransactionFiles(dir, tx); err != nil {
			return preview, err
		}
		if err := verifyRuntimeRunInventory(root, reference, tx.Entries); err != nil {
			return preview, err
		}
		if err := verifyRuntimeTransactionApplied(dir, tx, false); err != nil {
			return preview, err
		}
		if err := finishRuntimeTransaction(dir, tx, false); err != nil {
			return preview, err
		}
		return preview, nil
	}
	locks, err := acquireRuntimeRuns(root, reference, tx.Entries)
	if err != nil {
		return preview, err
	}
	defer releaseRuntimeRuns(locks)
	oldState, journals, err = validateRuntimeTransaction(tx, root, reference)
	if err != nil {
		return preview, err
	}
	if err := verifyRuntimeRunInventory(root, reference, tx.Entries); err != nil {
		return preview, err
	}
	if err := verifyRuntimeTransactionFiles(dir, tx); err != nil {
		return preview, err
	}
	if err := verifyRuntimeArchiveMode(dir, tx, rollback); err != nil {
		return preview, err
	}
	if !rollback {
		if verify == nil {
			return preview, errors.New("feature runtime migration requires frozen-input and PR verification")
		}
		if err := verify(ctx, oldState, journals); err != nil {
			return preview, err
		}
	}
	for i, e := range tx.Entries {
		preview.Runs = append(preview.Runs, runtimeMigrationRunPreview(e, journals[i]))
	}
	if dryRun {
		return preview, nil
	}
	if err := applyRuntimeTransaction(ctx, dir, tx, rollback); err != nil {
		return preview, err
	}
	if !rollback {
		if err := verify(ctx, oldState, journals); err != nil {
			return preview, err
		}
	}
	if err := verifyRuntimeTransactionFiles(dir, tx); err != nil {
		return preview, err
	}
	if err := verifyRuntimeRunInventory(root, reference, tx.Entries); err != nil {
		return preview, err
	}
	if err := verifyRuntimeTransactionApplied(dir, tx, rollback); err != nil {
		return preview, err
	}
	if err := finishRuntimeTransaction(dir, tx, rollback); err != nil {
		return preview, err
	}
	return preview, nil
}
