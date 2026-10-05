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
	"sort"
	"strconv"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
)

const maximumMetadata = 64 * 1024

type sidecar struct {
	Version int `json:"version"`
	Tickets map[string]struct {
		DependsOn []string `json:"depends_on"`
		Priority  int      `json:"priority"`
		Touches   []string `json:"touches"`
	} `json:"tickets"`
}

// Discover reads ticket names and optional private scheduling metadata. Ticket
// contents are deliberately never opened.
func Discover(ctx context.Context, directory, reference, base string) (Plan, error) {
	work, err := project.InspectWork(ctx, directory, reference)
	if err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(base) == "" {
		return Plan{}, errors.New("series base must not be empty")
	}
	p := Plan{Version: 1, Root: work.Root, Reference: reference, Base: base}
	byName := map[string]string{}
	for _, file := range work.Tickets {
		name := filepath.Base(file)
		byName[name] = name
		p.Tickets = append(p.Tickets, Ticket{File: name, DependsOn: []string{}, Touches: []string{}})
	}
	metadata, err := readSidecar(filepath.Join(work.Root, ".sdlc", "work", reference, "plan.json"))
	if err != nil {
		return Plan{}, err
	}
	if metadata != nil {
		for name, item := range metadata.Tickets {
			file, ok := byName[name]
			if !ok {
				return Plan{}, fmt.Errorf("plan metadata names unknown ticket %q", name)
			}
			for i := range p.Tickets {
				if p.Tickets[i].File == file {
					p.Tickets[i].DependsOn = append([]string(nil), item.DependsOn...)
					p.Tickets[i].Priority = item.Priority
					p.Tickets[i].Touches = append([]string(nil), item.Touches...)
				}
			}
		}
	}
	if err := validatePlan(&p); err != nil {
		return Plan{}, err
	}
	p.DefinitionSHA = definitionSHA(p)
	return p, nil
}

func readSidecar(path string) (*sidecar, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumMetadata {
		return nil, errors.New("private series plan must be a regular file of at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read private series plan")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private series plan changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, maximumMetadata+1))
	if err != nil || len(b) > maximumMetadata {
		return nil, errors.New("private series plan exceeds 64 KiB")
	}
	var s sidecar
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Version != 1 {
		return nil, errors.New("private series plan must be version 1 JSON")
	}
	if s.Tickets == nil {
		s.Tickets = map[string]struct {
			DependsOn []string `json:"depends_on"`
			Priority  int      `json:"priority"`
			Touches   []string `json:"touches"`
		}{}
	}
	return &s, nil
}

func validatePlan(p *Plan) error {
	if p.Version != 1 || p.Reference == "" || p.Base == "" || len(p.Tickets) == 0 {
		return errors.New("invalid series plan")
	}
	names := map[string]bool{}
	for _, t := range p.Tickets {
		name := filepath.Base(t.File)
		prefix, title, hasDash := strings.Cut(name, "-")
		id, numberErr := strconv.ParseUint(prefix, 10, 64)
		if name == "." || name != t.File || names[name] || !hasDash || id == 0 || numberErr != nil || title == "" || !strings.HasSuffix(name, ".md") {
			return errors.New("series tickets must have unique safe filenames")
		}
		names[name] = true
	}
	for _, t := range p.Tickets {
		seen := map[string]bool{}
		for _, d := range t.DependsOn {
			if !names[d] || d == filepath.Base(t.File) || seen[d] {
				return fmt.Errorf("ticket %s has invalid dependency %q", filepath.Base(t.File), d)
			}
			seen[d] = true
		}
		seen = map[string]bool{}
		for _, touch := range t.Touches {
			if !safeTouch(touch) || seen[touch] {
				return fmt.Errorf("ticket %s has invalid touched path %q", filepath.Base(t.File), touch)
			}
			seen[touch] = true
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	byName := ticketMap(p.Tickets)
	visit = func(name string) error {
		if visiting[name] {
			return errors.New("series dependencies contain a cycle")
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, dep := range byName[name].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		return nil
	}
	for name := range byName {
		if err := visit(name); err != nil {
			return err
		}
	}
	// Kahn ordering ensures every dependency precedes its child. Priority only
	// selects among vertices that are simultaneously available.
	remaining := map[string]int{}
	children := map[string][]string{}
	for _, ticket := range p.Tickets {
		name := filepath.Base(ticket.File)
		remaining[name] = len(ticket.DependsOn)
		for _, dep := range ticket.DependsOn {
			children[dep] = append(children[dep], name)
		}
	}
	available := []Ticket{}
	for _, ticket := range p.Tickets {
		if remaining[filepath.Base(ticket.File)] == 0 {
			available = append(available, ticket)
		}
	}
	less := func(i, j int) bool {
		if available[i].Priority != available[j].Priority {
			return available[i].Priority < available[j].Priority
		}
		return numericName(available[i].File) < numericName(available[j].File)
	}
	ordered := make([]Ticket, 0, len(p.Tickets))
	byTicket := ticketMap(p.Tickets)
	for len(available) > 0 {
		sort.Slice(available, less)
		ticket := available[0]
		available = available[1:]
		ordered = append(ordered, ticket)
		for _, child := range children[filepath.Base(ticket.File)] {
			remaining[child]--
			if remaining[child] == 0 {
				available = append(available, byTicket[child])
			}
		}
	}
	if len(ordered) != len(p.Tickets) {
		return errors.New("series dependencies contain a cycle")
	}
	p.Tickets = ordered
	return nil
}

func numericName(file string) uint64 {
	n, _, _ := strings.Cut(filepath.Base(file), "-")
	v, _ := strconv.ParseUint(n, 10, 64)
	return v
}
func ticketMap(ts []Ticket) map[string]Ticket {
	out := map[string]Ticket{}
	for _, t := range ts {
		out[filepath.Base(t.File)] = t
	}
	return out
}
func safeTouch(path string) bool {
	return path != "" && filepath.ToSlash(filepath.Clean(path)) == path && !strings.HasPrefix(path, "/") && path != "." && path != ".." && !strings.HasPrefix(path, "../") && !strings.Contains(path, "\\")
}
func overlaps(a, b Ticket) bool {
	for _, x := range a.Touches {
		for _, y := range b.Touches {
			if x == y || strings.HasPrefix(x, y+"/") || strings.HasPrefix(y, x+"/") {
				return true
			}
		}
	}
	return false
}
func definitionSHA(p Plan) string {
	type normalized struct {
		Version   int      `json:"version"`
		Reference string   `json:"reference"`
		Base      string   `json:"base"`
		Tickets   []Ticket `json:"tickets"`
	}
	b, _ := json.Marshal(normalized{p.Version, p.Reference, p.Base, p.Tickets})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
