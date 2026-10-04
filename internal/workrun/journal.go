package workrun

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func NewID() (string, error) {
	var data [12]byte
	_, err := rand.Read(data[:])
	return hex.EncodeToString(data[:]), err
}

func saveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data)+1 > 1024*1024 {
		return fmt.Errorf("private run state exceeds 1 MiB; checkpoint was not replaced")
	}
	if err := realDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("private run state must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".journal-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func Save(directory string, journal *Journal) error {
	if err := journal.Plan.ValidateSidecarImages(); err != nil {
		return err
	}
	if journal.StartedAt.IsZero() {
		journal.StartedAt = journal.UpdatedAt
		if journal.StartedAt.IsZero() {
			journal.StartedAt = time.Now().UTC()
		}
	}
	journal.UpdatedAt = time.Now().UTC()
	return saveJSON(filepath.Join(directory, "journal.json"), journal)
}

func Load(directory string) (Journal, error) {
	if err := realDirectory(directory); err != nil {
		return Journal{}, err
	}
	path := filepath.Join(directory, "journal.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return Journal{}, fmt.Errorf("run journal must be a regular file of at most 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return Journal{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Journal{}, fmt.Errorf("run journal changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return Journal{}, fmt.Errorf("run journal exceeds 1 MiB")
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || decoder.Decode(new(any)) != io.EOF || journal.Version != 1 || journal.ID != filepath.Base(directory) || !objectID.MatchString(journal.Plan.StartingSHA) || !objectID.MatchString(journal.Plan.SourceSHA) {
		return Journal{}, fmt.Errorf("invalid run journal")
	}
	if filepath.Clean(journal.Workspace) != filepath.Join(directory, "workspace") {
		return Journal{}, fmt.Errorf("run journal points outside its captured workspace")
	}
	if err := ValidateModel(journal.Plan.Roles.Implementation); err != nil {
		return Journal{}, err
	}
	if err := ValidateModel(journal.Plan.Roles.Review); err != nil {
		return Journal{}, err
	}
	if journal.Plan.Roles.Implementation.Provider == journal.Plan.Roles.Review.Provider {
		return Journal{}, fmt.Errorf("run providers must differ")
	}
	if err := journal.Plan.ValidateSidecarImages(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}
