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

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/workrun"
)

// Forget removes only a stopped run's dashboard registration. Journals, logs,
// inputs and workspaces remain available, and resuming can register the run again.
// Controller ownership is checked with the same run.lock used by workrun.Runner;
// a stale heartbeat alone never authorises removal.
func (r *Registry) Forget(id string) error {
	if !identifier.MatchString(id) {
		return fmt.Errorf("forget requires a full 24-character run ID")
	}
	state, err := historyDirectory(r.stateDir)
	if err != nil {
		return fmt.Errorf("cannot inspect private run registry; check its directory and permissions")
	}
	defer state.Close()
	registryPath := filepath.Join(r.stateDir, "runs")
	registry, err := historyDirectory(registryPath)
	if err != nil {
		return fmt.Errorf("cannot inspect private run registry; check its directory and permissions")
	}
	defer registry.Close()
	name := id + ".json"
	original, originalInfo, err := historyEntry(registry, name, id)
	if err != nil {
		return err
	}
	run, err := historyDirectory(original.Directory)
	if err != nil {
		return fmt.Errorf("cannot safely inspect the registered run directory; restore its real private directory before forgetting")
	}
	defer run.Close()
	// A controller takes run.lock before registering. Match that ordering and
	// never wait for a live controller while holding a registry lock or mutex.
	controller, err := historyLock(run, original.Directory, "run.lock")
	if err != nil {
		return fmt.Errorf("run controller is busy or its lock is unsafe; stop the controller and check run.lock before forgetting")
	}
	defer controller.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	registration, err := historyLock(registry, registryPath, id+".lock")
	if err != nil {
		return fmt.Errorf("run registration is busy or its lock is unsafe; retry after checking registry storage")
	}
	defer registration.Close()
	current, currentInfo, err := historyEntry(registry, name, id)
	if err != nil {
		return err
	}
	if current != original || !os.SameFile(originalInfo, currentInfo) {
		return fmt.Errorf("run registration changed during removal; inspect the dashboard and retry")
	}
	journal, journalErr := workrun.Load(current.Directory)
	available := journalErr == nil && journal.ID == id && journal.Plan.Root == current.Root && journal.Plan.Reference == current.Reference && journal.Plan.Ticket == current.Ticket
	if available {
		var activity Snapshot
		if err := readJSON(filepath.Join(current.Directory, "activity.json"), &activity); err == nil && activity.Version == 1 && activity.ID == id && identifier.MatchString(activity.ControllerID) && !activity.HeartbeatAt.IsZero() && !activity.Stopped && time.Since(activity.HeartbeatAt) <= StaleAfter {
			return fmt.Errorf("run reports a live controller; stop it before forgetting")
		}
	}
	// Retain anchored roots throughout. Also refuse a path replacement so a
	// resume cannot register through different directory/lock inodes.
	for _, directory := range []struct {
		root *os.Root
		path string
	}{{state, r.stateDir}, {registry, registryPath}, {run, current.Directory}} {
		if err := historySameDirectory(directory.root, directory.path); err != nil {
			return err
		}
	}
	for _, held := range []struct {
		root *os.Root
		name string
		lock *os.File
	}{{run, "run.lock", controller}, {registry, id + ".lock", registration}} {
		info, err := historyFile(held.root, held.name)
		opened, statErr := held.lock.Stat()
		if err != nil || statErr != nil || !os.SameFile(info, opened) {
			return fmt.Errorf("run ownership lock changed; inspect private storage before forgetting")
		}
	}
	final, finalInfo, err := historyEntry(registry, name, id)
	if err != nil {
		return err
	}
	if final != original || !os.SameFile(originalInfo, finalInfo) {
		return fmt.Errorf("run registration changed during removal; inspect the dashboard and retry")
	}
	if err := registry.Remove(name); err != nil {
		return fmt.Errorf("cannot remove run registration; check private registry storage")
	}
	return nil
}

func historyDirectory(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || realDirectory(path) != nil {
		return nil, fmt.Errorf("run history requires real absolute directories")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !historyOwned(info, false) {
		return nil, fmt.Errorf("run history requires private owned directories")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if opened, err := root.Stat("."); err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("run history directory changed while opening")
	}
	return root, nil
}

func historySameDirectory(root *os.Root, path string) error {
	info, err := os.Lstat(path)
	opened, statErr := root.Stat(".")
	if err != nil || statErr != nil || realDirectory(path) != nil || !os.SameFile(info, opened) || info.Mode().Perm() != 0700 || !historyOwned(info, false) {
		return fmt.Errorf("run history directory changed; inspect private storage before forgetting")
	}
	return nil
}

func historyFile(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxJSON || !historyOwned(info, true) {
		return nil, fmt.Errorf("run history file must be private, owned, regular and single-linked")
	}
	return info, nil
}

func historyEntry(root *os.Root, name, id string) (entry, os.FileInfo, error) {
	invalid := fmt.Errorf("run registration is missing or unsafe; inspect its private registry metadata before forgetting")
	info, err := historyFile(root, name)
	if err != nil {
		return entry{}, nil, invalid
	}
	file, err := root.Open(name)
	if err != nil {
		return entry{}, nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm() != 0600 || !historyOwned(opened, true) {
		return entry{}, nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJSON+1))
	if err != nil || len(data) > maxJSON {
		return entry{}, nil, invalid
	}
	var record entry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != 1 || record.ID != id || !filepath.IsAbs(record.Directory) || filepath.Clean(record.Directory) != record.Directory || filepath.Base(record.Directory) != id || !filepath.IsAbs(record.Root) || filepath.Clean(record.Root) != record.Root || record.Reference == "" || record.Ticket == "" {
		return entry{}, nil, invalid
	}
	for _, value := range []string{record.Directory, record.Root, record.Reference, record.Ticket} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return entry{}, nil, invalid
		}
	}
	return record, info, nil
}

func historyLock(root *os.Root, directory, name string) (*os.File, error) {
	created, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		created.Close()
	} else if !os.IsExist(err) {
		return nil, err
	}
	info, err := historyFile(root, name)
	if err != nil {
		return nil, err
	}
	if err := historySameDirectory(root, directory); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(filepath.Join(directory, name))
	if err != nil {
		return nil, err
	}
	opened, err := lock.Stat()
	current, currentErr := historyFile(root, name)
	if err != nil || currentErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) || opened.Mode().Perm() != 0600 || !historyOwned(opened, true) {
		lock.Close()
		return nil, fmt.Errorf("run history lock changed during acquisition")
	}
	return lock, nil
}
