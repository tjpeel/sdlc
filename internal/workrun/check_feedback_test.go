package workrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestFormatterDiagnosticAdmitsOnlyCommittedSourcePaths(t *testing.T) {
	root, _ := sourceFixture(t)
	sourceWrite(t, root, "src/example.js", "example\n")
	sourceWrite(t, root, ".sdlc/config.json", "{}\n")
	sourceWrite(t, root, "profiles.local.json", "{}\n")
	for _, args := range [][]string{{"add", "src", ".sdlc/config.json", "profiles.local.json"}, {"commit", "-m", "Add disposable files"}} {
		if _, err := SafeGit(context.Background(), root, args...); err != nil {
			t.Fatal(err)
		}
	}
	d := &formatterDiagnostic{approved: diagnosticSourcePaths(context.Background(), root)}
	input := "private-token=disposable-secret\n[warn] /host/private.js\n[warn] .sdlc/config.json\n[warn] profiles.local.json\n[warn] ../README.md\n[warn] src/example.js secret suffix\n[warn] untracked.js\nError src/example.js - Was not formatted.\nError /host/private.js - Was not formatted.\nError src/example.js - Was not formatted. private suffix\n\x1b[33m[warn] src/example.js\x1b[0m\n[warn] src/example.js\n[warn] Code style issues found in 1 files. Run Prettier with --write to fix.\n"
	// Deliberately split the output inside warnings and terminal escapes.
	for _, b := range []byte(input) {
		d.Write([]byte{b})
	}
	d.finish()
	if !d.styleIssues || !d.csharpier || !reflect.DeepEqual(d.paths, []string{"src/example.js"}) {
		t.Fatalf("unsafe diagnostic: %+v", d)
	}
	f := &CheckFailure{Command: 2, ExitCode: 1, formatter: d.styleIssues, csharpier: d.csharpier, paths: d.paths}
	feedback := checkRepairFeedback(testTree, f, [][]string{{"sh", "-c", "disposable-secret"}, {"npm", "run", "format:check"}})
	if !strings.Contains(feedback, "command 2 exited with status 1") || !strings.Contains(feedback, "src/example.js") {
		t.Fatal(feedback)
	}
	if !strings.Contains(feedback, "CSharpier reported") {
		t.Fatal(feedback)
	}
	for _, private := range []string{"disposable-secret", "/host/", "profiles.local", "untracked", "secret suffix", ".sdlc/", "private suffix"} {
		if strings.Contains(feedback, private) {
			t.Fatalf("private diagnostic reached feedback: %s", private)
		}
	}
}

func TestCSharpierFailureFeedsRepairFeedback(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	checker.DockerTests = false
	workspace, _ := sourceFixture(t)
	sourceWrite(t, workspace, "src/ExampleTests.cs", "class Example {}\n")
	if _, err := SafeGit(context.Background(), workspace, "add", "src"); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeGit(context.Background(), workspace, "commit", "-m", "Add C sharp source"); err != nil {
		t.Fatal(err)
	}
	checker.Runner = &failingFormatterRunner{failAt: 1, output: "private-token=disposable-secret\n#22 0.123 \x1b[31mError src/ExampleTests.cs - Was not formatted.\x1b[0m\n#22 0.124   ----------------------------- Expected: Around Line 42 -----------------------------\nprivate source fragment\nError /host/private.cs - Was not formatted.\n"}
	commands := [][]string{{"sh", "-c", "dotnet csharpier check .; private-command-argument"}}
	var log bytes.Buffer
	err := checker.Check(context.Background(), workspace, commands, &log)
	var failure *CheckFailure
	if !errors.As(err, &failure) || !failure.csharpier || !reflect.DeepEqual(failure.paths, []string{"src/ExampleTests.cs"}) {
		t.Fatalf("missing CSharpier evidence: %v", err)
	}
	feedback := checkRepairFeedback(testTree, failure, commands)
	if !strings.Contains(feedback, "CSharpier reported") || !strings.Contains(feedback, "src/ExampleTests.cs (CSharpier Expected around line 42)") {
		t.Fatal(feedback)
	}
	for _, private := range []string{"disposable-secret", "/host/", "private-command-argument", "Was not formatted.", "private source fragment", "#22", "Expected:"} {
		if strings.Contains(feedback, private) {
			t.Fatalf("private diagnostic reached feedback (%q): %s", private, feedback)
		}
	}
}

func TestCSharpierDiagnosticResolvesDockerRelativePaths(t *testing.T) {
	approved := map[string]bool{
		"tests/Example.IntegrationTests/Scenarios/SearchTests.cs": true,
		"src/Example/Administration/Cursor.cs":                    true,
	}
	for _, tc := range []struct {
		name, output, want string
	}{
		{"dot relative", "Error ./tests/Example.IntegrationTests/Scenarios/SearchTests.cs - Was not formatted.", "tests/Example.IntegrationTests/Scenarios/SearchTests.cs"},
		{"project relative", "Error ../Example/Administration/Cursor.cs - Was not formatted.", "src/Example/Administration/Cursor.cs"},
		{"build step", "#22 0.123 Error ./tests/Example.IntegrationTests/Scenarios/SearchTests.cs - Was not formatted.", "tests/Example.IntegrationTests/Scenarios/SearchTests.cs"},
		{"elapsed time", "0.123 Error ../Example/Administration/Cursor.cs - Was not formatted.", "src/Example/Administration/Cursor.cs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &formatterDiagnostic{approved: approved}
			d.Write([]byte(tc.output + "\n"))
			d.finish()
			if !d.csharpier || !reflect.DeepEqual(d.paths, []string{tc.want}) {
				t.Fatalf("missing committed formatter path: %+v", d)
			}
			feedback := checkRepairFeedback(testTree, &CheckFailure{Command: 1, ExitCode: 1, csharpier: d.csharpier, paths: d.paths}, [][]string{{"docker", "build", "."}})
			if !strings.Contains(feedback, "Formatting warning in committed source: "+tc.want) || strings.Contains(feedback, "../") || strings.Contains(feedback, "#22") {
				t.Fatalf("unexpected repair feedback: %s", feedback)
			}
		})
	}
}

func TestCSharpierDiagnosticRejectsUnresolvedRelativePaths(t *testing.T) {
	approved := map[string]bool{
		"src/Example/Administration/Cursor.cs":   true,
		"other/Example/Administration/Cursor.cs": true,
		"tests/ExampleTests.cs":                  true,
	}
	for _, output := range []string{
		"Error ../Example/Administration/Cursor.cs - Was not formatted.",
		"Error ../Missing/Unknown.cs - Was not formatted.",
		"Error ../../tests/ExampleTests.cs - Was not formatted.",
		"Error ../Example/../tests/ExampleTests.cs - Was not formatted.",
		"Error ./tests/Unknown.cs - Was not formatted.",
		"Error /host/tests/ExampleTests.cs - Was not formatted.",
		"Error ../tests/ExampleTests.cs private suffix - Was not formatted.",
		"private prefix Error ./tests/ExampleTests.cs - Was not formatted.",
		"#22 private-time Error ./tests/ExampleTests.cs - Was not formatted.",
		"#22 0.123 Error ./tests/ExampleTests.cs - Was not formatted. private suffix",
		"#22 0.123 \x1b]0;private-token\x07Error ./tests/ExampleTests.cs - Was not formatted.",
	} {
		t.Run(output, func(t *testing.T) {
			d := &formatterDiagnostic{approved: approved}
			d.Write([]byte(output + "\n"))
			d.finish()
			if d.csharpier || len(d.paths) != 0 {
				t.Fatalf("unapproved diagnostic admitted: %+v", d)
			}
		})
	}
}

func TestCSharpierDiagnosticLineAssociation(t *testing.T) {
	const file = "Error ./tests/ExampleTests.cs - Was not formatted.\n"
	const header = "  ----------------------------- Expected: Around Line 42 -----------------------------\n"
	approved := map[string]bool{
		"tests/ExampleTests.cs":   true,
		"src/Other.cs":            true,
		"src/Example/Cursor.cs":   true,
		"other/Example/Cursor.cs": true,
	}
	for _, tc := range []struct {
		name, output, want string
	}{
		{"chunked coloured docker", "#22 0.123 \x1b[31m" + strings.TrimSuffix(file, "\n") + "\x1b[0m\r\n#22 0.124 \x1b[32m" + strings.TrimSuffix(header, "\n") + "\x1b[0m\r\nprivate source fragment\n", "tests/ExampleTests.cs (CSharpier Expected around line 42)"},
		{"actual header", file + strings.Replace(header, "Expected", "Actual", 1), "tests/ExampleTests.cs (CSharpier Actual around line 42)"},
		{"blank line", file + "\n" + header, "tests/ExampleTests.cs (CSharpier Expected around line 42)"},
		{"upper bound", file + strings.Replace(header, "42", "9999999", 1), "tests/ExampleTests.cs (CSharpier Expected around line 9999999)"},
		{"new approved file", file + "Error src/Other.cs - Was not formatted.\n" + header, "src/Other.cs (CSharpier Expected around line 42)"},
		{"orphan header", header, ""},
		{"unapproved file", file + "Error /host/Unknown.cs - Was not formatted.\n" + header, ""},
		{"ambiguous file", file + "Error ../Example/Cursor.cs - Was not formatted.\n" + header, ""},
		{"unknown error", file + "Error private failure\n" + header, ""},
		{"source intervenes", file + "private source fragment\n" + header, ""},
		{"unknown marker", file + "  --- Expected: Around Line 7 ---\n" + header, ""},
		{"zero", file + strings.Replace(header, "42", "0", 1) + header, ""},
		{"negative", file + strings.Replace(header, "42", "-1", 1) + header, ""},
		{"oversized number", file + strings.Replace(header, "42", "10000000", 1) + header, ""},
		{"header suffix", file + strings.TrimSuffix(header, "\n") + " private suffix\n" + header, ""},
		{"unsupported control", file + "\x1b]0;private-token\x07\n" + header, ""},
		{"overlong intervening line", file + strings.Repeat("x", 2049) + "\n" + header, ""},
		{"first header wins", file + header + strings.Replace(header, "42", "99", 1), "tests/ExampleTests.cs (CSharpier Expected around line 42)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &formatterDiagnostic{approved: approved}
			for _, b := range []byte(tc.output) {
				d.Write([]byte{b})
			}
			d.finish()
			feedback := checkRepairFeedback(testTree, &CheckFailure{Command: 1, ExitCode: 1, csharpier: d.csharpier, paths: d.paths, formatterLocations: d.formatterLocations}, [][]string{{"docker", "build", "."}})
			if tc.want != "" && !strings.Contains(feedback, "Formatting warning in committed source: "+tc.want) {
				t.Fatalf("missing formatter location: %s", feedback)
			}
			if tc.want == "" && len(d.formatterLocations) != 0 {
				t.Fatalf("unassociated formatter location: %s", feedback)
			}
			if tc.want == "src/Other.cs (CSharpier Expected around line 42)" && strings.Contains(feedback, "tests/ExampleTests.cs (CSharpier Expected around line 42)") {
				t.Fatalf("formatter location crossed files: %s", feedback)
			}
			for _, private := range []string{"/host/", "#22", "Expected:", "Actual:", "\x1b", "around line 99)", "private source", "private-token", "private suffix"} {
				if strings.Contains(feedback, private) {
					t.Fatalf("diagnostic content reached feedback: %s", feedback)
				}
			}
		})
	}
}

func TestFormatterDiagnosticBoundsAndUnknownChecks(t *testing.T) {
	d := &formatterDiagnostic{approved: map[string]bool{"README.md": true}}
	d.Write([]byte(strings.Repeat("x", 3000) + "[warn] README.md\n[warn] README.md\n"))
	d.finish()
	if !reflect.DeepEqual(d.paths, []string{"README.md"}) {
		t.Fatal(d.paths)
	}
	d = &formatterDiagnostic{approved: map[string]bool{"README.md": true}}
	d.Write([]byte(strings.Repeat("x", 65536) + "\n[warn] README.md\n"))
	d.finish()
	if len(d.line) > 2048 || !reflect.DeepEqual(d.paths, []string{"README.md"}) {
		t.Fatal("line bound or late diagnostic scanning failed")
	}
	f := &CheckFailure{Command: 1, ExitCode: 2, paths: []string{"README.md"}}
	feedback := checkRepairFeedback(testTree, f, [][]string{{"sh", "-c", "private command"}})
	if strings.Contains(feedback, "README.md") || strings.Contains(feedback, "private command") || strings.Contains(feedback, "Prettier") {
		t.Fatal(feedback)
	}
	if !errors.Is(f, ErrCheckFailed) {
		t.Fatal("failure classification lost")
	}
}

func TestFormatterDiagnosticRejectsUnsupportedTerminalControls(t *testing.T) {
	for _, line := range []string{
		"\x1b]0;private-token\x07[warn] README.md",
		"\x1b]8;;https://example.invalid/private\x1b\\[warn] README.md\x1b]8;;\x1b\\",
		"\x1b[2K[warn] README.md",
		"\x1b[" + strings.Repeat("1;", 20) + "m[warn] README.md",
		"[warn] README.md\x00",
		"[warn] README.md\rprivate-token",
		"[warn] README.md\u009b",
	} {
		d := &formatterDiagnostic{approved: map[string]bool{"README.md": true}}
		d.Write([]byte(line + "\n"))
		d.finish()
		if len(d.paths) != 0 || d.styleIssues {
			t.Fatalf("control line admitted: %q", line)
		}
	}
	d := &formatterDiagnostic{approved: map[string]bool{"README.md": true}}
	d.Write([]byte("\x1b[33m[warn]\x1b[39m README.md\r\n\x1b[33m[warn]\x1b[39m Code style issues found in 2 files. Run Prettier with --write to fix.\n"))
	d.finish()
	if !d.styleIssues || !reflect.DeepEqual(d.paths, []string{"README.md"}) {
		t.Fatal("ordinary coloured Prettier warnings rejected")
	}
}

type failingFormatterRunner struct {
	calls  int
	failAt int
	output string
}

func (r *failingFormatterRunner) Run(_ context.Context, out io.Writer, _ ...string) error {
	r.calls++
	if r.calls < r.failAt {
		return nil
	}
	io.WriteString(out, r.output)
	return errors.New("private transport detail")
}

func TestDockerFormatterFailureFeedsSameImplementationSession(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	workspace, _ := sourceFixture(t)
	checker.Runner = &failingFormatterRunner{failAt: 2, output: "private-token=disposable-secret\n[warn] README.md\n[warn] Code style issues found in the above file(s). Run Prettier with --write to fix.\n"}
	commands := [][]string{{"go", "test", "./..."}, {"npm", "run", "format:check"}}
	var log bytes.Buffer
	err := checker.Check(context.Background(), workspace, commands, &log)
	var failure *CheckFailure
	if !errors.As(err, &failure) || failure.Command != 2 || failure.ExitCode != 1 || !reflect.DeepEqual(failure.paths, []string{"README.md"}) {
		t.Fatalf("missing failed-check evidence: %v", err)
	}
	if !strings.Contains(log.String(), "disposable-secret") {
		t.Fatal("private log unexpectedly changed")
	}
	dir, journal := testRun(t)
	journal.Plan.Checks = commands
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("waiting_for_human")}}
	r := fakeRunner(p, &fakeChecker{err: failure}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("unexpected run result: %v", err)
	}
	if len(p.calls) != 2 || p.calls[1].ResumeID != testNative {
		t.Fatalf("repair did not resume implementation session: %+v", p.calls)
	}
	prompt := p.calls[1].Prompt
	for _, evidence := range []string{"command 2 exited with status 1", "Prettier reported code style issues", "README.md"} {
		if !strings.Contains(prompt, evidence) {
			t.Fatalf("missing feedback %q", evidence)
		}
	}
	for _, private := range []string{"disposable-secret", "private transport detail"} {
		if strings.Contains(prompt, private) {
			t.Fatalf("private logs reached provider: %s", private)
		}
	}
}

func TestDockerCompoundCheckFormatterEvidence(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	checker.DockerTests = false
	workspace, _ := sourceFixture(t)
	for _, path := range []string{"src/example.js", "src/other.js"} {
		sourceWrite(t, workspace, path, "example\n")
	}
	for _, args := range [][]string{{"add", "src"}, {"commit", "-m", "Add disposable source"}} {
		if _, err := SafeGit(context.Background(), workspace, args...); err != nil {
			t.Fatal(err)
		}
	}
	commands := [][]string{{"sh", "-c", "set -eu; npm ci; npm run format:check; npm run lint; npm run test:unit"}}
	checker.Runner = &failingFormatterRunner{failAt: 1, output: "added 10 packages\nprivate-token=disposable-secret\n\n> example@1.0.0 format:check\n> prettier --check .\n\nChecking formatting...\n[warn] src/example.js\n[warn] src/other.js\n[warn] /host/private-input.js\n[warn] .env\n[warn] src/example.js private suffix\n[warn] Code style issues found in 2 files. Run Prettier with --write to fix.\nCannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"}
	var log bytes.Buffer
	err := checker.Check(context.Background(), workspace, commands, &log)
	var failure *CheckFailure
	if !errors.As(err, &failure) || failure.Command != 1 || !failure.formatter || !reflect.DeepEqual(failure.paths, []string{"src/example.js", "src/other.js"}) {
		t.Fatalf("missing compound-check evidence: %v", err)
	}
	dir, journal := testRun(t)
	journal.Plan.Checks = commands
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("waiting_for_human")}}
	r := fakeRunner(p, &fakeChecker{err: failure}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 2 || p.calls[1].ResumeID != testNative {
		t.Fatal("compound check did not resume same session")
	}
	feedback := checkRepairFeedback(testTree, failure, commands)
	for _, evidence := range []string{"command 1 exited with status 1", "Prettier reported code style issues", "src/example.js", "src/other.js"} {
		if !strings.Contains(feedback, evidence) || !strings.Contains(p.calls[1].Prompt, evidence) {
			t.Fatalf("missing compound feedback: %s", evidence)
		}
	}
	for _, private := range []string{commands[0][2], "disposable-secret", "/host/", ".env", "private suffix", "example@", "added 10"} {
		if strings.Contains(feedback, private) {
			t.Fatalf("raw shell/log detail reached feedback: %s", private)
		}
	}
	if !strings.Contains(log.String(), "disposable-secret") {
		t.Fatal("private host log changed")
	}
}

func TestDockerDaemonFailureStopsWithoutCodeRepair(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "integration disabled", true: "integration enabled"}[enabled], func(t *testing.T) {
			checker, _, _, _ := checkFixture(t)
			checker.DockerTests = enabled
			workspace, _ := sourceFixture(t)
			commands := [][]string{{"node", "--test", "example.test.js"}, {"sh", "-c", "docker compose up --build -d --wait"}}
			message := "failed to connect to the docker API at unix:///var/run/docker.sock; check if the path is correct and if the daemon is running: dial unix /var/run/docker.sock: connect: no such file or directory"
			if enabled {
				message = "Cannot connect to the Docker daemon at unix:///run/sdlc/docker.sock. Is the docker daemon running?"
			}
			checker.Runner = &failingFormatterRunner{failAt: 2, output: "private-token=disposable-secret\n" + message + "\n" + message}
			var log bytes.Buffer
			checkErr := checker.Check(context.Background(), workspace, commands, &log)
			if checkErr == nil || errors.Is(checkErr, ErrCheckFailed) {
				t.Fatalf("Docker setup failure requested code repair: %v", checkErr)
			}
			if !strings.Contains(checkErr.Error(), "Docker daemon unavailable") || !strings.Contains(log.String(), message) || !strings.Contains(log.String(), "disposable-secret") {
				t.Fatal("missing safe diagnostic or private evidence", checkErr)
			}
			if !enabled && !strings.Contains(checkErr.Error(), "new run with --docker-tests") {
				t.Fatal("missing recovery for frozen disabled setting", checkErr)
			}
			if enabled && strings.Contains(checkErr.Error(), "new run") {
				t.Fatal("enabled integration incorrectly requires a new run", checkErr)
			}
			if strings.Contains(checkErr.Error(), "disposable-secret") || strings.Contains(checkErr.Error(), "docker.sock") {
				t.Fatal("raw log crossed diagnostic boundary", checkErr)
			}
			dir, journal := testRun(t)
			journal.Plan.Checks, journal.Plan.DockerTests = commands, enabled
			p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested")}}
			r := fakeRunner(p, &fakeChecker{err: checkErr}, &fakePublisher{}, &fakeRepository{})
			if err := r.Run(context.Background(), dir, journal, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			if len(p.calls) != 1 || journal.State != "blocked" || journal.ResumeState != "checking" || journal.Rounds != 0 || journal.Evidence.Passed || journal.Evidence.Log != "checks-1.log" {
				t.Fatalf("setup failure consumed a repair round or lost its checkpoint: %+v", journal)
			}
		})
	}
}

func TestDockerDaemonDiagnosticRejectsUnrecognisedOrUnsafeLines(t *testing.T) {
	message := "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"
	for _, line := range []string{
		"private prefix " + message,
		message + " private suffix",
		"Cannot connect to the Docker daemon at unix:///host/private.sock. Is the docker daemon running?",
		"\x1b]0;private-token\x07" + message,
		message + "\x00",
	} {
		d := &formatterDiagnostic{}
		d.Write([]byte(line + "\n"))
		d.finish()
		if d.dockerUnavailable {
			t.Fatalf("unrecognised Docker diagnostic admitted: %q", line)
		}
	}
	d := &formatterDiagnostic{}
	for _, b := range []byte("\x1b[31m" + message + "\x1b[0m\r\n") {
		d.Write([]byte{b})
	}
	d.finish()
	if !d.dockerUnavailable {
		t.Fatal("ordinary coloured Docker diagnostic rejected")
	}
}

func TestDockerCompilerErrorBeyondLargeBuildOutputSurvivesTeardown(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	checker.DockerTests = false
	workspace, _ := sourceFixture(t)
	path := "tests/Example.Tests/SampleTests.cs"
	sourceWrite(t, workspace, path, "class SampleTests {}\n")
	for _, args := range [][]string{{"add", "tests"}, {"commit", "-m", "Add disposable compiler source"}} {
		if _, err := SafeGit(context.Background(), workspace, args...); err != nil {
			t.Fatal(err)
		}
	}
	commands := [][]string{{"node", "--test", "example.test.js"}, {"sh", "-c", "set -eu; trap 'docker compose down -v' EXIT; docker compose up --build -d --wait; dotnet build; dotnet test"}}
	// Mimic verbose JSON/build output before a BuildKit-prefixed compiler error,
	// duplicate summaries and a much later teardown tail. None of the raw text
	// or compiler message operands may cross into the authenticated session.
	noise := strings.Repeat("{\"private\":\""+strings.Repeat("x", 500)+"\"}\n", 200)
	first := "\x1b[31m#32 9.358 /src/" + path + "(1051,9): error CS0104: 'SampleFixture' is an ambiguous reference between 'Private.Namespace.Fixture' and 'private-token-secret' [/src/tests/Example.Tests/Example.Tests.csproj]\x1b[0m\n"
	invalid := "#32 9.357 /host/private-input.cs(2,1): error CS0104: private-input-secret\n"
	duplicate := "10.11 /src/" + path + "(1051,9): error CS0104: duplicate-private-secret\n"
	later := "/workspace/" + path + "(2000,1): error CS1002: later-private-secret\n"
	teardown := strings.Repeat("Container example Removed private-teardown-secret\n", 2000) + "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"
	checker.Runner = &failingFormatterRunner{failAt: 2, output: noise + invalid + first + duplicate + later + teardown}
	var log bytes.Buffer
	checkErr := checker.Check(context.Background(), workspace, commands, &log)
	var failure *CheckFailure
	if !errors.As(checkErr, &failure) {
		t.Fatalf("completed check lost classification: %v", checkErr)
	}
	dir, journal := testRun(t)
	journal.Plan.Checks = commands
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("waiting_for_human")}}
	r := fakeRunner(p, &fakeChecker{err: failure}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 2 || p.calls[1].ResumeID != testNative {
		t.Fatal("compiler repair did not resume same implementation session")
	}
	prompt := p.calls[1].Prompt
	for _, useful := range []string{"command 2 exited with status 1", "Failure phase: C# compilation", "First recognised compiler error: CS0104", path + ":1051:9", "ambiguous reference", "qualify the intended symbol"} {
		if !strings.Contains(prompt, useful) {
			t.Fatalf("late compiler evidence absent: %q", useful)
		}
	}
	for _, withheld := range []string{"/host/", "SampleFixture", "Private.Namespace", "private-token-secret", "duplicate-private-secret", "later-private-secret", "private-teardown-secret", "CS1002", "/src/"} {
		if strings.Contains(prompt, withheld) {
			t.Fatalf("raw or later diagnostic reached repair prompt: %q", withheld)
		}
	}
	if !strings.Contains(log.String(), "private-token-secret") || !strings.Contains(log.String(), "private-teardown-secret") {
		t.Fatal("private host log was changed")
	}
}

func TestBuildAnalyzerFailureSuppliesSafeConfiguredAndAdditionalFeedback(t *testing.T) {
	for _, code := range []string{"S8969", "S1854", "S6667", "S1075", "S1111", "S123", "S123456"} {
		t.Run(code, func(t *testing.T) {
			d := &formatterDiagnostic{approved: map[string]bool{"src/ExampleTests.cs": true}}
			output := "/host/private.cs(4,2): error " + code + ": private input\n#22 0.123 \x1b[31m/src/src/ExampleTests.cs(42,9): error " + code + ": private source operand and diagnostic text\x1b[0m\r\n" +
				"/workspace/src/ExampleTests.cs(43,10): error S9999: later private diagnostic\n"
			for _, b := range []byte(output) {
				d.Write([]byte{b})
			}
			d.finish()
			failure := &CheckFailure{Command: 1, ExitCode: 1, compiler: d.compiler}
			commands := [][]string{{"dotnet", "build", "Example.slnx"}}
			request := regressionRequest()
			request.Commands = commands
			guidance := "C# analyzer reports an error. Inspect the source location and repair the analyzer error before rerunning checks."
			if code == "S8969" {
				guidance = "redundant null-forgiving operator"
			}
			for _, feedback := range []string{
				checkRepairFeedback(testTree, failure, commands),
				verificationDiagnostic(request, testTree, &VerificationFailure{Phase: "candidate", Err: failure}),
			} {
				for _, wanted := range []string{code, "src/ExampleTests.cs:42:9", guidance} {
					if !strings.Contains(feedback, wanted) {
						t.Fatalf("analyzer repair evidence missing %q: %s", wanted, feedback)
					}
				}
				for _, private := range []string{"private input", "private source", "diagnostic text", "/host/", "/src/", "#22", "\x1b", "S9999", ":43:10"} {
					if strings.Contains(feedback, private) {
						t.Fatalf("raw or later analyzer diagnostic reached feedback: %s", feedback)
					}
				}
				if code != "S8969" && strings.Contains(feedback, "redundant null-forgiving operator") {
					t.Fatalf("unrelated specific analyzer guidance reached feedback: %s", feedback)
				}
			}
		})
	}
}

func TestBuildAnalyzerDiagnosticRetainsExistingSafetyBoundaries(t *testing.T) {
	for _, output := range []string{
		"src/Unknown.cs(42,9): error S1854: private diagnostic\n",
		"../src/ExampleTests.cs(42,9): error S6667: private diagnostic\n",
		"/host/src/ExampleTests.cs(42,9): error S1075: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error S12: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error S1234567: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error S1854X: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error S18A4: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error s1854: private diagnostic\n",
		"src/ExampleTests.cs(42,9): error S1854: private\x07diagnostic\n",
		"\x1b]0;private-token\x07src/ExampleTests.cs(42,9): error S6667: private diagnostic\n",
		"\x1b[2Jsrc/ExampleTests.cs(42,9): error S1075: private diagnostic\n",
	} {
		d := &formatterDiagnostic{approved: map[string]bool{"src/ExampleTests.cs": true}}
		d.Write([]byte(output))
		d.finish()
		if d.compiler != nil {
			t.Fatalf("unsupported or unsafe analyzer diagnostic admitted: %#v", d.compiler)
		}
	}
}

func TestDotNetStackFrameSuppliesSafeConfiguredAndAdditionalFeedback(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	checker.DockerTests = false
	workspace, _ := sourceFixture(t)
	path := "tests/Example.Tests/ExampleTests.cs"
	sourceWrite(t, workspace, path, "class ExampleTests {}\n")
	for _, args := range [][]string{{"add", "tests"}, {"commit", "-m", "Add disposable stack source"}} {
		if _, err := SafeGit(context.Background(), workspace, args...); err != nil {
			t.Fatal(err)
		}
	}
	commands := [][]string{{"dotnet", "test", "Example.slnx"}}
	for _, frame := range []string{
		"   at Example.Tests.ExampleTests.TestMethod() in /src/" + path + ":123",
		"#22 0.123 \x1b[31m   at Example.Tests.ExampleTests.TestMethod(private-argument) in /workspace/" + path + ":line 123\x1b[0m\r",
	} {
		checker.Runner = &failingFormatterRunner{failAt: 1, output: "private assertion value and collection\n" + frame + "\n   at Private.LaterMethod() in /src/" + path + ":456\n"}
		var log bytes.Buffer
		checkErr := checker.Check(context.Background(), workspace, commands, &log)
		var failure *CheckFailure
		if !errors.As(checkErr, &failure) {
			t.Fatalf("completed check lost classification: %v", checkErr)
		}
		request := regressionRequest()
		request.Commands = commands
		for _, feedback := range []string{
			checkRepairFeedback(testTree, failure, commands),
			verificationDiagnostic(request, testTree, &VerificationFailure{Phase: "candidate", Err: failure}),
		} {
			for _, wanted := range []string{path + ":123", ".NET execution", "Inspect the stack source location"} {
				if !strings.Contains(feedback, wanted) {
					t.Fatalf("stack repair evidence missing %q: %s", wanted, feedback)
				}
			}
			for _, private := range []string{"TestMethod", "private-argument", "private assertion", "collection", "Private.LaterMethod", ":456", "/src/", "/workspace/", "#22", "\x1b"} {
				if strings.Contains(feedback, private) {
					t.Fatalf("raw or later stack text reached feedback: %s", feedback)
				}
			}
		}
	}
}

func TestDotNetStackFrameStreamingAndSafetyBoundaries(t *testing.T) {
	path := "tests/Example.Tests/ExampleTests.cs"
	approved := map[string]bool{path: true, "src/Example.js": true}
	for _, location := range []string{path + ":1", "/src/" + path + ":123", "/workspace/" + path + ":line 9999999"} {
		d := &formatterDiagnostic{approved: approved}
		output := "at Private.Unknown() in /host/private.cs:1\n#22 0.123 \x1b[31m   at Private.TestMethod(private-argument) in " + location + "\x1b[0m\r\n" +
			"at Private.Later() in " + path + ":456\n"
		for _, b := range []byte(output) {
			d.Write([]byte{b})
		}
		d.finish()
		if d.stackFrame == nil || d.stackFrame.path != path || d.stackFrame.line == "456" {
			t.Fatalf("first approved streamed frame missing: %#v", d.stackFrame)
		}
		wantLine := "123"
		if strings.HasSuffix(location, ":1") {
			wantLine = "1"
		} else if strings.HasSuffix(location, "9999999") {
			wantLine = "9999999"
		}
		if d.stackFrame.line != wantLine {
			t.Fatalf("bounded line missing: %#v", d.stackFrame)
		}
	}
	for _, frame := range []string{
		"at Private.Test() in tests/Unknown.cs:123",
		"at Private.Test() in /host/" + path + ":123",
		"at Private.Test() in ../" + path + ":123",
		"at Private.Test() in src/Example.js:123",
		"at Private.Test() in " + path + ":0",
		"at Private.Test() in " + path + ":0123",
		"at Private.Test() in " + path + ":10000000",
		"at Private.Test() in " + path + ":-1",
		"at Private.Test() in " + path + ":line x",
		"at Private.Test() in " + path + ":123:4",
		"at Private.Test() in " + path + ":123 private assertion",
		"private assertion in " + path + ":123",
		"at Private.Test(\x07) in " + path + ":123",
		"\x1b]0;private-token\x07at Private.Test() in " + path + ":123",
		"\x1b[2Jat Private.Test() in " + path + ":123",
		"at " + strings.Repeat("x", 2048) + " in " + path + ":123",
	} {
		d := &formatterDiagnostic{approved: approved}
		d.Write([]byte(frame + "\n"))
		d.finish()
		if d.stackFrame != nil {
			t.Fatalf("unsafe stack location admitted: %#v", d.stackFrame)
		}
	}
}

func TestDotNetStackFramePreservesCompilerAndFormatterPrecedence(t *testing.T) {
	path := "src/Example.cs"
	frame := "at Private.Test() in /src/" + path + ":123\n"
	compiler := "/src/" + path + "(42,9): error CS0104: private compiler message\n"
	for _, output := range []string{
		frame + compiler,
		compiler + frame,
		frame + "[warn] Code style issues found in the above file(s). Run Prettier with --write to fix.\n",
		frame + "Error " + path + " - Was not formatted.\n",
	} {
		d := &formatterDiagnostic{approved: map[string]bool{path: true}}
		d.Write([]byte(output))
		d.finish()
		failure := &CheckFailure{Command: 1, ExitCode: 1, compiler: d.compiler, formatter: d.styleIssues, csharpier: d.csharpier, paths: d.paths, stackFrame: d.stackFrame}
		feedback := checkRepairFeedback(testTree, failure, [][]string{{"dotnet", "test"}})
		if strings.Contains(feedback, ".NET execution") || strings.Contains(feedback, ":123") {
			t.Fatalf("stack metadata displaced compiler or formatter feedback: %s", feedback)
		}
		if d.compiler != nil && !strings.Contains(feedback, "CS0104") {
			t.Fatalf("compiler evidence missing: %s", feedback)
		}
	}
}
