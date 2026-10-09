package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func regressionRequest() VerificationRequest {
	return VerificationRequest{ID: "regression", Purpose: "prove regression test detects original source", Commands: [][]string{{"go", "test", "./example"}}, Baseline: &VerificationBaseline{Revision: testBase, Paths: []string{"example/source.go"}, ExpectedExitCode: 1, FailureContains: []string{"TestRegression", "expected fixed"}}}
}
func TestVerificationContract(t *testing.T) {
	for _, body := range []string{`{"verification_requests":null}`, `{"verification_requests":{}}`, `{"verification_requests":[{"id":"test","purpose":"run","commands":null}]}`} {
		var o Outcome
		if json.Unmarshal([]byte(body), &o) == nil {
			t.Errorf("accepted %s", body)
		}
	}
	for _, body := range []string{`{}`, `{"verification_requests":[]}`} {
		var o Outcome
		if err := json.Unmarshal([]byte(body), &o); err != nil {
			t.Fatal(err)
		}
	}
	r := regressionRequest()
	plan := Plan{StartingSHA: testBase, SourceSHA: testHead}
	if err := validateVerificationRequests([]VerificationRequest{r}, &plan); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../source.go", ".git/config", "/source.go", "example/../source.go"} {
		r := regressionRequest()
		r.Baseline.Paths = []string{p}
		if validateVerificationRequests([]VerificationRequest{r}, &plan) == nil {
			t.Errorf("accepted %s", p)
		}
	}
	r.Baseline.Revision = testTree
	if validateVerificationRequests([]VerificationRequest{r}, &plan) == nil {
		t.Fatal("accepted uncaptured SHA")
	}
}
func TestFailureMatcherBoundedChunks(t *testing.T) {
	m := newFailureMatcher(regressionRequest().Baseline)
	for _, chunk := range []string{"prefix TestReg", "ression", strings.Repeat("x", 10000), "expected ", "fixed"} {
		m.Write([]byte(chunk))
		if len(m.tail) >= m.longest {
			t.Fatal("unbounded tail")
		}
	}
	if !m.complete() {
		t.Fatal("split markers missing")
	}
	if newFailureMatcher(regressionRequest().Baseline).complete() {
		t.Fatal("empty output matched")
	}
}

type supplementaryChecker struct {
	fakeChecker
	requests []VerificationRequest
	err      error
}

func (c *supplementaryChecker) Verify(_ context.Context, _ string, r VerificationRequest, out io.Writer) error {
	c.requests = append(c.requests, r)
	io.WriteString(out, "private test output")
	return c.err
}
func TestAdditionalVerificationLifecycleAndCache(t *testing.T) {
	dir, j := testRun(t)
	request := regressionRequest()
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{request}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), asked, asked, testOutcome("implemented"), testOutcome("reviewed")}}
	c := &supplementaryChecker{}
	pub := &fakePublisher{}
	r := fakeRunner(p, &c.fakeChecker, pub, &fakeRepository{})
	r.Checker = c
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || c.calls != 1 || len(c.requests) != 1 || pub.calls != 1 {
		t.Fatalf("state=%s suite=%d additional=%d publish=%d", j.State, c.calls, len(c.requests), pub.calls)
	}
	if p.calls[2].ResumeID != testNative || !strings.Contains(p.calls[3].Prompt, `"additional"`) || !strings.Contains(p.calls[4].Prompt, `"additional_verification"`) {
		t.Fatal("session or review evidence missing")
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded.Verification) != 1 || !loaded.Verification[0].Passed {
		t.Fatalf("persisted evidence missing: %v", err)
	}
}
func TestFailedAdditionalRequestSurvivesOmissionAndResume(t *testing.T) {
	dir, j := testRun(t)
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{regressionRequest()}
	p := &fakeProvider{outcomes: []Outcome{asked, testOutcome("implemented")}}
	c := &supplementaryChecker{err: ErrCheckFailed}
	pub := &fakePublisher{}
	r := fakeRunner(p, &c.fakeChecker, pub, &fakeRepository{})
	r.Checker = c
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("failed request published")
	}
	if pub.calls != 0 || len(c.requests) != 1 || j.State != "blocked" {
		t.Fatal("failed request bypassed")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Verification) != 1 || loaded.Verification[0].Passed {
		t.Fatal("failed request lost")
	}
	if err := r.Run(context.Background(), dir, &loaded, ""); err == nil || len(c.requests) != 1 || pub.calls != 0 {
		t.Fatal("resumed unchanged failed evidence reran or published")
	}

	generic := regressionRequest()
	generic.Baseline = nil
	if mergeVerification(&loaded, []VerificationRequest{generic}) == nil {
		t.Fatal("baseline downgraded")
	}
}
func TestVerificationCacheIdentity(t *testing.T) {
	_, j := testRun(t)
	r := regressionRequest()
	key := verificationKey(j, r, testHead, testTree)
	j.Verification = []VerificationResult{{Request: r, Key: key, Passed: true}}
	if !verificationCurrent(j, testHead, testTree) {
		t.Fatal("cache miss")
	}
	if verificationCurrent(j, testBase, testTree) || verificationCurrent(j, testHead, testBase) {
		t.Fatal("head/tree cache collision")
	}
	r.Purpose = "changed"
	if verificationKey(j, r, testHead, testTree) == key {
		t.Fatal("request cache collision")
	}
	j.ImageID = "changed"
	if verificationCurrent(j, testHead, testTree) {
		t.Fatal("runtime cache collision")
	}
}
func TestNoProgressVerificationBlocks(t *testing.T) {
	dir, j := testRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("checks_requested"), testOutcome("checks_requested"), testOutcome("waiting_for_human")}}
	c := &fakeChecker{}
	pub := &fakePublisher{}
	r := fakeRunner(p, c, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("repeated no progress did not stop")
	}
	if j.State != "blocked" || len(p.calls) != 3 || c.calls != 1 || pub.calls != 0 {
		t.Fatalf("no-progress gate: %s %d %d %d", j.State, len(p.calls), c.calls, pub.calls)
	}
}

// Run the production copy helper against real commits with ownership changes stubbed.
func TestBaselineCopyRetainsCandidateTestsAndOriginalMode(t *testing.T) {
	root := t.TempDir()
	destination := t.TempDir()
	inputs := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.invalid", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	source := filepath.Join(root, "source.sh")
	os.WriteFile(source, []byte("old source"), 0755)
	git("add", ".")
	git("commit", "-m", "Original fixture")
	base := git("rev-parse", "HEAD")
	os.Chmod(source, 0644)
	os.WriteFile(source, []byte("fixed source"), 0644)
	os.WriteFile(filepath.Join(root, "new_test.go"), []byte("new regression test"), 0644)
	git("add", ".")
	git("commit", "-m", "Candidate fixture")
	script := strings.NewReplacer("/source", root, "/workspace", destination, "/inputs", inputs).Replace(copyCheckWorkspace)
	script = "import os\nos.chown = lambda *args, **kwargs: None\n" + script
	baseline := regressionRequest().Baseline
	baseline.Revision = base
	baseline.Paths = []string{"source.sh"}
	data, _ := json.Marshal(baseline)
	cmd := exec.Command("python3", "-c", script, string(data))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %s %v", out, err)
	}
	old, _ := os.ReadFile(filepath.Join(destination, "source.sh"))
	test, _ := os.ReadFile(filepath.Join(destination, "new_test.go"))
	mode, _ := os.Stat(filepath.Join(destination, "source.sh"))
	current, _ := os.ReadFile(source)
	if string(old) != "old source" || string(test) != "new regression test" || mode.Mode().Perm() != 0755 || string(current) != "fixed source" {
		t.Fatal("baseline did not preserve candidate tests and original source mode")
	}
}

type baselineCommandRunner struct {
	output string
	calls  int
	failAt int
	cancel context.CancelFunc
}

func (r *baselineCommandRunner) Run(_ context.Context, out io.Writer, args ...string) error {
	r.calls++
	io.WriteString(out, r.output)
	if r.cancel != nil {
		r.cancel()
	}
	if r.calls == r.failAt {
		return errors.New("process nonzero")
	}
	return nil
}
func TestBaselineFinalCommandClassification(t *testing.T) {
	for _, tc := range []struct {
		name, output, state string
		fail                int
		cleanup, inspect    bool
		pass                bool
	}{
		{"expected", "TestRegression expected fixed", "", 2, false, false, true},
		{"markers absent", "other output", "", 2, false, false, false},
		{"command args are not output", "", "", 2, false, false, false},
		{"earlier failure", "TestRegression expected fixed", "", 1, false, false, false},
		{"wrong exit", "TestRegression expected fixed", `{"Status":"exited","ExitCode":2}`, 2, false, false, false},
		{"oom", "TestRegression expected fixed", `{"Status":"exited","ExitCode":1,"OOMKilled":true}`, 2, false, false, false},
		{"still running", "TestRegression expected fixed", `{"Status":"running","Running":true,"ExitCode":1}`, 2, false, false, false},
		{"cleanup", "TestRegression expected fixed", "", 2, true, false, false},
		{"transport", "TestRegression expected fixed", "", 2, false, true, false},
		{"unexpected pass", "TestRegression expected fixed", "", 0, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker, docker, _, workspace := checkFixture(t)
			docker.workerState = tc.state
			docker.cleanupFail = tc.cleanup
			docker.inspectFail = tc.inspect
			r := &baselineCommandRunner{output: tc.output, failAt: tc.fail}
			checker.Runner = r
			request := regressionRequest()
			request.Commands = [][]string{{"setup"}, {"test", "TestRegression expected fixed"}}
			err := checker.check(context.Background(), workspace, request.Commands, io.Discard, request.Baseline)
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v err=%v", tc.pass, err)
			}
		})
	}
}

func TestNativeVerificationSchemaRequiredProperties(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(outcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if node["type"] == "object" {
				props, _ := node["properties"].(map[string]any)
				required, _ := node["required"].([]any)
				set := map[string]bool{}
				for _, key := range required {
					set[key.(string)] = true
				}
				for key := range props {
					if !set[key] {
						t.Errorf("native schema property %s must be required", key)
					}
				}
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(schema)
	props := schema["properties"].(map[string]any)
	item := props["verification_requests"].(map[string]any)["items"].(map[string]any)
	baseline := item["properties"].(map[string]any)["baseline"].(map[string]any)
	choices := baseline["anyOf"].([]any)
	if len(choices) != 2 || choices[1].(map[string]any)["type"] != "null" {
		t.Fatal("generic baseline is not nullable")
	}
	var outcome Outcome
	if err := json.Unmarshal([]byte(`{"verification_requests":[{"id":"test","purpose":"generic check","commands":[["true"]],"baseline":null}]}`), &outcome); err != nil {
		t.Fatal(err)
	}
}
func TestVerificationProtectsFrozenInputsAndBounds(t *testing.T) {
	plan := Plan{StartingSHA: testBase, SourceSHA: testBase, Inputs: []Input{{Path: "requirements/spec.md"}}, CheckInputs: []Input{{Path: "config/test.json"}}}
	for _, p := range []string{"requirements/spec.md", "config/test.json", "config/test.json/child"} {
		r := regressionRequest()
		r.Baseline.Paths = []string{p}
		if validateVerificationRequests([]VerificationRequest{r}, &plan) == nil {
			t.Fatalf("accepted protected input %s", p)
		}
	}
	r := regressionRequest()
	r.Baseline.Paths = []string{"inputs/source.go"}
	if err := validateVerificationRequests([]VerificationRequest{r}, &plan); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"test\nline", "test\x1bsecret"} {
		r := regressionRequest()
		r.Purpose = s
		if validateVerificationRequests([]VerificationRequest{r}, &plan) == nil {
			t.Fatal("accepted control character")
		}
	}
}
func TestAdditionalFailureFeedbackPreservesSafeDetails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  *CheckFailure
		expected []string
	}{
		{"formatter", &CheckFailure{Command: 1, ExitCode: 1, formatter: true, paths: []string{"src/example.js"}}, []string{"candidate phase", "command 1 exited with status 1", "src/example.js", "Prettier"}},
		{"compiler", &CheckFailure{Command: 1, ExitCode: 1, compiler: &compilerDiagnostic{code: "CS0104", path: "src/Example.cs", line: "12", column: "3"}}, []string{"candidate phase", "CS0104", "src/Example.cs:12:3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, j := testRun(t)
			request := regressionRequest()
			request.Commands = [][]string{{"sh", "-c", "disposable-private-argument"}}
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{request}
			p := &fakeProvider{outcomes: []Outcome{asked, testOutcome("waiting_for_human")}}
			c := &supplementaryChecker{err: &VerificationFailure{Phase: "candidate", Err: tc.failure}}
			r := fakeRunner(p, &c.fakeChecker, &fakePublisher{}, &fakeRepository{})
			r.Checker = c
			r.Run(context.Background(), dir, j, "")
			if len(p.calls) != 2 || p.calls[1].ResumeID != testNative {
				t.Fatal("failed request did not resume original session")
			}
			prompt := p.calls[1].Prompt
			for _, text := range tc.expected {
				if !strings.Contains(prompt, text) {
					t.Errorf("feedback missing %q", text)
				}
			}
			if strings.Contains(j.Verification[0].Diagnostic, "disposable-private-argument") || strings.Contains(prompt, "private test output") {
				t.Fatal("private diagnostic leaked")
			}
			if _, err := Load(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestAdditionalBaselineMismatchFeedback(t *testing.T) {
	request := regressionRequest()
	err := &VerificationFailure{Phase: "baseline", Err: &baselineMismatch{Reason: "baseline final command omitted required literal markers", Err: &CheckFailure{Command: 1, ExitCode: 1}}}
	feedback := verificationDiagnostic(request, testTree, err)
	if !strings.Contains(feedback, "baseline phase") || !strings.Contains(feedback, "omitted required literal markers") {
		t.Fatal(feedback)
	}
}
func TestAdditionalUnsupportedRuntimeBlocks(t *testing.T) {
	dir, j := testRun(t)
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{regressionRequest()}
	p := &fakeProvider{outcomes: []Outcome{asked}}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err == nil || j.State != "blocked" || pub.calls != 0 {
		t.Fatal("unsupported runtime allowed publication")
	}
}

func TestVerificationRequestEncodingBound(t *testing.T) {
	r := regressionRequest()
	r.Commands = [][]string{{"echo", strings.Repeat("\x01", 2000)}}
	if validateVerificationRequests([]VerificationRequest{r}, nil) == nil {
		t.Fatal("escaped request exceeded encoding bound")
	}
	outcome := testOutcome("implemented")
	outcome.VerificationRequests = []VerificationRequest{regressionRequest()}
	if validateOutcome(outcome, "implementation") == nil {
		t.Fatal("completed outcome accepted new pending machine work")
	}
}
func TestVerificationRuntimeUsesBothCandidateAndBaselinePhases(t *testing.T) {
	checker, docker, _, workspace := checkFixture(t)
	checker.DockerTests = false
	runner := &baselineCommandRunner{output: "TestRegression expected fixed", failAt: 2}
	checker.Runner = runner
	request := regressionRequest()
	request.Commands = [][]string{{"test"}}
	var log strings.Builder
	if err := checker.Verify(context.Background(), workspace, request, &log); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 || !strings.Contains(log.String(), "candidate phase") || !strings.Contains(log.String(), "baseline phase") {
		t.Fatal("candidate/baseline phases missing")
	}
	copies := 0
	for _, args := range docker.calls {
		if args[0] == "run" && strings.Contains(strings.Join(args, " "), "-copy") {
			copies++
			if copies == 2 {
				last := args[len(args)-1]
				var baseline VerificationBaseline
				if json.Unmarshal([]byte(last), &baseline) != nil || baseline.Revision != testBase {
					t.Fatal("baseline controller JSON absent")
				}
			}
		}
		if strings.Contains(strings.Join(args, " "), "--privileged") || strings.Contains(strings.Join(args, " "), "docker.sock") {
			t.Fatal("additional request changed Docker authority")
		}
	}
	if copies != 2 {
		t.Fatal("baseline reused candidate workspace")
	}
}
func TestBaselineCancellationCannotPass(t *testing.T) {
	checker, _, _, workspace := checkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	checker.Runner = &baselineCommandRunner{output: "TestRegression expected fixed", failAt: 1, cancel: cancel}
	request := regressionRequest()
	if err := checker.check(ctx, workspace, request.Commands, io.Discard, request.Baseline); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled baseline accepted: %v", err)
	}
}

func TestBaselineCopyRealRegressionRedGreen(t *testing.T) {
	root, _ := sourceFixture(t)
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		data, err := SafeGit(ctx, root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	sourceWrite(t, root, "arithmetic.py", "def add_one(value):\n    return value\n")
	git("add", "arithmetic.py")
	git("commit", "-m", "Original arithmetic fixture")
	baselineSHA := git("rev-parse", "HEAD")
	sourceWrite(t, root, "arithmetic.py", "def add_one(value):\n    return value + 1\n")
	sourceWrite(t, root, "test_regression.py", "import unittest\nfrom arithmetic import add_one\n\nclass RegressionTest(unittest.TestCase):\n    def test_add_one_regression(self):\n        self.assertEqual(add_one(2), 3)\n")
	git("add", "arithmetic.py", "test_regression.py")
	git("commit", "-m", "Candidate arithmetic fixture")
	statusBefore := git("status", "--porcelain")
	inputs := t.TempDir()
	export := func(baseline *VerificationBaseline) string {
		t.Helper()
		destination := t.TempDir()
		script := strings.NewReplacer("/source", root, "/workspace", destination, "/inputs", inputs).Replace(copyCheckWorkspace)
		script = "import os\nos.chown = lambda *args, **kwargs: None\n" + script
		args := []string{"-c", script}
		if baseline != nil {
			data, _ := json.Marshal(baseline)
			args = append(args, string(data))
		}
		cmd := exec.Command("python3", args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("production snapshot helper: %s %v", out, err)
		}
		return destination
	}
	baseline := &VerificationBaseline{Revision: baselineSHA, Paths: []string{"arithmetic.py"}, ExpectedExitCode: 1, FailureContains: []string{"test_add_one_regression", "AssertionError: 2 != 3"}}
	candidate := export(nil)
	original := export(baseline)
	runTest := func(directory string) (string, error) {
		cmd := exec.Command("python3", "-m", "unittest", "discover", "-v", "-s", ".", "-p", "test_regression.py")
		cmd.Dir = directory
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := runTest(candidate); err != nil {
		t.Fatalf("candidate regression: %s %v", out, err)
	}
	out, err := runTest(original)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != baseline.ExpectedExitCode {
		t.Fatalf("baseline failed unexpectedly: %s %v", out, err)
	}
	for _, marker := range baseline.FailureContains {
		if !strings.Contains(out, marker) {
			t.Fatalf("intended failure marker %q absent: %s", marker, out)
		}
	}
	if current, err := os.ReadFile(filepath.Join(root, "arithmetic.py")); err != nil || string(current) != "def add_one(value):\n    return value + 1\n" {
		t.Fatal("source workspace changed")
	}
	if git("status", "--porcelain") != statusBefore {
		t.Fatal("fixture source workspace dirty")
	}
}

func TestVerificationCacheStillValidatesFrozenInputs(t *testing.T) {
	for _, state := range []string{"checking", "publishing"} {
		t.Run(state, func(t *testing.T) {
			dir, j := testRun(t)
			j.State = state
			j.Outcome = testOutcome("implemented")
			j.Plan.CheckInputs = []Input{{Path: "config.json", SHA256: strings.Repeat("a", 64)}}
			inputRoot := filepath.Join(dir, "check-inputs")
			if err := os.Mkdir(inputRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(inputRoot, "config.json"), []byte("changed input"), 0600); err != nil {
				t.Fatal(err)
			}
			j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true, Commands: j.Plan.Checks}
			request := regressionRequest()
			j.Verification = []VerificationResult{{Request: request, Key: verificationKey(j, request, testHead, testTree), Head: testHead, Tree: testTree, Passed: true}}
			c := &supplementaryChecker{}
			pub := &fakePublisher{}
			r := fakeRunner(&fakeProvider{}, &c.fakeChecker, pub, &fakeRepository{})
			r.Checker = c
			if err := r.Run(context.Background(), dir, j, ""); err == nil || j.State != "blocked" || len(c.requests) != 0 || c.calls != 0 || pub.calls != 0 {
				t.Fatal("changed frozen inputs used cached success")
			}
		})
	}
}

func TestAdditionalOperationalFailureCanRetryAfterResume(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"setup", errors.New("copy initialization failed")},
		{"transport", errors.New("container inspect unavailable")},
		{"cleanup", errors.New("container cleanup failed")},
		{"cancellation", context.Canceled},
	} {
		t.Run(failure.name, func(t *testing.T) {
			dir, j := testRun(t)
			asked := testOutcome("checks_requested")
			asked.VerificationRequests = []VerificationRequest{regressionRequest()}
			p := &fakeProvider{outcomes: []Outcome{asked, testOutcome("implemented"), testOutcome("reviewed")}}
			c := &supplementaryChecker{err: &VerificationFailure{Phase: "baseline", Err: failure.err}}
			pub := &fakePublisher{}
			r := fakeRunner(p, &c.fakeChecker, pub, &fakeRepository{})
			r.Checker = c
			if err := r.Run(context.Background(), dir, j, ""); err == nil || j.State != "blocked" || len(c.requests) != 1 || pub.calls != 0 {
				t.Fatal("operational failure did not stop")
			}
			loaded, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Verification[0].Key != "" || loaded.Verification[0].Head != testHead || loaded.Verification[0].Tree != testTree || loaded.Verification[0].Log == "" {
				t.Fatal("operational failure was cached or lost its log metadata")
			}
			c.err = nil
			if err := r.Run(context.Background(), dir, &loaded, ""); err != nil {
				t.Fatal(err)
			}
			if loaded.State != "ready" || len(c.requests) != 2 || c.calls != 1 || pub.calls != 1 {
				t.Fatal("recovered infrastructure could not retry unchanged candidate")
			}
		})
	}
}
func TestLongVerificationDiagnosticJournalRoundtrip(t *testing.T) {
	dir, j := testRun(t)
	request := regressionRequest()
	paths := make([]string, 32)
	for i := range paths {
		paths[i] = strings.Repeat("a", 200) + "/example.js"
	}
	failure := &VerificationFailure{Phase: "candidate", Err: &CheckFailure{Command: 1, ExitCode: 1, formatter: true, paths: paths}}
	diagnostic := verificationDiagnostic(request, testTree, failure)
	if len(diagnostic) > 4096 || !strings.Contains(diagnostic, paths[0]) {
		t.Fatal("long diagnostic lost its bounded source metadata")
	}
	j.Verification = []VerificationResult{{Request: request, Diagnostic: diagnostic}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Verification[0].Diagnostic != diagnostic {
		t.Fatalf("diagnostic checkpoint cannot roundtrip: %v", err)
	}
}
