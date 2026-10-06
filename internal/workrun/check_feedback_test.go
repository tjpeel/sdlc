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
	input := "private-token=disposable-secret\n[warn] /host/private.js\n[warn] .sdlc/config.json\n[warn] profiles.local.json\n[warn] ../README.md\n[warn] src/example.js secret suffix\n[warn] untracked.js\n\x1b[33m[warn] src/example.js\x1b[0m\n[warn] src/example.js\n[warn] Code style issues found in 1 files. Run Prettier with --write to fix.\n"
	// Deliberately split the output inside warnings and terminal escapes.
	for _, b := range []byte(input) {
		d.Write([]byte{b})
	}
	d.finish()
	if !d.styleIssues || !reflect.DeepEqual(d.paths, []string{"src/example.js"}) {
		t.Fatalf("unsafe diagnostic: %+v", d)
	}
	f := &CheckFailure{Command: 2, ExitCode: 1, formatter: d.styleIssues, paths: d.paths}
	feedback := checkRepairFeedback(testTree, f, [][]string{{"sh", "-c", "disposable-secret"}, {"npm", "run", "format:check"}})
	if !strings.Contains(feedback, "command 2 exited with status 1") || !strings.Contains(feedback, "src/example.js") {
		t.Fatal(feedback)
	}
	for _, private := range []string{"disposable-secret", "/host/", "profiles.local", "untracked", "secret suffix", ".sdlc/"} {
		if strings.Contains(feedback, private) {
			t.Fatalf("private diagnostic reached feedback: %s", private)
		}
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
	checker.Runner = &failingFormatterRunner{failAt: 1, output: "added 10 packages\nprivate-token=disposable-secret\n\n> example@1.0.0 format:check\n> prettier --check .\n\nChecking formatting...\n[warn] src/example.js\n[warn] src/other.js\n[warn] /host/private-input.js\n[warn] .env\n[warn] src/example.js private suffix\n[warn] Code style issues found in 2 files. Run Prettier with --write to fix.\n"}
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

func TestDockerCompilerErrorBeyondLargeBuildOutputSurvivesTeardown(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
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
	teardown := strings.Repeat("Container example Removed private-teardown-secret\n", 2000)
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
