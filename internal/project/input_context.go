package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// BlockerDeclaration reads only explicit planning metadata, never ordinary links.
type BlockerDeclaration struct {
	Present   bool     `json:"present"`
	Completed bool     `json:"completed"`
	Parents   []string `json:"parents"`
}

func ParseTicketBlockers(data []byte) (BlockerDeclaration, error) {
	d := BlockerDeclaration{Parents: []string{}}
	lines := strings.Split(stripMarkdownComments(string(data)), "\n")
	value := ""
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			break
		}
		if strings.HasPrefix(line, "**Status:**") {
			d.Completed = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "**Status:**")), "completed")
		}
		if !strings.HasPrefix(line, "**Blocked by:**") {
			continue
		}
		if d.Present {
			return d, errors.New("duplicate Blocked by metadata")
		}
		d.Present = true
		value = strings.TrimSpace(strings.TrimPrefix(line, "**Blocked by:**"))
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimSpace(next)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "**") {
				break
			}
			if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(next, " ") || strings.HasPrefix(next, "\t") {
				value += " " + trimmed
			} else {
				break
			}
		}
	}
	if !d.Present {
		return d, nil
	}
	targets := markdownTargets(value)
	if strings.EqualFold(value, "None") || strings.HasPrefix(strings.ToLower(value), "none (") {
		if len(targets) > 0 {
			return d, errors.New("Blocked by cannot combine None with blocker links")
		}
		return d, nil
	}
	if len(targets) == 0 {
		return d, errors.New("Blocked by must be None or relative links to exact ticket filenames")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" {
			return d, errors.New("blockers must be relative ticket links")
		}
		name, err := url.PathUnescape(u.EscapedPath())
		if err != nil || name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".md") {
			return d, errors.New("blockers must name sibling ticket files")
		}
		if !seen[name] {
			d.Parents = append(d.Parents, name)
			seen[name] = true
		}
	}
	return d, nil
}
func TicketBlockerDeclarations(ctx context.Context, root, reference string, selected ...string) (map[string]BlockerDeclaration, error) {
	work, err := InspectWork(ctx, root, reference)
	if err != nil {
		return nil, err
	}
	result := map[string]BlockerDeclaration{}
	for _, path := range work.Tickets {
		if len(selected) > 0 && selected[0] != "" && filepath.Base(path) != selected[0] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		full := filepath.Join(work.Root, filepath.FromSlash(path))
		if err := directoryOrMissing(filepath.Dir(full)); err != nil {
			return nil, err
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("ticket metadata requires regular files")
		}
		if info.Size() > requirementBytes {
			return nil, errors.New("ticket metadata exceeds requirement input size")
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		d, err := ParseTicketBlockers(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		result[filepath.Base(path)] = d
	}
	return result, nil
}

var contextTicketName = regexp.MustCompile(`^[0-9]+-[^/]+\.md$`)

// RequirementContext describes provenance. It never grants implementation scope.
type RequirementContext struct {
	Path         string   `json:"path"`
	Role         string   `json:"role"`
	ReferencedBy []string `json:"referenced_by,omitempty"`
}

func DescribeRequirementContext(root, selected string, inputs []string) ([]RequirementContext, error) {
	roles := map[string]string{}
	known := map[string]bool{}
	for _, path := range inputs {
		known[path] = true
		roles[path] = "supporting_context"
	}
	selectedData := []byte{}
	var err error
	if known[selected] {
		selectedData, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(selected)))
	}
	if err != nil {
		return nil, err
	}
	declaration, err := ParseTicketBlockers(selectedData)
	if err != nil {
		return nil, err
	}
	parents := map[string]bool{}
	for _, name := range declaration.Parents {
		parents[name] = true
	}
	for _, path := range inputs {
		if path == selected {
			roles[path] = "selected_ticket"
			continue
		}
		if filepath.Dir(path) == filepath.Dir(selected) && contextTicketName.MatchString(filepath.Base(path)) {
			roles[path] = "boundary_context"
			if parents[filepath.Base(path)] {
				roles[path] = "prerequisite_context"
			}
		}
	}
	references := map[string]map[string]bool{}
	for _, path := range inputs {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		for _, target := range markdownTargets(string(data)) {
			u, err := url.Parse(target)
			if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
				continue
			}
			decoded, err := url.PathUnescape(u.EscapedPath())
			if err != nil {
				continue
			}
			candidate := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(decoded))))
			if !known[candidate] || candidate == path {
				continue
			}
			if references[candidate] == nil {
				references[candidate] = map[string]bool{}
			}
			references[candidate][path] = true
		}
	}
	result := make([]RequirementContext, 0, len(inputs))
	for _, path := range inputs {
		item := RequirementContext{Path: path, Role: roles[path]}
		for from := range references[path] {
			item.ReferencedBy = append(item.ReferencedBy, from)
		}
		sort.Strings(item.ReferencedBy)
		result = append(result, item)
	}
	return result, nil
}
