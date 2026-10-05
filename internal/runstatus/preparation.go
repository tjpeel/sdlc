package runstatus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

// PreparationFailure records a caught failure before the first execution
// journal. It contains identity and failure metadata, never source or inputs.
type PreparationFailure struct {
	Version      int           `json:"version"`
	ID           string        `json:"id"`
	Root         string        `json:"root"`
	Reference    string        `json:"reference"`
	Ticket       string        `json:"ticket"`
	Roles        workrun.Roles `json:"roles"`
	FailedAt     time.Time     `json:"failed_at"`
	Reason       string        `json:"reason"`
	FeatureOwned bool          `json:"feature_owned"`
}

// OwnPreparation uses the execution controller's lock, with private ownership
// and inode checks. Callers release it before the normal Runner takes over.
func OwnPreparation(directory string) (*os.File, error) {
	run, err := historyDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer run.Close()
	return historyLock(run, directory, "run.lock")
}

func (p PreparationFailure) validate(directory string) error {
	if p.Version != 1 || !identifier.MatchString(p.ID) || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Ticket == "" || p.Ticket == "." || p.Ticket == ".." || filepath.Base(p.Ticket) != p.Ticket || strings.ContainsAny(p.Root+p.Reference+p.Ticket, "\x00\r\n") || p.FailedAt.IsZero() || p.FailedAt.After(time.Now().Add(HeartbeatInterval)) || strings.TrimSpace(p.Reason) == "" || workrun.ValidateModel(p.Roles.Implementation) != nil || workrun.ValidateModel(p.Roles.Review) != nil || p.Roles.Implementation.Provider == p.Roles.Review.Provider {
		return fmt.Errorf("invalid preparation failure metadata")
	}
	canonical, err := workrun.RunDirectory(p.Root, p.Reference, p.Ticket, p.ID, false)
	if err != nil || canonical != directory {
		return fmt.Errorf("preparation identity does not match its canonical run directory")
	}
	return nil
}

// LoadPreparation only falls back when the execution journal is truly absent.
// A corrupt journal or link is an execution checkpoint requiring inspection.
func LoadPreparation(directory string) (PreparationFailure, error) {
	run, err := historyDirectory(directory)
	if err != nil {
		return PreparationFailure{}, err
	}
	defer run.Close()
	if _, err := run.Lstat("journal.json"); !os.IsNotExist(err) {
		return PreparationFailure{}, fmt.Errorf("preparation fallback requires an absent execution journal")
	}
	info, err := historyFile(run, "preparation.json")
	if err != nil {
		return PreparationFailure{}, err
	}
	file, err := run.Open("preparation.json")
	if err != nil {
		return PreparationFailure{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return PreparationFailure{}, fmt.Errorf("preparation metadata changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJSON+1))
	if err != nil || len(data) > maxJSON {
		return PreparationFailure{}, fmt.Errorf("preparation metadata exceeds its size limit")
	}
	var p PreparationFailure
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("invalid preparation failure metadata")
	}
	if err := p.validate(directory); err != nil {
		return p, err
	}
	current, err := historyFile(run, "preparation.json")
	if err != nil || !os.SameFile(opened, current) {
		return p, fmt.Errorf("preparation metadata changed while reading")
	}
	if _, err := run.Lstat("journal.json"); !os.IsNotExist(err) {
		return p, fmt.Errorf("execution journal appeared while reading preparation metadata")
	}
	if err := historySameDirectory(run, directory); err != nil {
		return p, err
	}
	return p, nil
}

// ValidatePreparationRetry permits only metadata from an owned, caught feature
// failure. Provider work or unknown leftovers are always retained for inspection.
// The caller must hold the returned ownership lock until preparation begins.
func ValidatePreparationRetry(directory string, expected PreparationFailure) (*os.File, error) {
	owner, err := OwnPreparation(directory)
	if err != nil {
		return nil, err
	}
	p, err := LoadPreparation(directory)
	if err == nil && (!p.FeatureOwned || p.ID != expected.ID || p.Root != expected.Root || p.Reference != expected.Reference || p.Ticket != expected.Ticket || p.Roles != expected.Roles) {
		err = fmt.Errorf("failed preparation differs from frozen feature identity")
	}
	if err == nil {
		run, openErr := historyDirectory(directory)
		if openErr != nil {
			err = openErr
		} else {
			defer run.Close()
			file, readErr := run.Open(".")
			var entries []os.DirEntry
			if readErr == nil {
				entries, readErr = file.ReadDir(-1)
				file.Close()
			}
			err = readErr
			for _, entry := range entries {
				if entry.Name() != "preparation.json" && entry.Name() != "run.lock" {
					err = fmt.Errorf("incomplete ticket preparation retained; inspect private run leftovers before retrying")
					break
				}
				if _, fileErr := historyFile(run, entry.Name()); fileErr != nil {
					err = fileErr
					break
				}
			}
			if err == nil {
				err = historySameDirectory(run, directory)
			}
		}
	}
	if err != nil {
		owner.Close()
		return nil, err
	}
	return owner, nil
}

// RegisterPreparationFailure is called under run.lock, before acquiring registry
// ownership. Invalid metadata never creates an orphan registration.
func (r *Registry) RegisterPreparationFailure(directory string, p PreparationFailure, owner *os.File) error {
	if err := p.validate(directory); err != nil {
		return err
	}
	run, err := historyDirectory(directory)
	if err != nil {
		return err
	}
	defer run.Close()
	info, err := historyFile(run, "run.lock")
	if owner == nil {
		return fmt.Errorf("preparation requires controller ownership")
	}
	opened, statErr := owner.Stat()
	if err != nil || statErr != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("preparation ownership changed")
	}
	if _, err := run.Lstat("journal.json"); !os.IsNotExist(err) {
		return fmt.Errorf("cannot replace an execution journal with preparation metadata")
	}
	if _, err := run.Lstat("preparation.json"); err == nil {
		if _, err := LoadPreparation(directory); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := historySameDirectory(run, directory); err != nil {
		return err
	}
	if err := replaceJSON(filepath.Join(directory, "preparation.json"), p); err != nil {
		return err
	}
	if _, err := LoadPreparation(directory); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := privateDirectory(r.stateDir); err != nil {
		return err
	}
	state, err := historyDirectory(r.stateDir)
	if err != nil {
		return err
	}
	defer state.Close()
	path := filepath.Join(r.stateDir, "runs")
	if err := privateDirectory(path); err != nil {
		return err
	}
	registry, err := historyDirectory(path)
	if err != nil {
		return err
	}
	defer registry.Close()
	lock, err := historyLock(registry, path, p.ID+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	next := entry{Version: 1, ID: p.ID, Directory: directory, Root: p.Root, Reference: p.Reference, Ticket: ".sdlc/work/" + p.Reference + "/tickets/" + p.Ticket, Preparation: true}
	name := p.ID + ".json"
	if _, err := registry.Lstat(name); err == nil {
		old, _, err := historyEntry(registry, name, p.ID)
		if err != nil {
			return err
		}
		if old.ID != next.ID || old.Directory != next.Directory || old.Root != next.Root || old.Reference != next.Reference || old.Ticket != next.Ticket || !old.Preparation {
			return fmt.Errorf("run identity is already registered elsewhere")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return replaceJSON(filepath.Join(path, name), next)
}
