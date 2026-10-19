package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type diagnosticPhaseChecker struct {
	fakeChecker
	requests   []VerificationRequest
	failures   map[string]error
	phases     int
	after      func()
	cleanupErr error
	text       string
}

func (c *diagnosticPhaseChecker) Verify(_ context.Context, _ string, r VerificationRequest, out io.Writer) error {
	c.requests = append(c.requests, r)
	err := c.failures[r.ID]
	w := out.(interface{ commandOutput(int, bool) io.Writer }).commandOutput(1, false)
	text := c.text
	if text == "" {
		text = "diagnostic-result-" + r.ID
	}
	io.WriteString(w, text)
	exit := 0
	if err != nil {
		exit = 1
	}
	w.(interface{ completed(int) }).completed(exit)
	return err
}
func (c *diagnosticPhaseChecker) WithCandidate(_ context.Context, _ string, _ io.Writer, run func(Checker) error) error {
	c.phases++
	err := run(c)
	if c.after != nil {
		c.after()
	}
	return errors.Join(err, c.cleanupErr)
}
func diagnosticRequest(id string) VerificationRequest {
	return VerificationRequest{ID: id, Purpose: "inspect related behavior", Commands: [][]string{{"tests", id}}}
}
func diagnosticOutcome(ids ...string) Outcome {
	outcome := testOutcome("diagnostics_requested")
	for _, id := range ids {
		outcome.DiagnosticRequests = append(outcome.DiagnosticRequests, diagnosticRequest(id))
	}
	return outcome
}

func TestDiagnosticBatchReturnsAllResultsToOriginalSessionThenFullGates(t *testing.T) {
	directory, journal := testRun(t)
	asked := testOutcome("checks_requested")
	asked.VerificationRequests = []VerificationRequest{diagnosticRequest("required")}
	provider := &fakeProvider{outcomes: []Outcome{diagnosticOutcome("first", "second"), asked, testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &diagnosticPhaseChecker{failures: map[string]error{"first": ErrCheckFailed}}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	checker.after = func() {
		if checker.phases == 1 && (checker.calls != 0 || publisher.calls != 0 || journal.Evidence.Passed || journal.Rounds != 0) {
			t.Fatal("diagnostics ran suite, granted clearance, or spent a repair round")
		}
	}
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	if journal.State != "ready" || checker.calls != 1 || publisher.calls != 1 || len(checker.requests) != 3 || checker.requests[2].ID != "required" || journal.Rounds != 0 {
		t.Fatalf("gates or diagnostic lifetime wrong: state=%s configured=%d requests=%v publications=%d rounds=%d", journal.State, checker.calls, checker.requests, publisher.calls, journal.Rounds)
	}
	if len(provider.calls) != 4 || provider.calls[1].ResumeID != testNative || provider.calls[2].ResumeID != testNative || provider.calls[3].ResumeID != "" {
		t.Fatal("diagnostics did not return to the original implementation session")
	}
	feedback := provider.calls[1].Prompt
	if !strings.Contains(feedback, "diagnostic-result-first") || !strings.Contains(feedback, "diagnostic-result-second") || !strings.Contains(feedback, `"passed":false`) || !strings.Contains(feedback, `"passed":true`) || !strings.Contains(feedback, "untrusted") {
		t.Fatal("incomplete batch feedback", feedback)
	}
	if strings.Contains(provider.calls[3].Prompt, "diagnostic-result-") || strings.Contains(provider.calls[3].Prompt, `"diagnostics"`) {
		t.Fatal("diagnostics leaked to independent review")
	}
	if len(journal.Verification) != 1 || !journal.Verification[0].Passed || journal.Diagnostics[0].Passed {
		t.Fatal("diagnostic failure was promoted to required evidence")
	}
}

func TestDiagnosticBatchReplacesOldRequestsAndCachesUnchangedCandidate(t *testing.T) {
	directory, journal := testRun(t)
	provider := &fakeProvider{outcomes: []Outcome{diagnosticOutcome("old"), diagnosticOutcome("old"), diagnosticOutcome("new"), testOutcome("waiting_for_human")}}
	checker := &diagnosticPhaseChecker{failures: map[string]error{"old": ErrCheckFailed}}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if checker.phases != 2 || checker.calls != 0 || len(checker.requests) != 2 || checker.requests[0].ID != "old" || checker.requests[1].ID != "new" || len(journal.Diagnostics) != 1 || journal.Diagnostics[0].Request.ID != "new" || len(journal.Verification) != 0 || publisher.calls != 0 || journal.Rounds != 0 {
		t.Fatal("diagnostics were retained as lifetime gates or cache not used")
	}
}

func TestRepeatedUnchangedDiagnosticFeedbackIsBounded(t *testing.T) {
	directory, journal := testRun(t)
	provider := &fakeProvider{outcomes: []Outcome{diagnosticOutcome("probe"), diagnosticOutcome("probe"), diagnosticOutcome("probe")}}
	checker := &diagnosticPhaseChecker{}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if !strings.Contains(journal.StopReason, "already supplied") || len(checker.requests) != 1 || journal.DiagnosticFeedbackCount != 3 || journal.Rounds != 0 || publisher.calls != 0 {
		t.Fatal("unchanged diagnostic feedback evaded bound")
	}
}

func TestDiagnosticBatchDoesNotRerunOnLaterReadyCandidate(t *testing.T) {
	directory, journal := testRun(t)
	journal.State = "checking"
	journal.SessionID = testNative
	journal.Outcome = diagnosticOutcome("development")
	mergeDiagnostics(journal, journal.Outcome.DiagnosticRequests)
	checker := &diagnosticPhaseChecker{failures: map[string]error{"development": ErrCheckFailed}}
	repository := &fakeRepository{}
	publisher := &fakePublisher{}
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, repository)
	runner.Checker = checker
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	newer := Revision{Head: strings.Repeat("d", 40), Tree: strings.Repeat("e", 40), Clean: true}
	repository.revisions = []Revision{newer}
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	if len(checker.requests) != 1 || checker.calls != 1 || publisher.calls != 1 || strings.Contains(provider.calls[0].Prompt, "diagnostic-result-development") || journal.Diagnostics[0].Key != "" {
		t.Fatal("stale diagnostics replayed or reran after source change")
	}
}

func TestDiagnosticLegacyRequiredFailureStillBlocksPublication(t *testing.T) {
	directory, journal := testRun(t)
	journal.Verification = []VerificationResult{{Request: diagnosticRequest("legacy")}}
	provider := &fakeProvider{outcomes: []Outcome{diagnosticOutcome("development"), testOutcome("checks_requested"), testOutcome("waiting_for_human")}}
	checker := &diagnosticPhaseChecker{failures: map[string]error{"legacy": ErrCheckFailed}}
	publisher := &fakePublisher{}
	runner := fakeRunner(provider, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if publisher.calls != 0 || checker.calls != 1 || len(checker.requests) != 2 || journal.Verification[0].Passed || !strings.Contains(provider.calls[2].Prompt, "diagnostic-result-legacy") {
		t.Fatal("existing required request was retired or demoted")
	}
}

func TestDiagnosticOperationalFailuresDiscardWholeBatchAndHold(t *testing.T) {
	for _, kind := range []string{"command", "cleanup", "stale", "input"} {
		t.Run(kind, func(t *testing.T) {
			directory, journal := testRun(t)
			journal.State = "checking"
			journal.SessionID = testNative
			journal.Outcome = diagnosticOutcome("first", "second")
			mergeDiagnostics(journal, journal.Outcome.DiagnosticRequests)
			journal.Evidence = CheckEvidence{Key: verificationKey(journal, VerificationRequest{}, testHead, testTree), Head: testHead, Tree: testTree, Passed: true}
			checker := &diagnosticPhaseChecker{}
			repository := &fakeRepository{}
			if kind == "command" {
				checker.failures = map[string]error{"second": errors.New("private operational failure")}
			}
			if kind == "cleanup" {
				checker.cleanupErr = errors.New("fixture cleanup failed")
			}
			if kind == "stale" {
				checker.after = func() { repository.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}} }
			}
			if kind == "input" {
				root := filepath.Join(directory, "check-inputs")
				os.Mkdir(root, 0700)
				path := filepath.Join(root, ".env")
				os.WriteFile(path, []byte("fixture=before\n"), 0600)
				journal.Plan.CheckInputs = []Input{{Path: ".env", SHA256: patchDigest("fixture=before\n")}}
				checker.after = func() { os.WriteFile(path, []byte("fixture=after\n"), 0600) }
			}
			publisher := &fakePublisher{}
			provider := &fakeProvider{}
			runner := fakeRunner(provider, &checker.fakeChecker, publisher, repository)
			runner.Checker = checker
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			if journal.State != "blocked" || journal.ResumeState != "checking" || journal.Feedback != "" || journal.Evidence.Passed || publisher.calls != 0 || len(provider.calls) != 0 {
				t.Fatal("unsafe failure dispatched feedback or permitted publication")
			}
			for _, result := range journal.Diagnostics {
				if result.Key != "" || result.Output != nil || result.Passed {
					t.Fatal("provisional batch survived operational hold")
				}
			}
		})
	}
}

func TestDiagnosticSemanticFailurePreservesPassingSuiteAndBoundsOutput(t *testing.T) {
	directory, journal := testRun(t)
	journal.State = "checking"
	journal.SessionID = testNative
	journal.Outcome = diagnosticOutcome("one", "two", "three")
	mergeDiagnostics(journal, journal.Outcome.DiagnosticRequests)
	journal.Evidence = CheckEvidence{Key: verificationKey(journal, VerificationRequest{}, testHead, testTree), Head: testHead, Tree: testTree, Passed: true}
	checker := &diagnosticPhaseChecker{text: strings.Repeat("x", 48*1024), failures: map[string]error{"one": ErrCheckFailed}}
	publisher := &fakePublisher{}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, publisher, &fakeRepository{})
	runner.Checker = checker
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if !journal.Evidence.Passed || journal.Rounds != 0 || checker.calls != 0 || publisher.calls != 0 {
		t.Fatal("semantic diagnostic failure invalidated configured success")
	}
	var feedback []VerificationResult
	begin := strings.Index(journal.Feedback, "[{\"")
	end := strings.LastIndex(journal.Feedback, "]")
	if begin < 0 || end < 0 || json.Unmarshal([]byte(journal.Feedback[begin:end+1]), &feedback) != nil {
		t.Fatal("feedback could not be decoded")
	}
	retained := 0
	for _, result := range feedback {
		for _, out := range result.Output {
			data, err := commandOutputBytes(out)
			if err != nil || !out.Truncated || !out.Completed || out.TotalBytes != 48*1024 {
				t.Fatal("missing truncation or completion evidence")
			}
			retained += len(data)
		}
	}
	if retained > 16*1024 {
		t.Fatal("diagnostic feedback exceeded aggregate budget", retained)
	}
	if _, err := Load(directory); err != nil {
		t.Fatal("bounded diagnostics not loadable", err)
	}
}

func TestDiagnosticOutcomeContract(t *testing.T) {
	good := diagnosticOutcome("probe")
	if err := validateOutcome(good, "implementation"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "review", "mixed", "wrong status", "source patch", "baseline", "repository", "pr assessment", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			outcome := diagnosticOutcome("probe")
			role := "implementation"
			switch kind {
			case "missing":
				outcome.DiagnosticRequests = nil
			case "review":
				role = "review"
			case "mixed":
				outcome.VerificationRequests = []VerificationRequest{diagnosticRequest("required")}
			case "wrong status":
				outcome.Status = "checks_requested"
			case "source patch":
				outcome.DiagnosticRequests[0] = sourcePatchRequest()
			case "baseline":
				outcome.DiagnosticRequests[0] = regressionRequest()
				outcome.DiagnosticRequests[0].Baseline.Revision = "uncaptured"
			case "pr assessment":
				outcome.PRFeedbackAssessments = []PRFeedbackAssessment{{}}
			case "repository":
				outcome.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example/project", Purpose: "context"}}
			case "overflow":
				for i := 1; i < 17; i++ {
					outcome.DiagnosticRequests = append(outcome.DiagnosticRequests, diagnosticRequest(fmt.Sprintf("probe%d", i)))
				}
			}
			if validateOutcome(outcome, role) == nil {
				t.Fatal("invalid diagnostic handoff accepted")
			}
		})
	}
	for _, body := range []string{`{}`, `{"diagnostic_requests":[]}`} {
		var outcome Outcome
		if err := json.Unmarshal([]byte(body), &outcome); err != nil {
			t.Fatal("legacy handoff rejected", err)
		}
	}
	for _, body := range []string{`{"diagnostic_requests":null}`, `{"diagnostic_requests":{}}`} {
		var outcome Outcome
		if json.Unmarshal([]byte(body), &outcome) == nil {
			t.Fatal("malformed diagnostic field accepted")
		}
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(outcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["properties"].(map[string]any)["diagnostic_requests"] == nil {
		t.Fatal("native output schema omitted diagnostics")
	}
}

func TestDiagnosticsPrepareFormattingWithoutRuntimeSuiteDiscovery(t *testing.T) {
	directory, journal := testRun(t)
	journal.State = "checking"
	journal.SessionID = testNative
	journal.Plan.CheckSelection = "runtime"
	journal.Outcome = diagnosticOutcome("probe")
	mergeDiagnostics(journal, journal.Outcome.DiagnosticRequests)
	checker := &diagnosticPhaseChecker{}
	repository := &preparationRepositoryFake{}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &repository.fakeRepository)
	runner.Checker = checker
	runner.Repository = repository
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if repository.discoveries != 1 || checker.calls != 0 || len(checker.requests) != 1 || len(journal.FormatterPreparation) != 0 || len(journal.EffectiveChecks) != 0 || journal.Evidence.Passed {
		t.Fatal("diagnostic phase attempted full readiness preparation")
	}
}

func TestDiagnosticsInterruptedPhaseDiscardsProvisionalResultsBeforeRetry(t *testing.T) {
	directory, journal := testRun(t)
	journal.State = "checking"
	journal.SessionID = testNative
	journal.CandidatePhasePending = true
	journal.Outcome = diagnosticOutcome("probe")
	mergeDiagnostics(journal, journal.Outcome.DiagnosticRequests)
	journal.Diagnostics[0] = VerificationResult{Request: journal.Diagnostics[0].Request, Key: verificationKey(journal, journal.Diagnostics[0].Request, testHead, testTree), Head: testHead, Tree: testTree, Passed: true}
	checker := &diagnosticPhaseChecker{}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &fakeRepository{})
	runner.Checker = checker
	if err := runner.checkCandidatePhase(context.Background(), directory, journal, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if len(checker.requests) != 1 || checker.phases != 1 || journal.CandidatePhasePending || journal.State != "implementing" {
		t.Fatal("interrupted lifecycle reused provisional diagnostic success")
	}
}
