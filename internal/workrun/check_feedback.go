package workrun

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// CheckFailure describes a completed command, without exposing its arguments or logs.
type CheckFailure struct {
	Command            int
	ExitCode           int
	formatter          bool
	csharpier          bool
	paths              []string
	formatterLocations map[string]csharpierLocation
	compiler           *compilerDiagnostic
	stackFrame         *dotNetStackFrame
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

// Only fixed diagnostic metadata and exact committed file names may cross into
// repair feedback. This is deliberately not a general purpose log redactor.
type formatterDiagnostic struct {
	approved           map[string]bool
	line               []byte
	dropping           bool
	styleIssues        bool
	csharpier          bool
	paths              []string
	formatterLocations map[string]csharpierLocation
	csharpierPath      string
	compiler           *compilerDiagnostic
	stackFrame         *dotNetStackFrame
	dockerUnavailable  bool
}

func (d *formatterDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	for _, b := range p {
		// Scan the entire stream with bounded retained state. Build output can
		// exceed 64 KiB before its first useful error; teardown may follow it.
		if b == '\n' {
			if !d.dropping {
				d.consume()
			}
			d.line = d.line[:0]
			d.dropping = false
		} else if len(d.line) >= 2048 {
			d.dropping = true
			d.csharpierPath = ""
		} else if !d.dropping {
			d.line = append(d.line, b)
		}
	}
	return n, nil
}

func (d *formatterDiagnostic) consume() {
	line, safe := formatterLine(d.line)
	if !safe {
		d.csharpierPath = ""
		return
	}
	formatterOutput := dockerOutputPrefix.ReplaceAllString(line, "")
	if d.csharpierPath != "" {
		if match := csharpierLineHeader.FindStringSubmatch(formatterOutput); match != nil {
			if d.formatterLocations == nil {
				d.formatterLocations = make(map[string]csharpierLocation)
			}
			if d.formatterLocations[d.csharpierPath].line == "" {
				d.formatterLocations[d.csharpierPath] = csharpierLocation{kind: match[1], line: match[2]}
			}
			d.csharpierPath = ""
			return
		}
		if formatterOutput == "" {
			return
		}
	}
	d.csharpierPath = ""
	if dockerDaemonUnavailable(line) {
		d.dockerUnavailable = true
	}
	if d.compiler == nil {
		d.compiler = parseCompilerDiagnostic(line, d.approved)
	}
	if d.stackFrame == nil {
		d.stackFrame = parseDotNetStackFrame(formatterOutput, d.approved)
	}
	path, warning := strings.CutPrefix(line, "[warn] ")
	if !warning {
		// CSharpier reports only a fixed file-level diagnostic. Admit it only
		// when its resolved path is part of the approved committed source tree.
		path, ok := strings.CutPrefix(formatterOutput, "Error ")
		if !ok {
			return
		}
		path, ok = strings.CutSuffix(path, " - Was not formatted.")
		if !ok {
			return
		}
		path = csharpierSourcePath(path, d.approved)
		if path == "" {
			return
		}
		d.csharpier = true
		d.appendPath(path)
		for _, admitted := range d.paths {
			if admitted == path {
				d.csharpierPath = path
				break
			}
		}
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
	if !d.approved[path] {
		return
	}
	d.appendPath(path)
}

// Strip only the bounded step/time prefix emitted by Docker build output.
var dockerOutputPrefix = regexp.MustCompile(`^(?:#[0-9]{1,6} [0-9]{1,6}(?:\.[0-9]{1,6})? |[0-9]{1,6}(?:\.[0-9]{1,6})? )`)

// Only the fixed CSharpier header may supply a positive, bounded location.
// Diff content is never retained; another line ends the file association.
var csharpierLineHeader = regexp.MustCompile(`^  ----------------------------- (Expected|Actual): Around Line ([1-9][0-9]{0,6}) -----------------------------$`)

type csharpierLocation struct {
	kind string
	line string
}

func csharpierSourcePath(path string, approved map[string]bool) string {
	path = strings.TrimPrefix(path, "./")
	if approved[path] {
		return path
	}
	// CSharpier may run in a project directory and report a sibling project.
	// A single parent prefix is supported only when its remaining path names
	// exactly one approved file; never return a raw diagnostic path.
	suffix, relative := strings.CutPrefix(path, "../")
	if !relative || len(suffix) > 240 || !sourceRelative(suffix) {
		return ""
	}
	resolved := ""
	for candidate, allowed := range approved {
		if allowed && (candidate == suffix || strings.HasSuffix(candidate, "/"+suffix)) {
			if resolved != "" {
				return ""
			}
			resolved = candidate
		}
	}
	return resolved
}

func (d *formatterDiagnostic) appendPath(path string) {
	if len(d.paths) >= 20 {
		return
	}
	for _, seen := range d.paths {
		if path == seen {
			return
		}
	}
	d.paths = append(d.paths, path)
}

// Recognise only fixed Docker diagnostics for the default or isolated socket.
// Endpoint values and arbitrary log text stay in the private check log.
func dockerDaemonUnavailable(line string) bool {
	for _, socket := range []string{"/var/run/docker.sock", checkSocket} {
		if line == "Cannot connect to the Docker daemon at unix://"+socket+". Is the docker daemon running?" {
			return true
		}
		prefix := "failed to connect to the docker API at unix://" + socket + "; check if the path is correct and if the daemon is running: dial unix " + socket + ": connect: "
		if reason, ok := strings.CutPrefix(line, prefix); ok && (reason == "no such file or directory" || reason == "connection refused") {
			return true
		}
	}
	return false
}

type compilerDiagnostic struct {
	code   string
	path   string
	line   string
	column string
}

// Docker BuildKit prefixes command output with a step and elapsed time. Accept
// those fixed prefixes, then only C# code/location metadata, never message text.
var compilerErrorLine = regexp.MustCompile(`^(?:#[0-9]{1,6} [0-9]{1,6}(?:\.[0-9]{1,6})? |[0-9]{1,6}(?:\.[0-9]{1,6})? )?([^()]+)\(([0-9]{1,7}),([0-9]{1,5})\): error (CS[0-9]{4}|S[0-9]{3,6}): `)

func parseCompilerDiagnostic(line string, approved map[string]bool) *compilerDiagnostic {
	match := compilerErrorLine.FindStringSubmatch(line)
	if match == nil {
		return nil
	}
	path := match[1]
	for _, prefix := range []string{"/src/", "/workspace/"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	if !approved[path] {
		return nil
	}
	return &compilerDiagnostic{code: match[4], path: path, line: match[2], column: match[3]}
}

// Stack text stays private; retain only the first approved C# source location.
type dotNetStackFrame struct {
	path string
	line string
}

var dotNetStackFrameLine = regexp.MustCompile(`^ *at .+ in ([^:]+):(?:line )?([1-9][0-9]{0,6})$`)

func parseDotNetStackFrame(line string, approved map[string]bool) *dotNetStackFrame {
	match := dotNetStackFrameLine.FindStringSubmatch(line)
	if match == nil {
		return nil
	}
	path := match[1]
	for _, prefix := range []string{"/src/", "/workspace/"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	if len(path) > 240 || !strings.HasSuffix(path, ".cs") || !approved[path] {
		return nil
	}
	return &dotNetStackFrame{path: path, line: match[2]}
}

// Formatters and compilers may colour output. Accept only bounded numeric SGR
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
	if !d.dropping && len(d.line) > 0 {
		d.consume()
	}
}

func checkRepairFeedback(tree string, err error, commands [][]string) string {
	feedback := "Configured checks failed on committed tree " + tree + "."
	var failure *CheckFailure
	if errors.As(err, &failure) && failure.Command > 0 && failure.Command <= len(commands) && failure.ExitCode > 0 {
		feedback += fmt.Sprintf(" Check command %d exited with status %d.", failure.Command, failure.ExitCode)
		if c := failure.compiler; c != nil {
			feedback += fmt.Sprintf("\nFailure phase: C# compilation. First recognised compiler error: %s at committed source %s:%s:%s.", c.code, c.path, c.line, c.column)
			if c.code == "CS0104" {
				feedback += " C# reports an ambiguous reference. Inspect that location and qualify the intended symbol or resolve conflicting namespace imports."
			} else if c.code == "S8969" {
				feedback += " The analyzer reports a redundant null-forgiving operator. Inspect that location and remove the unnecessary operator while preserving nullability behavior."
			} else if strings.HasPrefix(c.code, "S") {
				feedback += " C# analyzer reports an error. Inspect the source location and repair the analyzer error before rerunning checks."
			} else {
				feedback += " Inspect that source location and repair the compiler error before rerunning checks."
			}
		}
		if frame := failure.stackFrame; frame != nil && failure.compiler == nil && !failure.formatter && !failure.csharpier && !formatterCheck(commands[failure.Command-1]) {
			feedback += fmt.Sprintf("\nFailure phase: .NET execution. First recognised stack location at committed source %s:%s. Inspect the stack source location and repair the execution failure before rerunning checks.", frame.path, frame.line)
		}

		// Compound checks may invoke Prettier through a shell. A fixed output
		// marker identifies formatter evidence without interpreting shell text.
		if failure.formatter || failure.csharpier || formatterCheck(commands[failure.Command-1]) {
			if formatterCheck(commands[failure.Command-1]) {
				feedback += " The configured formatting check failed."
			}
			if failure.formatter {
				feedback += " Prettier reported code style issues; apply the repository formatter."
			}
			if failure.csharpier {
				feedback += " CSharpier reported that committed source is not formatted; format the reported file before requesting checks again."
			}
			for _, path := range failure.paths {
				feedback += "\nFormatting warning in committed source: " + path
				if location := failure.formatterLocations[path]; location.line != "" {
					feedback += fmt.Sprintf(" (CSharpier %s around line %s)", location.kind, location.line)
				}
			}
		}
	}
	return feedback + "\nInspect the selected tests and code, repair within ticket scope, and request isolated checks again. Other diagnostic logs remain private on the host. Ask a human only if the evidence needed for repair is unavailable."
}
