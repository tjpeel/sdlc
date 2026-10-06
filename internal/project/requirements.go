package project

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const requirementBytes = 16 * 1024 * 1024
const requirementFiles = 256

// ResolveRequirements validates exact seeds and follows Markdown document links
// within the selected work reference. Its result is suitable for freezing.
func ResolveRequirements(ctx context.Context, root, reference string, selected []string) ([]string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	prefix := ".sdlc/work/" + reference + "/"
	queue := append([]string{}, selected...)
	explicit := map[string]bool{}
	for _, path := range selected {
		explicit[path] = true
	}
	seen := map[string]bool{}
	var result []string
	var total int64
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		if !launchPath(path) || requirementExcluded(path) {
			return nil, fmt.Errorf("unsafe requirement input %q; select an ordinary project document with --input", path)
		}
		if len(result) >= requirementFiles {
			return nil, fmt.Errorf("requirement graph exceeds %d files", requirementFiles)
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := directoryOrMissing(filepath.Dir(full)); err != nil {
			return nil, fmt.Errorf("requirement %q: %w", path, err)
		}
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("requirement %q must exist as a regular file; restore it or correct the referring link", path)
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			f.Close()
			return nil, fmt.Errorf("requirement %q changed while opening", path)
		}
		data, err := io.ReadAll(io.LimitReader(f, requirementBytes-total+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > requirementBytes {
			return nil, fmt.Errorf("requirement graph exceeds 16 MiB")
		}
		seen[path] = true
		result = append(result, path)
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			continue
		}
		for _, target := range markdownTargets(string(data)) {
			if externalRequirementTarget.MatchString(target) || strings.HasPrefix(target, "//") {
				continue
			}
			u, err := url.Parse(target)
			if err != nil {
				return nil, fmt.Errorf("requirement %q links to invalid target %q; correct the link", path, target)
			}
			if u.Scheme != "" || u.Host != "" || u.Path == "" {
				continue
			}
			decoded, err := url.PathUnescape(u.EscapedPath())
			if err != nil {
				return nil, fmt.Errorf("requirement %q links to invalid target %q", path, target)
			}
			candidate := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(decoded))))
			// Documents outside the selected process folder remain explicit selections.
			if !strings.EqualFold(filepath.Ext(decoded), ".md") {
				continue
			}
			if filepath.IsAbs(decoded) || strings.Contains(decoded, "\\") || requirementExcluded(candidate) {
				return nil, fmt.Errorf("requirement %q links to unsafe target %q; correct the link or select an ordinary document with --input", path, target)
			}
			if !strings.HasPrefix(candidate, prefix) {
				if explicit[candidate] {
					continue
				}
				if strings.HasPrefix(candidate, ".sdlc/work/") || !launchPath(candidate) {
					return nil, fmt.Errorf("requirement %q links outside selected reference to %q; select the document explicitly with --input", path, target)
				}
				continue
			}
			if err := directoryOrMissing(filepath.Dir(filepath.Join(root, filepath.FromSlash(candidate)))); err != nil {
				return nil, fmt.Errorf("requirement %q links to unsafe target %q; restore a regular document without filesystem links", path, target)
			}
			if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(candidate))); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("requirement %q links to missing target %q; restore it or correct the link", path, target)
			}
			queue = append(queue, candidate)
		}
	}
	return result, nil
}

func requirementExcluded(path string) bool {
	lower := strings.ToLower(path)
	for _, part := range strings.Split(lower, "/") {
		if strings.HasPrefix(part, ".env.") {
			return true
		}
		switch part {
		case ".git", ".secrets", ".ssh", ".aws", ".claude", ".t3", "profiles.local.json", "credentials", "credentials.json", ".env", "runs", "series", "logs":
			return true
		}
	}
	return lower == ".codex/auth.json" || strings.HasSuffix(lower, "/.codex/auth.json")
}

var externalRequirementTarget = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

var referenceDefinition = regexp.MustCompile(`^ {0,3}\[([^\]]+)\]:\s*(.*)$`)

func linkLabel(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// Strip code before finding links, so examples cannot add execution inputs.
func markdownTargets(source string) []string {
	source = stripMarkdownComments(source)
	var clean strings.Builder
	var fence byte
	fenceLength := 0
	for _, line := range strings.Split(source, "\n") {
		marker, length, tail := markdownFence(line)
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(tail) == "" {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if length >= 3 && !(marker == '`' && strings.ContainsRune(tail, '`')) {
			fence = marker
			fenceLength = length
			continue
		}
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		clean.WriteString(line)
		clean.WriteByte('\n')
	}
	s := clean.String()
	var without strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' {
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			end := strings.Index(s[i+n:], strings.Repeat("`", n))
			if end >= 0 {
				i += n + end + n
				continue
			}
		}
		without.WriteByte(s[i])
		i++
	}
	s = without.String()
	refs := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if m := referenceDefinition.FindStringSubmatch(line); m != nil {
			if dest := markdownDestination(m[2]); dest != "" {
				refs[linkLabel(m[1])] = dest
			}
		}
	}
	var targets []string
	for i := 0; i < len(s); i++ {
		if s[i] != '[' || i > 0 && s[i-1] == '\\' {
			continue
		}
		end := strings.IndexByte(s[i+1:], ']')
		if end < 0 {
			continue
		}
		end += i + 1
		label := s[i+1 : end]
		next := end + 1
		image := i > 0 && s[i-1] == '!'
		if next < len(s) && s[next] == ':' {
			continue
		}
		if next < len(s) && s[next] == '(' {
			depth := 1
			j := next + 1
			for ; j < len(s); j++ {
				if s[j] == '\\' {
					j++
					continue
				}
				if s[j] == '(' {
					depth++
				}
				if s[j] == ')' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j < len(s) {
				if dest := markdownDestination(s[next+1 : j]); dest != "" && !image {
					targets = append(targets, dest)
				}
				i = j
			}
		} else {
			if next < len(s) && s[next] == '[' {
				j := strings.IndexByte(s[next+1:], ']')
				if j < 0 {
					continue
				}
				j += next + 1
				if j > next+1 {
					label = s[next+1 : j]
				}
				i = j
			} else {
				i = end
			}
			if dest := refs[linkLabel(label)]; dest != "" && !image {
				targets = append(targets, dest)
			}
		}
	}
	return targets
}
func markdownDestination(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == '<' {
		if end := strings.IndexByte(s, '>'); end > 0 {
			return s[1:end]
		}
		return ""
	}
	// Accept authored paths containing spaces, and optional quoted link titles.
	for _, marker := range []string{` "`, ` '`, ` (`} {
		if pos := strings.LastIndex(s, marker); pos >= 0 {
			s = strings.TrimSpace(s[:pos])
			break
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, `\(`, `(`), `\)`, `)`)
}

// A fence may be indented by at most three spaces. Its delimiter length is
// significant: shorter example fences cannot close an enclosing Markdown block.
func markdownFence(line string) (byte, int, string) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) {
		return 0, 0, ""
	}
	marker := line[indent]
	if marker != '`' && marker != '~' {
		return 0, 0, ""
	}
	end := indent
	for end < len(line) && line[end] == marker {
		end++
	}
	return marker, end - indent, line[end:]
}

// Remove HTML comments while retaining newlines and respecting code examples.
// Comment markers inside fenced blocks or complete code spans are literal text.
func stripMarkdownComments(source string) string {
	var result strings.Builder
	var fence byte
	fenceLength := 0
	for i := 0; i < len(source); {
		if i == 0 || source[i-1] == '\n' {
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				end = len(source)
			} else {
				end += i
			}
			line := source[i:end]
			marker, length, tail := markdownFence(line)
			if fence != 0 || length >= 3 && !(marker == '`' && strings.ContainsRune(tail, '`')) {
				if fence != 0 {
					if marker == fence && length >= fenceLength && strings.TrimSpace(tail) == "" {
						fence = 0
						fenceLength = 0
					}
				} else {
					fence = marker
					fenceLength = length
				}
				if end < len(source) {
					end++
				}
				result.WriteString(source[i:end])
				i = end
				continue
			}
		}
		if source[i] == '`' {
			length := 1
			for i+length < len(source) && source[i+length] == '`' {
				length++
			}
			end := i + length
			closed := false
			for end < len(source) {
				if source[end] != '`' {
					end++
					continue
				}
				run := 1
				for end+run < len(source) && source[end+run] == '`' {
					run++
				}
				if run == length {
					end += run
					closed = true
					break
				}
				end += run
			}
			if closed {
				result.WriteString(source[i:end])
				i = end
				continue
			}
		}
		if strings.HasPrefix(source[i:], "<!--") {
			end := strings.Index(source[i+4:], "-->")
			if end < 0 {
				end = len(source)
			} else {
				end += i + 7
			}
			for ; i < end; i++ {
				if source[i] == '\n' {
					result.WriteByte('\n')
				}
			}
			continue
		}
		result.WriteByte(source[i])
		i++
	}
	return result.String()
}
