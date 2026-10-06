package workrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// CheckFailure describes a completed command, without exposing its arguments or logs.
type CheckFailure struct {
	Command   int
	ExitCode  int
	formatter bool
	paths     []string
}

func (f *CheckFailure) Error() string {
	return fmt.Sprintf("repository check failed: command %d exited %d", f.Command, f.ExitCode)
}
func (f *CheckFailure) Unwrap() error { return ErrCheckFailed }

func formatterCheck(command []string) bool {
	// Exact selectors only: shell commands and arbitrary arguments remain private.
	return len(command) == 3 && command[0] == "npm" && command[1] == "run" && command[2] == "format:check" ||
		len(command) == 3 && command[0] == "npx" && command[1] == "prettier" && command[2] == "--check"
}

func diagnosticSourcePaths(ctx context.Context, workspace string) map[string]bool {
	paths := make(map[string]bool)
	entries, err := SafeGit(ctx, workspace, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return paths
	}
	for _, entry := range strings.Split(entries, "\x00") {
		metadata, path, ok := strings.Cut(entry, "\t")
		if !ok || !(strings.HasPrefix(metadata, "100644 blob ") || strings.HasPrefix(metadata, "100755 blob ")) {
			continue
		}
		if !sourceRelative(path) || excluded(path) || len(path) > 240 || strings.Contains(strings.ToLower("/"+path+"/"), "/.sdlc/") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
			continue
		}
		paths[path] = true
	}
	return paths
}

// Only fixed Prettier warnings and exact committed file names may cross into
// repair feedback. This is deliberately not a general purpose log redactor.
type formatterDiagnostic struct {
	approved    map[string]bool
	line        []byte
	bytes       int
	dropping    bool
	styleIssues bool
	paths       []string
}

func (d *formatterDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	for _, b := range p {
		if d.bytes >= 65536 {
			break
		}
		d.bytes++
		if b == '\n' {
			if !d.dropping {
				d.consume()
			}
			d.line = d.line[:0]
			d.dropping = false
		} else if len(d.line) >= 2048 {
			d.dropping = true
		} else if !d.dropping {
			d.line = append(d.line, b)
		}
	}
	return n, nil
}

func (d *formatterDiagnostic) consume() {
	line, safe := formatterLine(d.line)
	if !safe {
		return
	}
	path, warning := strings.CutPrefix(line, "[warn] ")
	if !warning {
		return
	}
	if path == "Code style issues found in the above file(s). Run Prettier with --write to fix." {
		d.styleIssues = true
		return
	}
	// Newer Prettier emits a decimal count in an otherwise fixed message.
	if count, ok := strings.CutPrefix(path, "Code style issues found in "); ok {
		if count, ok = strings.CutSuffix(count, " files. Run Prettier with --write to fix."); ok && len(count) > 0 && len(count) <= 6 && strings.Trim(count, "0123456789") == "" {
			d.styleIssues = true
			return
		}
	}
	if !d.approved[path] || len(d.paths) >= 20 {
		return
	}
	for _, seen := range d.paths {
		if path == seen {
			return
		}
	}
	d.paths = append(d.paths, path)
}

// Prettier may colour warnings with SGR codes. Accept only bounded numeric SGR
// sequences; cursor movement, OSC payloads and other controls discard the line.
// The runtime publisher copies this package without third-party dependencies.
func formatterLine(raw []byte) (string, bool) {
	if len(raw) > 0 && raw[len(raw)-1] == '\r' {
		raw = raw[:len(raw)-1]
	}
	var text strings.Builder
	text.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == 0x1b {
			if i+1 >= len(raw) || raw[i+1] != '[' {
				return "", false
			}
			start := i + 2
			j := start
			for j < len(raw) && j-start <= 32 && (raw[j] >= '0' && raw[j] <= '9' || raw[j] == ';') {
				j++
			}
			if j >= len(raw) || j-start > 32 || raw[j] != 'm' {
				return "", false
			}
			i = j
			continue
		}
		if b < 0x20 || b == 0x7f {
			return "", false
		}
		text.WriteByte(b)
	}
	line := text.String()
	if strings.IndexFunc(line, unicode.IsControl) >= 0 {
		return "", false
	}
	return line, true
}

func (d *formatterDiagnostic) finish() {
	if !d.dropping && d.bytes < 65536 && len(d.line) > 0 {
		d.consume()
	}
}

func checkRepairFeedback(tree string, err error, commands [][]string) string {
	feedback := "Configured checks failed on committed tree " + tree + "."
	var failure *CheckFailure
	if errors.As(err, &failure) && failure.Command > 0 && failure.Command <= len(commands) && failure.ExitCode > 0 {
		feedback += fmt.Sprintf(" Check command %d exited with status %d.", failure.Command, failure.ExitCode)
		// Compound checks may invoke Prettier through a shell. A fixed output
		// marker identifies formatter evidence without interpreting shell text.
		if failure.formatter || formatterCheck(commands[failure.Command-1]) {
			if formatterCheck(commands[failure.Command-1]) {
				feedback += " The configured formatting check failed."
			}
			if failure.formatter {
				feedback += " Prettier reported code style issues; apply the repository formatter."
			}
			for _, path := range failure.paths {
				feedback += "\nFormatting warning in committed source: " + path
			}
		}
	}
	return feedback + "\nInspect the selected tests and code, repair within ticket scope, and request isolated checks again. Other diagnostic logs remain private on the host. Ask a human only if the evidence needed for repair is unavailable."
}
