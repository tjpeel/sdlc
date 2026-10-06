package dashboard

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Wrap expands terminal tabs before measuring physical rows. ANSI is allowed
// only from the trusted presentation layer, after private text is sanitized.
func Wrap(text string, width int) []string {
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		style := ""
		if strings.HasPrefix(line, "\x1b[") && strings.HasSuffix(line, "\x1b[0m") {
			if end := strings.IndexByte(line, 'm'); end >= 0 {
				style = line[:end+1]
				line = strings.TrimSuffix(line[end+1:], "\x1b[0m")
			}
		}
		var expanded strings.Builder
		parts := strings.Split(line, "\t")
		for i, part := range parts {
			if i > 0 {
				expanded.WriteString(strings.Repeat(" ", 8-ansi.StringWidth(expanded.String())%8))
			}
			expanded.WriteString(part)
		}
		for _, row := range strings.Split(ansi.Hardwrap(expanded.String(), max(1, width), true), "\n") {
			row = ansi.Truncate(row, max(1, width), "")
			if style != "" && row != "" {
				row = style + row + "\x1b[0m"
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// StyleSnapshot colors the dashboard's own plain sections. Callers must sanitize
// untrusted text first; this never accepts terminal control sequences from logs.
func StyleSnapshot(text string, enabled bool) string {
	if !enabled {
		return text
	}
	lines := strings.Split(text, "\n")
	recorded, active, review := false, false, false
	for i, line := range lines {
		code := ""
		switch {
		case strings.HasPrefix(line, "SDLC "):
			code = "1;36"
		case strings.HasPrefix(line, "Page "), strings.Contains(line, "RUN") && strings.Contains(line, "REPOSITORY"):
			code = "1;34"
		case strings.HasPrefix(line, "== "):
			recorded = line == "== Recorded runs =="
			active = line == "== Active runs =="
			review = line == "== Ready for review =="
			code = "1;36"
			if recorded {
				code = "90"
			}
			if active {
				code = "1;32"
			}
			if line == "== Needs attention ==" {
				code = "1;31"
			}
		case strings.HasPrefix(line, "Select:"), strings.HasPrefix(line, "Remove from dashboard:"), strings.HasPrefix(line, "Ctrl-C"):
			recorded, active, review = false, false, false
			code = "90"
		case review && !reviewAction(line):
			code = "90"
		case recorded:
			code = "90"
		case active && !strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "":
			code = "32"
		}
		if code != "" && line != "" {
			lines[i] = "\x1b[" + code + "m" + line + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

// Completed summaries can recede while the human's next action stays readable.
func reviewAction(line string) bool {
	text := strings.TrimSpace(line)
	return strings.HasPrefix(text, "Next:") || strings.HasPrefix(text, "[review]") || strings.HasPrefix(text, "Question:") || strings.HasPrefix(text, "Missing file?")
}
