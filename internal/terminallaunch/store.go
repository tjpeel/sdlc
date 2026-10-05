// Package terminallaunch hands the existing foreground CLI to an independent
// terminal. Receipts correlate launches; they are not job journals or leases.
package terminallaunch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ErrUnsupported guarantees no terminal handoff was dispatched. A backend
// must return a different error after dispatch, even if acknowledgement fails.
var ErrUnsupported = errors.New("background terminal launch unavailable")

type Request struct {
	ID          string   `json:"id"`
	Root        string   `json:"root"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	PreviewHash string   `json:"preview_hash,omitempty"`
}

type Receipt struct {
	ID            string    `json:"id"`
	State         string    `json:"state"`
	Root          string    `json:"root"`
	Reference     string    `json:"reference,omitempty"`
	Directory     string    `json:"directory,omitempty"`
	RunIDs        []string  `json:"run_ids,omitempty"`
	Error         string    `json:"error,omitempty"`
	ManualCommand string    `json:"manual_command,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RunIdentity struct {
	Reference string
	Directory string
	RunIDs    []string
}

type envelope struct {
	Request          Request `json:"request"`
	RootIdentity     string  `json:"root_identity"`
	ExecutableSHA256 string  `json:"executable_sha256"`
	Receipt          Receipt `json:"receipt"`
}

type Backend interface {
	// Launch returns ErrUnsupported only before dispatch; other failures leave
	// the receipt uncertain because a helper may still arrive.
	Launch(context.Context, string) error
}

type Store struct{ Directory string }

func NewStore(directory string) *Store { return &Store{Directory: directory} }

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// Launch records intent before contacting the terminal. Repeating an ID never
// launches another terminal, including after a lost or ambiguous acknowledgement.
func (s *Store) Launch(ctx context.Context, request Request, backend Backend) (Receipt, error) {
	if err := validateRequest(request); err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	created := false
	err := s.locked(ctx, request.ID, func(path string) error {
		existing, err := readEnvelope(path)
		if err == nil {
			if !reflect.DeepEqual(existing.Request, request) {
				return fmt.Errorf("launch ID already belongs to different arguments")
			}
			receipt = existing.Receipt
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		digest, err := executableDigest(request.Executable)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		receipt = Receipt{ID: request.ID, Root: request.Root, State: "dispatched", CreatedAt: now, UpdatedAt: now, ManualCommand: manualCommand(request, filepath.Dir(s.Directory))}
		created = true
		rootInfo, err := os.Stat(request.Root)
		if err != nil {
			return err
		}
		return writeEnvelope(path, envelope{Request: request, RootIdentity: directoryIdentity(rootInfo), ExecutableSHA256: digest, Receipt: receipt})
	})
	if err != nil {
		return receipt, err
	}
	if !created {
		return receipt, receiptError(receipt)
	}
	command := quote(request.Executable) + " launch execute --id " + request.ID + " --state-dir " + quote(filepath.Dir(s.Directory))
	if backend == nil {
		err = ErrUnsupported
	} else {
		err = backend.Launch(ctx, command)
	}
	if err != nil {
		// A helper may already have consumed the request before its launcher lost
		// the acknowledgement. Never overwrite that evidence or retry the launch.
		updateErr := s.locked(context.Background(), request.ID, func(path string) error {
			e, readErr := readEnvelope(path)
			if readErr != nil {
				return readErr
			}
			if e.Receipt.State == "dispatched" {
				e.Receipt.State = "unknown"
				if errors.Is(err, ErrUnsupported) {
					e.Receipt.State = "unavailable"
				}
				e.Receipt.Error = err.Error()
				e.Receipt.UpdatedAt = time.Now().UTC()
				if writeErr := writeEnvelope(path, e); writeErr != nil {
					return writeErr
				}
			}
			receipt = e.Receipt
			return nil
		})
		return receipt, errors.Join(err, updateErr)
	}
	return s.Status(request.ID)
}

func (s *Store) Status(id string) (Receipt, error) {
	if !idPattern.MatchString(id) {
		return Receipt{}, fmt.Errorf("launch ID must be a lowercase UUID v4")
	}
	if err := cleanDirectory(s.Directory); err != nil {
		return Receipt{}, err
	}
	info, err := os.Lstat(s.Directory)
	if err != nil {
		return Receipt{}, err
	}
	if !owned(info) || info.Mode().Perm() != 0700 {
		return Receipt{}, fmt.Errorf("launch store must be private and owned")
	}
	e, err := readEnvelope(filepath.Join(s.Directory, id+".json"))
	return e.Receipt, err
}

func receiptError(r Receipt) error {
	switch r.State {
	case "unavailable":
		return fmt.Errorf("%w: %s", ErrUnsupported, r.Error)
	case "unknown", "dispatched":
		return fmt.Errorf("terminal launch acknowledgement is %s; inspect launch status before starting another job", r.State)
	case "failed":
		return fmt.Errorf("terminal job failed: %s", r.Error)
	default:
		return nil
	}
}

// Consume validates the original executable again and consumes exactly once,
// before the helper invokes existing run code in its own process and terminal.
func (s *Store) Consume(id string) (Request, error) {
	var request Request
	err := s.locked(context.Background(), id, func(path string) error {
		e, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if e.Receipt.State != "dispatched" && e.Receipt.State != "unknown" && e.Receipt.State != "unavailable" {
			return fmt.Errorf("launch is already consumed or unavailable (%s)", e.Receipt.State)
		}
		if err := validateRequest(e.Request); err != nil {
			return err
		}
		if e.Request.ID != id || e.Receipt.ID != id {
			return fmt.Errorf("launch receipt identity mismatch")
		}
		rootInfo, err := os.Stat(e.Request.Root)
		if err != nil {
			return err
		}
		if directoryIdentity(rootInfo) != e.RootIdentity {
			return fmt.Errorf("project root changed after launch preparation")
		}
		digest, err := executableDigest(e.Request.Executable)
		if err != nil {
			return err
		}
		if digest != e.ExecutableSHA256 {
			return fmt.Errorf("SDLC executable changed after launch preparation")
		}
		current, err := os.Executable()
		if err != nil {
			return err
		}
		current, err = filepath.EvalSymlinks(current)
		if err != nil {
			return err
		}
		if current != e.Request.Executable {
			return fmt.Errorf("launch helper is not the prepared SDLC executable")
		}
		e.Receipt.State = "started"
		e.Receipt.UpdatedAt = time.Now().UTC()
		if err := writeEnvelope(path, e); err != nil {
			return err
		}
		request = e.Request
		return nil
	})
	return request, err
}

func (s *Store) RecordRun(id string, run RunIdentity) error {
	return s.update(id, func(r *Receipt) error {
		if r.State != "started" {
			return fmt.Errorf("launch has not started")
		}
		if run.Reference != "" {
			r.Reference = run.Reference
		}
		if run.Directory != "" {
			r.Directory = run.Directory
		}
		for _, id := range run.RunIDs {
			if id == "" {
				continue
			}
			found := false
			for _, old := range r.RunIDs {
				if old == id {
					found = true
					break
				}
			}
			if !found {
				r.RunIDs = append(r.RunIDs, id)
			}
		}
		return nil
	})
}

func (s *Store) Complete(id string, runErr error) error {
	return s.update(id, func(r *Receipt) error {
		if r.State != "started" {
			return fmt.Errorf("launch has not started")
		}
		r.State = "finished"
		if runErr != nil {
			r.State = "failed"
			r.Error = runErr.Error()
		}
		return nil
	})
}

func (s *Store) update(id string, change func(*Receipt) error) error {
	return s.locked(context.Background(), id, func(path string) error {
		e, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if err := change(&e.Receipt); err != nil {
			return err
		}
		e.Receipt.UpdatedAt = time.Now().UTC()
		return writeEnvelope(path, e)
	})
}

func (s *Store) locked(ctx context.Context, id string, operation func(string) error) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("launch ID must be a lowercase UUID v4")
	}
	if err := privateDirectory(s.Directory); err != nil {
		return err
	}
	lockPath := filepath.Join(s.Directory, id+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		file.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := privateFile(lockPath)
	if err != nil {
		return err
	}
	lock, err := filelock.AcquireContext(ctx, lockPath, filelock.Exclusive, nil)
	if err != nil {
		return err
	}
	defer lock.Close()
	opened, err := lock.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("launch lock changed while opening")
	}
	return operation(filepath.Join(s.Directory, id+".json"))
}

func validateRequest(r Request) error {
	if r.PreviewHash != "" {
		if len(r.PreviewHash) != 64 {
			return fmt.Errorf("preview hash must be lowercase SHA-256")
		}
		for _, c := range r.PreviewHash {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return fmt.Errorf("preview hash must be lowercase SHA-256")
			}
		}
	}
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("launch ID must be a lowercase UUID v4")
	}
	if err := cleanDirectory(r.Root); err != nil {
		return fmt.Errorf("project root: %w", err)
	}
	if !filepath.IsAbs(r.Executable) || filepath.Clean(r.Executable) != r.Executable {
		return fmt.Errorf("executable must be an absolute clean path")
	}
	if len(r.Args) == 0 || r.Args[0] != "run" || len(r.Args) > 256 {
		return fmt.Errorf("launch requires existing run arguments")
	}
	total := 0
	for _, arg := range r.Args {
		total += len(arg)
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("argument contains NUL")
		}
	}
	// Literal values (notably repeated --input values) may themselves look like
	// flags. Keep the existing CLI's argument boundary instead of reinterpreting
	// such values as terminal adapter options.
	valueFlags := map[string]bool{"reference": true, "ticket": true, "provider": true, "github-profile": true, "input": true, "base": true, "branch": true, "repo": true, "model": true, "effort": true, "review-model": true, "review-effort": true, "resume": true, "answer-file": true, "timeout": true, "notify": true, "parallel": true}
	for i := 1; i < len(r.Args); i++ {
		arg := r.Args[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, assigned := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "terminal" || name == "launch-id" || name == "json" {
			return fmt.Errorf("terminal adapter flags must be removed before launch")
		}
		if valueFlags[name] && !assigned {
			i++
		}
	}
	if total > 64*1024 {
		return fmt.Errorf("launch arguments are too large")
	}
	return nil
}

func executableDigest(path string) (string, error) {
	if err := cleanDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("executable must be a regular executable file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("executable changed while opening")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cleanDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("directory must be an absolute clean path")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory must not contain symbolic links")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

func privateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("launch store must be an absolute clean path")
	}
	// Verify existing ancestors before creating anything through them.
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		ancestor = filepath.Dir(ancestor)
	}
	if err := cleanDirectory(ancestor); err != nil {
		return err
	}
	ancestorInfo, err := os.Lstat(ancestor)
	if err != nil {
		return err
	}
	if !owned(ancestorInfo) || ancestorInfo.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("launch store parent must be owned by the current user and not writable by others")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := cleanDirectory(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 || !owned(info) {
		return fmt.Errorf("launch store must be owned by the current user with mode 0700")
	}
	return nil
}

func privateFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) || !singleLink(info) {
		return nil, fmt.Errorf("launch files must be private, owned, regular files without links")
	}
	return info, nil
}

func readEnvelope(path string) (envelope, error) {
	var e envelope
	info, err := privateFile(path)
	if err != nil {
		return e, err
	}
	f, err := os.Open(path)
	if err != nil {
		return e, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return e, fmt.Errorf("launch file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if err != nil {
		return e, err
	}
	if len(data) > 256*1024 {
		return e, fmt.Errorf("launch file is too large")
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("invalid launch file: %w", err)
	}
	if !idPattern.MatchString(e.Request.ID) || e.Request.ID != e.Receipt.ID || filepath.Base(path) != e.Request.ID+".json" {
		return e, fmt.Errorf("launch receipt identity mismatch")
	}
	if e.Receipt.Root != e.Request.Root {
		return e, fmt.Errorf("launch receipt project mismatch")
	}
	switch e.Receipt.State {
	case "dispatched", "unknown", "unavailable", "started", "finished", "failed":
	default:
		return e, fmt.Errorf("invalid launch receipt state")
	}
	return e, nil
}

func writeEnvelope(path string, e envelope) error {
	if _, err := privateFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".launch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
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
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func manualCommand(r Request, stateDirectory string) string {
	// Manual fallback must compete for the same receipt as a delayed native
	// helper. Replaying the original run argv could allocate a duplicate job.
	return quote(r.Executable) + " launch execute --id " + r.ID + " --state-dir " + quote(stateDirectory)
}
