// Package savedwork manages stopped checkpoints in one checkout. It does not
// use the dashboard registry as an inventory: an unregistered journal is saved
// work too.
package savedwork

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

type Run struct{ ID, Root, Reference, Ticket, Directory string }
type RemoveOptions struct {
	Run         string
	All, DryRun bool
}
type Removal struct {
	Runs   []Run
	Series []string
}
type ArchiveResult struct {
	Destination string
	Runs        []Run
}

var identifier = regexp.MustCompile(`^[0-9a-f]{24}$`)

type inventory struct {
	root         string
	runs         []Run
	series       map[string]workseries.State
	references   []string
	featureOwned map[string]bool
}

func component(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.EqualFold(s, ".archive") && utf8.ValidString(s) && !strings.ContainsAny(s, "/\\") && strings.IndexFunc(s, unicode.IsControl) < 0
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("cannot inspect checkout index or ignore rules")
	}
	return string(b), nil
}

func checkout(ctx context.Context, root string) (string, error) {
	s, err := git(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(s)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("saved work requires a real absolute checkout")
	}
	if err := realPath(root); err != nil {
		return "", err
	}
	tracked, err := git(ctx, root, "ls-files", "-z", "--", ":(icase).sdlc/work")
	if err != nil {
		return "", err
	}
	if tracked != "" {
		return "", fmt.Errorf(".sdlc/work contains tracked or staged files; remove them from the Git index")
	}
	ignored, err := git(ctx, root, "check-ignore", "--no-index", ".sdlc/work/")
	if err != nil || strings.TrimSpace(ignored) != ".sdlc/work/" {
		return "", fmt.Errorf("Git must ignore .sdlc/work before managing saved work")
	}
	return root, nil
}

func realPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("saved work path %q must be absolute and clean", path)
	}
	for p := path; ; p = filepath.Dir(p) {
		i, err := os.Lstat(p)
		if err != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("saved work directory %q must be a real directory; symlink paths are unsupported", p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}

func openDirectory(path string) (*os.Root, error) {
	return openCheckedDirectory(path, false)
}

// Reference folders also hold ordinary specifications before any controller
// starts. Read access is allowed, but only their owner may write them. Runtime
// checkpoints, work storage, archive storage and registry directories remain
// subject to the private-directory policy.
func openReference(path string) (*os.Root, error) {
	return openCheckedDirectory(path, true)
}

func directoryMode(info os.FileInfo, reference bool) bool {
	mode := info.Mode().Perm()
	if reference {
		return mode&0700 == 0700 && mode&0022 == 0
	}
	return mode == 0700
}

func openCheckedDirectory(path string, reference bool) (*os.Root, error) {
	if err := realPath(path); err != nil {
		return nil, err
	}
	i, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect saved work directory %q: %w", path, err)
	}
	if !directoryMode(i, reference) || !owned(i, false) {
		if reference {
			return nil, fmt.Errorf("work reference directory %q requires owner rwx permissions, no group or other write permissions, and current-user ownership (mode %04o is unsupported)", path, i.Mode().Perm())
		}
		return nil, fmt.Errorf("saved work directory %q requires mode 0700 and current-user ownership (mode %04o is unsupported)", path, i.Mode().Perm())
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(i, opened) || !directoryMode(opened, reference) || !owned(opened, false) {
		r.Close()
		return nil, fmt.Errorf("saved work directory %q changed while opening", path)
	}
	return r, nil
}

func entries(path string) ([]os.DirEntry, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		for p := filepath.Dir(path); ; p = filepath.Dir(p) {
			if _, err := os.Lstat(p); err == nil {
				return nil, realPath(p)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	r, err := openDirectory(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	// realPath deliberately sanitizes errors, so check absence separately.
	if err != nil {
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	e, err := f.ReadDir(4097)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(e) > 4096 {
		return nil, fmt.Errorf("too many saved work entries")
	}
	sort.Slice(e, func(i, j int) bool { return e[i].Name() < e[j].Name() })
	return e, nil
}

func scan(ctx context.Context, root string) (inventory, error) {
	in := inventory{runs: []Run{}, series: map[string]workseries.State{}, featureOwned: map[string]bool{}}
	var err error
	in.root, err = checkout(ctx, root)
	if err != nil {
		return in, err
	}
	work := filepath.Join(in.root, ".sdlc", "work")
	refs, err := entries(work)
	if err != nil {
		return in, err
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return in, err
		}
		if strings.EqualFold(ref.Name(), ".archive") {
			continue
		}
		path := filepath.Join(work, ref.Name())
		if !component(ref.Name()) {
			return in, fmt.Errorf("unsafe work reference entry %q", path)
		}
		if !ref.IsDir() {
			info, err := ref.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !owned(info, true) {
				return in, fmt.Errorf("work reference entry %q must be an owned regular file or a safe reference directory; links and writable entries are unsupported", path)
			}
			continue
		}
		r, err := openReference(path)
		if err != nil {
			return in, err
		}
		r.Close()
		in.references = append(in.references, ref.Name())
		seriesDir := filepath.Join(path, "series")
		if _, err := os.Lstat(seriesDir); err == nil {
			r, err := openDirectory(seriesDir)
			if err != nil {
				return in, err
			}
			if _, err := privateFile(r, "journal.json"); err != nil {
				r.Close()
				return in, err
			}
			r.Close()
			s, err := workseries.Load(seriesDir)
			if err != nil || s.Plan.Root != in.root || s.Plan.Reference != ref.Name() {
				return in, fmt.Errorf("invalid saved feature checkpoint in %q", seriesDir)
			}
			in.series[seriesDir] = s
		} else if !os.IsNotExist(err) {
			return in, err
		}
		tickets, err := entries(filepath.Join(path, "runs"))
		if err != nil {
			return in, err
		}
		for _, ticket := range tickets {
			if !component(ticket.Name()) || !ticket.IsDir() {
				return in, fmt.Errorf("unsafe saved run namespace %q", filepath.Join(path, "runs", ticket.Name()))
			}
			namespace := filepath.Join(path, "runs", ticket.Name())
			runs, err := entries(namespace)
			if err != nil {
				return in, err
			}
			for _, entry := range runs {
				if !identifier.MatchString(entry.Name()) || !entry.IsDir() {
					return in, fmt.Errorf("invalid saved run directory %q", filepath.Join(namespace, entry.Name()))
				}
				directory := filepath.Join(namespace, entry.Name())
				r, err := openDirectory(directory)
				if err != nil {
					return in, err
				}
				// Preparation can stop after creating its canonical directory but
				// before writing any checkpoint. Only a genuinely empty directory
				// is absent saved work; even a lone lock or invalid journal still
				// requires inspection. Creators hold the runtime shared lease
				// before mkdir, and mutations repeat this scan under its writer lock.
				f, err := r.Open(".")
				if err != nil {
					r.Close()
					return in, err
				}
				contents, readErr := f.ReadDir(1)
				f.Close()
				if readErr != nil && readErr != io.EOF {
					r.Close()
					return in, fmt.Errorf("cannot inspect saved run directory %q: %w", directory, readErr)
				}
				if len(contents) == 0 && readErr == io.EOF {
					r.Close()
					continue
				}
				if _, err := privateFile(r, "journal.json"); err != nil && !os.IsNotExist(err) {
					r.Close()
					return in, err
				}
				r.Close()
				j, err := workrun.Load(directory)
				run := Run{ID: entry.Name(), Root: in.root, Reference: ref.Name(), Directory: directory}
				if err == nil {
					run.Ticket = j.Plan.Ticket
					if j.ID != run.ID || j.Plan.Root != in.root || j.Plan.Reference != run.Reference {
						return in, fmt.Errorf("saved run identity does not match its location %q", directory)
					}
				} else {
					p, e := runstatus.LoadPreparation(directory)
					if e != nil {
						return in, fmt.Errorf("invalid saved run or preparation checkpoint in %q", directory)
					}
					run.Ticket = filepath.ToSlash(filepath.Join(".sdlc", "work", p.Reference, "tickets", p.Ticket))
					in.featureOwned[run.Directory] = p.FeatureOwned
					if p.ID != run.ID || p.Root != in.root || p.Reference != run.Reference {
						return in, fmt.Errorf("saved preparation identity does not match its location %q", directory)
					}
				}
				ticketFile := filepath.Base(run.Ticket)
				if !component(ticketFile) || run.Ticket != filepath.ToSlash(filepath.Join(".sdlc", "work", run.Reference, "tickets", ticketFile)) || strings.TrimSuffix(ticketFile, ".md") != ticket.Name() {
					return in, fmt.Errorf("saved run ticket identity does not match its namespace %q", namespace)
				}
				in.runs = append(in.runs, run)
			}
		}
	}
	return in, nil
}

func Discover(ctx context.Context, root string) ([]Run, error) {
	in, err := scan(ctx, root)
	return in.runs, err
}

func selectRemoval(in inventory, options RemoveOptions) (Removal, error) {
	result := Removal{Runs: []Run{}, Series: []string{}}
	if options.All == (options.Run != "") {
		return result, fmt.Errorf("select exactly one run or all saved work")
	}
	if options.All {
		result.Runs = in.runs
	} else {
		ids := []string{}
		for _, r := range in.runs {
			ids = append(ids, r.ID)
		}
		id, err := workrun.SelectRunID(ids, options.Run)
		if err != nil {
			return result, err
		}
		for _, r := range in.runs {
			if r.ID == id {
				result.Runs = append(result.Runs, r)
			}
		}
	}
	for path, s := range in.series {
		selected := options.All
		for _, r := range result.Runs {
			if r.Reference == s.Plan.Reference && in.featureOwned[r.Directory] {
				selected = true
			}
		}
		for _, owned := range s.Results {
			for _, r := range result.Runs {
				if owned.RunID == r.ID {
					selected = true
				}
			}
		}
		if selected {
			result.Series = append(result.Series, path)
		}
	}
	sort.Strings(result.Series)
	return result, nil
}

// held retains anchored directories and lock inodes until the mutation ends.
type held struct {
	dirs          map[string]*os.Root
	references    map[string]bool
	locks         map[string]*os.File
	registrations []registration
}
type registration struct {
	root *os.Root
	name string
	info os.FileInfo
}

func (h *held) close() {
	for _, f := range h.locks {
		f.Close()
	}
	for _, r := range h.dirs {
		r.Close()
	}
}
func (h *held) directory(path string) (*os.Root, error) {
	return h.checkedDirectory(path, false)
}

func (h *held) reference(path string) (*os.Root, error) {
	return h.checkedDirectory(path, true)
}

func (h *held) checkedDirectory(path string, reference bool) (*os.Root, error) {
	if r := h.dirs[path]; r != nil {
		return r, nil
	}
	r, err := openCheckedDirectory(path, reference)
	if err != nil {
		return nil, err
	}
	h.dirs[path] = r
	if reference {
		if h.references == nil {
			h.references = map[string]bool{}
		}
		h.references[path] = true
	}
	return r, nil
}
func privateFile(r *os.Root, name string) (os.FileInfo, error) {
	i, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || !owned(i, true) {
		return nil, fmt.Errorf("saved work metadata or lock %q must be mode 0600, owned, regular and single-linked", filepath.Join(r.Name(), name))
	}
	return i, nil
}
func (h *held) lock(directory, name string) error {
	r, err := h.directory(directory)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, name)
	if h.locks[path] != nil {
		return nil
	}
	if _, err := r.Lstat(name); os.IsNotExist(err) {
		f, e := r.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		f.Close()
	} else if err != nil {
		return err
	}
	i, err := privateFile(r, name)
	if err != nil {
		return err
	}
	f, err := filelock.Acquire(path)
	if err != nil {
		return fmt.Errorf("saved work controller or registration is busy: %w", err)
	}
	opened, e := f.Stat()
	current, ce := privateFile(r, name)
	if e != nil || ce != nil || !os.SameFile(i, opened) || !os.SameFile(i, current) {
		f.Close()
		return fmt.Errorf("saved work lock changed")
	}
	h.locks[path] = f
	return nil
}
func (h *held) validate() error {
	for path, r := range h.dirs {
		i, e := os.Lstat(path)
		opened, oe := r.Stat(".")
		if e != nil || oe != nil || realPath(path) != nil || !os.SameFile(i, opened) || !directoryMode(i, h.references[path]) || !owned(i, false) {
			return fmt.Errorf("saved work directory %q changed or has unsupported permissions", path)
		}
	}
	for path, f := range h.locks {
		r := h.dirs[filepath.Dir(path)]
		i, e := privateFile(r, filepath.Base(path))
		opened, oe := f.Stat()
		if e != nil || oe != nil || !os.SameFile(i, opened) {
			return fmt.Errorf("saved work lock changed")
		}
	}
	for _, reg := range h.registrations {
		i, e := privateFile(reg.root, reg.name)
		if e != nil || !os.SameFile(i, reg.info) {
			return fmt.Errorf("saved run registration changed")
		}
	}
	return nil
}

func (h *held) registry(stateDir string, runs []Run) error {
	path := filepath.Join(stateDir, "runs")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	r, err := h.directory(path)
	if err != nil {
		return err
	}
	for _, run := range runs {
		name := run.ID + ".json"
		if _, err := r.Lstat(name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := h.lock(path, run.ID+".lock"); err != nil {
			return err
		}
		i, err := privateFile(r, name)
		if err != nil || i.Size() > 1024*1024 {
			return fmt.Errorf("unsafe saved run registration")
		}
		f, err := r.Open(name)
		if err != nil {
			return err
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(i, opened) {
			f.Close()
			return fmt.Errorf("saved run registration changed")
		}
		var record struct {
			Version     int    `json:"version"`
			ID          string `json:"id"`
			Directory   string `json:"directory"`
			Root        string `json:"root"`
			Reference   string `json:"reference"`
			Ticket      string `json:"ticket"`
			Preparation bool   `json:"preparation,omitempty"`
		}
		d := json.NewDecoder(io.LimitReader(f, 1024*1024+1))
		d.DisallowUnknownFields()
		err = d.Decode(&record)
		end := d.Decode(new(any))
		f.Close()
		if err != nil || end != io.EOF || record.Version != 1 || record.ID != run.ID || record.Directory != run.Directory || record.Root != run.Root || record.Reference != run.Reference || record.Ticket != run.Ticket {
			return fmt.Errorf("saved run registration identity is unsafe")
		}
		h.registrations = append(h.registrations, registration{r, name, i})
	}
	return nil
}

func ownership(in inventory, result Removal) []Run {
	runs := append([]Run{}, result.Runs...)
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.Directory] = true
	}
	for _, path := range result.Series {
		for _, record := range in.series[path].Results {
			if record.RunID == "" {
				continue
			}
			for _, r := range in.runs {
				if r.ID == record.RunID && r.Reference == in.series[path].Plan.Reference {
					if !seen[r.Directory] {
						runs = append(runs, r)
						seen[r.Directory] = true
					}
				}
			}
			// A missing run has no controller lock to own. A directory claiming
			// an external location is never used to acquire ownership.
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Directory < runs[j].Directory })
	return runs
}

func acquire(stateDir string, in inventory, result Removal) (*held, error) {
	h := &held{dirs: map[string]*os.Root{}, locks: map[string]*os.File{}}
	fail := func(err error) (*held, error) { h.close(); return nil, err }
	if err := ensureState(stateDir); err != nil {
		return fail(err)
	}
	if err := h.lock(stateDir, "runtime-build.lock"); err != nil {
		return fail(err)
	}
	for _, path := range result.Series {
		if err := h.lock(path, "series.lock"); err != nil {
			return fail(err)
		}
	}
	for _, run := range ownership(in, result) {
		if err := h.lock(run.Directory, "run.lock"); err != nil {
			return fail(err)
		}
		r := h.dirs[run.Directory]
		if info, err := privateFile(r, "activity.json"); err == nil && info.Size() <= 1024*1024 {
			f, err := r.Open("activity.json")
			if err != nil {
				return fail(err)
			}
			opened, statErr := f.Stat()
			var activity runstatus.Snapshot
			decodeErr := json.NewDecoder(io.LimitReader(f, 1024*1024+1)).Decode(&activity)
			f.Close()
			if statErr != nil || !os.SameFile(info, opened) {
				return fail(fmt.Errorf("saved controller activity changed"))
			}
			if decodeErr == nil && activity.Version == 1 && activity.ID == run.ID && identifier.MatchString(activity.ControllerID) && !activity.Stopped && !activity.HeartbeatAt.IsZero() && time.Since(activity.HeartbeatAt) <= runstatus.StaleAfter {
				return fail(fmt.Errorf("saved run reports a live controller; stop it before managing its checkpoint"))
			}
		}
	}
	for _, run := range result.Runs {
		for p := filepath.Dir(run.Directory); p != filepath.Join(in.root, ".sdlc"); p = filepath.Dir(p) {
			reference := p == filepath.Join(in.root, ".sdlc", "work", run.Reference)
			if _, err := h.checkedDirectory(p, reference); err != nil {
				return fail(err)
			}
		}
	}
	for _, p := range result.Series {
		if _, err := h.reference(filepath.Dir(p)); err != nil {
			return fail(err)
		}
	}
	if err := h.registry(stateDir, result.Runs); err != nil {
		return fail(err)
	}
	if err := h.validate(); err != nil {
		return fail(err)
	}
	return h, nil
}

// A checkout can have saved journals before it has a dashboard registry. Only
// real mutations initialize the installation lock directory. Create missing
// components through retained roots rather than following path symlinks.
func ensureState(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("saved work requires a real absolute state directory")
	}
	missing := []string{}
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
	if err := realPath(ancestor); err != nil {
		return err
	}
	r, err := os.OpenRoot(ancestor)
	if err != nil {
		return err
	}
	defer func() { r.Close() }()
	for i := len(missing) - 1; i >= 0; i-- {
		name := missing[i]
		if err := r.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := r.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !owned(info, false) {
			return fmt.Errorf("state paths must be private owned real directories")
		}
		next, err := r.OpenRoot(name)
		if err != nil {
			return err
		}
		opened, e := next.Stat(".")
		if e != nil || !os.SameFile(info, opened) {
			next.Close()
			return fmt.Errorf("state directory changed while creating")
		}
		r.Close()
		r = next
	}
	check, err := openDirectory(path)
	if err != nil {
		return err
	}
	return check.Close()
}

func (h *held) unregister() error {
	for _, reg := range h.registrations {
		if err := reg.root.Remove(reg.name); err != nil {
			return fmt.Errorf("cannot remove saved run registration; retry before changing runtime")
		}
	}
	return nil
}

// Retain checkpoint metadata until the contents have been removed. A failed
// workspace deletion therefore leaves a canonical checkpoint selectable for
// retry. Delete through the opened target, never a replacement directory.
func (h *held) removeDirectory(path string) (err error) {
	target := h.dirs[path]
	parent := h.dirs[filepath.Dir(path)]
	if target == nil || parent == nil {
		return fmt.Errorf("saved work deletion has no anchored ownership")
	}
	info, err := parent.Lstat(filepath.Base(path))
	opened, e := target.Stat(".")
	if err != nil || e != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("saved work directory changed before removal")
	}
	type checkpoint struct {
		file *os.File
		size int64
	}
	checkpoints := map[string]checkpoint{}
	defer func() {
		for name, saved := range checkpoints {
			if err != nil {
				if _, statErr := target.Lstat(name); os.IsNotExist(statErr) {
					f, restoreErr := target.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if restoreErr == nil {
						_, restoreErr = io.Copy(f, io.NewSectionReader(saved.file, 0, saved.size))
						if restoreErr == nil {
							restoreErr = f.Sync()
						}
						if closeErr := f.Close(); restoreErr == nil {
							restoreErr = closeErr
						}
					}
					if restoreErr != nil {
						err = fmt.Errorf("saved work deletion failed and checkpoint restoration requires inspection")
					}
				}
			}
			saved.file.Close()
		}
	}()
	for _, name := range []string{"journal.json", "preparation.json"} {
		info, e := privateFile(target, name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || info.Size() > 1024*1024 {
			return fmt.Errorf("unsafe saved checkpoint during removal")
		}
		f, e := target.Open(name)
		if e != nil {
			return e
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) {
			f.Close()
			return fmt.Errorf("saved checkpoint changed during removal")
		}
		checkpoints[name] = checkpoint{f, info.Size()}
	}
	f, err := target.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "journal.json", "preparation.json", "run.lock", "series.lock":
			continue
		}
		if err := target.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	for _, name := range []string{"run.lock", "series.lock", "preparation.json", "journal.json"} {
		if err := target.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	current, err := parent.Lstat(filepath.Base(path))
	if err != nil || !os.SameFile(current, opened) {
		return fmt.Errorf("saved work directory changed during removal")
	}
	return parent.Remove(filepath.Base(path))
}

func Remove(ctx context.Context, stateDir, root string, options RemoveOptions) (Removal, error) {
	in, err := scan(ctx, root)
	if err != nil {
		return Removal{}, err
	}
	result, err := selectRemoval(in, options)
	for _, run := range result.Runs {
		if contains(run.Directory, stateDir) {
			return result, fmt.Errorf("saved work contains installation state; move the state directory before purging")
		}
	}
	for _, path := range result.Series {
		if contains(path, stateDir) {
			return result, fmt.Errorf("saved feature contains installation state; move the state directory before purging")
		}
	}
	if err != nil || options.DryRun {
		return result, err
	}
	h, err := acquire(stateDir, in, result)
	if err != nil {
		return result, err
	}
	defer h.close()
	// Re-read under the installation writer lock: creators take its shared
	// lease before creating checkpoints, so this batch cannot grow underneath us.
	current, err := scan(ctx, in.root)
	if err != nil {
		return result, err
	}
	next, err := selectRemoval(current, options)
	if err != nil {
		return result, err
	}
	if !sameRemoval(result, next) {
		return result, fmt.Errorf("saved work changed; preview and retry")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := h.validate(); err != nil {
		return result, err
	}
	// Deregister first: a failed deletion leaves discoverable journals which
	// can be selected again, rather than unavailable runtime registrations.
	if err := h.unregister(); err != nil {
		return result, err
	}
	for _, path := range result.Series {
		if err := h.removeDirectory(path); err != nil {
			return result, fmt.Errorf("cannot remove saved feature checkpoint: %w", err)
		}
	}
	for _, run := range result.Runs {
		if err := h.removeDirectory(run.Directory); err != nil {
			return result, fmt.Errorf("cannot remove saved run directory: %w", err)
		}
	}
	return result, nil
}

func contains(parent, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sameRemoval(a, b Removal) bool {
	if len(a.Runs) != len(b.Runs) || len(a.Series) != len(b.Series) {
		return false
	}
	for i := range a.Runs {
		if a.Runs[i] != b.Runs[i] {
			return false
		}
	}
	for i := range a.Series {
		if a.Series[i] != b.Series[i] {
			return false
		}
	}
	return true
}

func Archive(ctx context.Context, stateDir, root, reference string, dryRun bool) (ArchiveResult, error) {
	result := ArchiveResult{Runs: []Run{}}
	if !component(reference) {
		return result, fmt.Errorf("select a valid work reference")
	}
	in, err := scan(ctx, root)
	if err != nil {
		return result, err
	}
	found := false
	for _, ref := range in.references {
		if ref == reference {
			found = true
		}
	}
	if !found {
		return result, fmt.Errorf("selected work reference is unavailable")
	}
	refPath := filepath.Join(in.root, ".sdlc", "work", reference)
	if contains(refPath, stateDir) {
		return result, fmt.Errorf("work reference contains installation state; move the state directory before archiving")
	}
	selection := Removal{Runs: []Run{}, Series: []string{}}
	for _, run := range in.runs {
		if run.Reference == reference {
			selection.Runs = append(selection.Runs, run)
		}
	}
	if _, ok := in.series[filepath.Join(refPath, "series")]; ok {
		selection.Series = append(selection.Series, filepath.Join(refPath, "series"))
	}
	result.Runs = selection.Runs
	id, err := workrun.NewID()
	if err != nil {
		return result, err
	}
	name := reference + "-" + time.Now().UTC().Format("20060102T150405Z") + "-" + id
	archivePath := filepath.Join(in.root, ".sdlc", "work", ".archive")
	result.Destination = filepath.Join(archivePath, name)
	if _, err := os.Lstat(archivePath); err == nil {
		r, e := openDirectory(archivePath)
		if e != nil {
			return result, e
		}
		r.Close()
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if dryRun {
		return result, nil
	}
	h, err := acquire(stateDir, in, selection)
	if err != nil {
		return result, err
	}
	defer h.close()
	workPath := filepath.Dir(refPath)
	work, err := h.directory(workPath)
	if err != nil {
		return result, err
	}
	if _, err := h.reference(refPath); err != nil {
		return result, err
	}
	current, err := scan(ctx, in.root)
	if err != nil {
		return result, err
	}
	next := Removal{Runs: []Run{}, Series: []string{}}
	for _, r := range current.runs {
		if r.Reference == reference {
			next.Runs = append(next.Runs, r)
		}
	}
	if _, ok := current.series[filepath.Join(refPath, "series")]; ok {
		next.Series = append(next.Series, filepath.Join(refPath, "series"))
	}
	if !sameRemoval(selection, next) {
		return result, fmt.Errorf("saved work changed; preview and retry")
	}
	if _, err := work.Lstat(".archive"); os.IsNotExist(err) {
		if err := work.Mkdir(".archive", 0700); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	archive, err := h.directory(archivePath)
	if err != nil {
		return result, err
	}
	if _, err := archive.Lstat(name); !os.IsNotExist(err) {
		return result, fmt.Errorf("archive destination already exists or is unsafe")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := h.validate(); err != nil {
		return result, err
	}
	if err := h.unregister(); err != nil {
		return result, err
	}
	// Rename relative to one retained work root. No contents are followed,
	// including workspace links and preserved ticket/input files.
	if err := work.Rename(reference, filepath.Join(".archive", name)); err != nil {
		return result, fmt.Errorf("cannot archive work reference; its saved journals remain discoverable for retry")
	}
	return result, nil
}
