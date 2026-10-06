// Package runprogress retains a private, bounded presentation stream for run tails.
package runprogress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const textLimit = 8 * 1024
const readLimit = 64 * 1024
const recordLimit = 64 * 1024

type Event struct {
	Version  int       `json:"version"`
	Time     time.Time `json:"time"`
	RunID    string    `json:"run_id"`
	Source   string    `json:"source"`
	Provider string    `json:"provider,omitempty"`
	Role     string    `json:"role,omitempty"`
	Stage    string    `json:"stage,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
	Text     string    `json:"text"`
}
type Batch struct {
	Cursor string  `json:"cursor"`
	Events []Event `json:"events"`
	Done   bool    `json:"done"`
}

func clean(s string) string {
	// Drop complete ANSI escape sequences as well as other terminal controls.
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 27 {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) {
					c := s[i]
					i++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			} else if i < len(s) && s[i] == ']' {
				i++
				for i < len(s) {
					c := s[i]
					i++
					if c == 7 {
						break
					}
					if c == 27 && i < len(s) && s[i] == '\\' {
						i++
						break
					}
				}
			} else if i < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, b.String())
}
func clip(s string) string {
	s = clean(s)
	if len(s) > textLimit {
		s = strings.ToValidUTF8(s[:textLimit], "") + " [truncated]"
	}
	return s
}
func Format(e Event) string {
	id := clean(e.RunID)
	if len(id) > 8 {
		id = id[:8]
	}
	label := id + " " + clean(e.Source)
	if e.Source == "agent" {
		label += ":" + clean(e.Provider) + " " + clean(e.Role)
	}
	prefix := "[" + strings.ReplaceAll(label, "\n", " ") + "] "
	return prefix + strings.ReplaceAll(strings.TrimRight(clean(e.Text), "\n"), "\n", "\n"+prefix) + "\n"
}
func realDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("progress directory must be an absolute clean path")
	}
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("progress parent must be a real directory")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
func openFile(directory string, flags int) (*os.File, error) {
	return openNamedFile(directory, "progress.jsonl", flags)
}

func openNamedFile(directory, name string, flags int) (*os.File, error) {
	if err := realDirectory(directory); err != nil {
		return nil, err
	}
	parentBefore, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	before, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && (!before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("progress must be a private regular file")
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	actual, statErr := f.Stat()
	if err != nil || statErr != nil || !after.Mode().IsRegular() || !os.SameFile(after, actual) || (before != nil && !os.SameFile(before, actual)) || actual.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, fmt.Errorf("progress file changed while opening")
	}
	if err := realDirectory(directory); err != nil {
		f.Close()
		return nil, err
	}
	parentAfter, err := os.Stat(directory)
	if err != nil || !os.SameFile(parentBefore, parentAfter) {
		f.Close()
		return nil, fmt.Errorf("progress parent changed while opening")
	}
	return f, nil
}

type Recorder struct {
	mu      sync.Mutex
	file    *os.File
	live    io.Writer
	err     error
	liveErr error
}

func Open(directory string, live io.Writer) (*Recorder, error) {
	f, err := openFile(directory, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		return nil, err
	}
	return &Recorder{file: f, live: live}, nil
}
func (r *Recorder) Record(e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	e.Version = 1
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Text = clip(e.Text)
	data, err := json.Marshal(e)
	if err == nil && len(data) > recordLimit {
		err = fmt.Errorf("progress event exceeds bound")
	}
	if err == nil {
		data = append(data, '\n')
		_, err = r.file.Write(data)
	}
	if err != nil {
		r.err = fmt.Errorf("cannot retain run progress: %w", err)
	}
	if err == nil && r.live != nil {
		if _, liveErr := io.WriteString(r.live, Format(e)); liveErr != nil {
			r.liveErr = fmt.Errorf("cannot retain or display run progress: %w", liveErr)
			r.live = nil
		}
	}
	return r.err
}
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.file.Close()
	return errors.Join(r.err, r.liveErr, err)
}
func Read(directory string, offset int64) ([]Event, int64, error) {
	return readEvents(directory, offset, readLimit, false)
}

// ReadBounded limits bytes consumed while keeping the initial recent-tail window.
// One complete record may exceed maxBytes so every positive budget can advance.
func ReadBounded(directory string, offset int64, maxBytes int) ([]Event, int64, error) {
	return readEvents(directory, offset, maxBytes, true)
}
func readEvents(directory string, offset int64, maxBytes int, strict bool) ([]Event, int64, error) {
	lines, next, err := readLines(directory, "progress.jsonl", offset, maxBytes, strict, false)
	if err != nil {
		return nil, next, err
	}
	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, offset, fmt.Errorf("invalid progress record: %w", err)
		}
		if err := validateEvent(e); err != nil {
			return nil, offset, err
		}
		events = append(events, e)
	}
	return events, next, nil
}

// ReadLines safely reads bounded legacy logs. It never consumes a partial line.
func ReadLines(directory, name string, offset int64) ([]string, int64, error) {
	if !legacyName(name) {
		return nil, offset, fmt.Errorf("unsupported progress log name")
	}
	return readLines(directory, name, offset, readLimit, false, false)
}

// ReadLinesBounded is the budgeted form of ReadLines, with identical path checks.
func ReadLinesBounded(directory, name string, offset int64, maxBytes int) ([]string, int64, error) {
	if !legacyName(name) {
		return nil, offset, fmt.Errorf("unsupported progress log name")
	}
	return readLines(directory, name, offset, maxBytes, true, false)
}

// ReadLinesFinal includes a bounded final fragment only for a stopped controller.
func ReadLinesFinal(directory, name string, offset int64, maxBytes int, finished bool) ([]string, int64, error) {
	if !legacyName(name) {
		return nil, offset, fmt.Errorf("unsupported progress log name")
	}
	return readLines(directory, name, offset, maxBytes, true, finished)
}

func validateEvent(e Event) error {
	if e.Version != 1 {
		return fmt.Errorf("unsupported progress version %d", e.Version)
	}
	switch e.Source {
	case "sdlc", "checks", "agent":
	default:
		return fmt.Errorf("invalid progress source")
	}
	if len(e.RunID) > 128 || len(e.Provider) > 64 || len(e.Role) > 64 || len(e.Stage) > 64 || len(e.Text) > textLimit+len(" [truncated]") {
		return fmt.Errorf("progress event fields exceed bounds")
	}
	return nil
}
func legacyName(name string) bool {
	for _, pair := range [][2]string{{"events-", ".jsonl"}, {"checks-", ".log"}, {"diagnostics-", ".log"}} {
		if strings.HasPrefix(name, pair[0]) && strings.HasSuffix(name, pair[1]) {
			number := strings.TrimSuffix(strings.TrimPrefix(name, pair[0]), pair[1])
			if number == "" {
				return false
			}
			for _, r := range number {
				if r < '0' || r > '9' {
					return false
				}
			}
			return true
		}
	}
	return false
}

func readLines(directory, name string, offset int64, maxBytes int, strict, finished bool) ([]string, int64, error) {
	if maxBytes <= 0 {
		return nil, offset, fmt.Errorf("progress read budget must be positive")
	}
	if maxBytes > readLimit {
		maxBytes = readLimit
	}
	f, err := openNamedFile(directory, name, os.O_RDONLY)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset < 0 || offset > info.Size() {
		return nil, offset, fmt.Errorf("progress cursor is invalid or file was truncated")
	}
	start := offset
	if offset == 0 && info.Size() > readLimit {
		start = info.Size() - readLimit
	}
	skipPrefix := start != offset
	if start > 0 {
		var previous [1]byte
		if _, err = f.ReadAt(previous[:], start-1); err != nil {
			return nil, offset, err
		}
		if previous[0] == '\n' {
			skipPrefix = false
		}
		if offset != 0 && previous[0] != '\n' && !(finished && offset == info.Size()) {
			return nil, offset, fmt.Errorf("progress cursor is not a record boundary")
		}
	}
	data := make([]byte, readLimit+recordLimit)
	n, err := f.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return nil, offset, err
	}
	data = data[:n]
	if skipPrefix {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil, offset, fmt.Errorf("progress record exceeds bound")
		}
		start += int64(i + 1)
		data = data[i+1:]
	}
	lines := []string{}
	next := start
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if len(data) > recordLimit {
				return nil, next, fmt.Errorf("progress record exceeds bound")
			}
			if finished && next+int64(len(data)) == info.Size() && (len(lines) == 0 || next-start+int64(len(data)) <= int64(maxBytes)) {
				lines = append(lines, string(data))
				next += int64(len(data))
			}
			break
		}
		if i > recordLimit {
			return nil, next, fmt.Errorf("progress record exceeds bound")
		}
		if strict && len(lines) > 0 && next-start+int64(i+1) > int64(maxBytes) {
			break
		}
		lines = append(lines, string(data[:i]))
		next += int64(i + 1)
		data = data[i+1:]
		if next-start >= int64(maxBytes) {
			break
		}
	}
	return lines, next, nil
}

// Pending reports whether a complete record remains after a cursor.
func Pending(directory string, offset int64) (bool, error) {
	events, _, err := Read(directory, offset)
	return len(events) > 0, err
}

// Err returns the first persistence or live display error.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.err, r.liveErr)
}
