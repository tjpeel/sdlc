package workseries

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxJournal = 1024 * 1024

func Directory(root, reference string, create bool) (string, error) {
	if root == "" || !safeReference(reference) {
		return "", errors.New("invalid series directory identity")
	}
	if err := realDir(root); err != nil {
		return "", err
	}
	path := root
	for _, part := range []string{".sdlc", "work", reference, "series"} {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(path, 0700); err != nil {
				return "", err
			}
			info, err = os.Lstat(path)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("private series paths must be real directories")
		}
		if create {
			_ = os.Chmod(path, 0700)
		}
	}
	return path, nil
}
func safeReference(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, "/\\\x00\n\r")
}
func realDir(path string) error {
	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("private series paths must be real directories")
	}
	return nil
}

func Load(directory string) (State, error) {
	if err := privateSeriesDir(directory); err != nil {
		return State{}, err
	}
	path := filepath.Join(directory, "journal.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxJournal {
		return State{}, errors.New("series journal must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return State{}, errors.New("series journal changed while opening")
	}
	data, e := io.ReadAll(io.LimitReader(f, maxJournal+1))
	if e != nil || len(data) > maxJournal {
		return State{}, errors.New("series journal exceeds 1 MiB")
	}
	var s State
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || validateState(&s) != nil {
		return State{}, errors.New("invalid series journal")
	}
	return s, nil
}

func Save(directory string, state *State) error {
	if err := privateSeriesDir(directory); err != nil {
		return err
	}
	if err := validateState(state); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(state)
	if err != nil || len(data)+1 > maxJournal {
		return errors.New("series journal exceeds 1 MiB")
	}
	path := filepath.Join(directory, "journal.json")
	if i, e := os.Lstat(path); e == nil && !i.Mode().IsRegular() {
		return errors.New("series journal must be a regular file")
	}
	f, err := os.CreateTemp(directory, ".journal-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
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
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func privateSeriesDir(directory string) error {
	if err := realDir(directory); err != nil {
		return err
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		return errors.New("series directory must be private (0700)")
	}
	return nil
}

func validateState(s *State) error {
	if s == nil || s.Version != 1 || s.Plan.Root == "" || !filepath.IsAbs(s.Plan.Root) || validatePlan(&s.Plan) != nil || s.Plan.DefinitionSHA == "" || s.Plan.DefinitionSHA != definitionSHA(s.Plan) || s.Results == nil {
		return errors.New("invalid series state")
	}
	known := ticketMap(s.Plan.Tickets)
	for key, r := range s.Results {
		ticket, ok := known[key]
		if !ok || !validResult(s.Plan, ticket, r) {
			return fmt.Errorf("invalid series result")
		}
	}
	return nil
}
func validResult(p Plan, ticket Ticket, r Result) bool {
	if strings.ContainsAny(r.RunID+r.State+r.Branch+r.Base+r.BaseSHA+r.HeadSHA+r.MergeSHA+r.URL+r.StopReason, "\x00") {
		return false
	}
	if r.RunID != "" && !runIDPattern(r.RunID) {
		return false
	}
	if r.State != "" && !knownState(r.State) {
		return false
	}
	for _, value := range []string{r.BaseSHA, r.HeadSHA, r.MergeSHA} {
		if value != "" && !objectSHA(value) {
			return false
		}
	}
	if r.Directory != "" {
		expected := filepath.Join(p.Root, ".sdlc", "work", p.Reference, "runs", strings.TrimSuffix(ticket.File, ".md"), r.RunID)
		if r.RunID == "" || filepath.Clean(r.Directory) != expected {
			return false
		}
	}
	return true
}

var objectSHARe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func objectSHA(value string) bool { return objectSHARe.MatchString(value) }
