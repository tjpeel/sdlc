package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func liveRequest(ids ...string) LiveDiagnosticRequest {
	request := LiveDiagnosticRequest{Version: 1, ID: "12345678-1234-4234-8234-123456789abc", Head: testHead, Tree: testTree}
	for _, id := range ids {
		request.Requests = append(request.Requests, diagnosticRequest(id))
	}
	return request
}
func TestLiveDiagnosticsAreDetachedAndDoNotAdvanceGates(t *testing.T) {
	directory, j := testRun(t)
	j.State = "implementing"
	j.SessionID = testNative
	j.Attempt = 4
	j.Rounds = 2
	output := CommandOutput{Completed: true, Command: 1, Phase: "candidate", CombinedOutput: "retained-required-output", HeadBytes: len("retained-required-output"), TotalBytes: int64(len("retained-required-output"))}
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true, Output: []CommandOutput{output}}
	j.Verification = []VerificationResult{{Request: diagnosticRequest("required"), Key: verificationKey(j, diagnosticRequest("required"), testHead, testTree), Passed: true, Output: []CommandOutput{output}}}
	before, _ := json.Marshal(j.Evidence)
	gates, _ := json.Marshal(j.Verification)
	checker := &diagnosticPhaseChecker{text: strings.Repeat("process-data", 4096), failures: map[string]error{"one": ErrCheckFailed}}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &fakeRepository{})
	runner.Checker = checker
	runner.rawOutput = io.Discard
	response, err := runner.liveDiagnostics(context.Background(), directory, j, liveRequest("one", "two"))
	after, _ := json.Marshal(j.Evidence)
	afterGates, _ := json.Marshal(j.Verification)
	if err != nil || response.Error != "" || len(response.Results) != 2 || response.Results[0].Passed || !response.Results[1].Passed {
		t.Fatal("semantic batch lost", response, err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(gates, afterGates) || j.Attempt != 4 || j.Rounds != 2 || j.State != "implementing" || j.SessionID != testNative || checker.calls != 0 || j.CheckAttempt != 2 {
		t.Fatal("live diagnostics changed required evidence or controller budgets")
	}
	total := 0
	for _, result := range response.Results {
		for _, output := range result.Output {
			data, err := commandOutputBytes(output)
			if err != nil {
				t.Fatal(err)
			}
			total += len(data)
		}
	}
	if total > 16*1024 {
		t.Fatal("live diagnostic output exceeded bound", total)
	}
	if len(j.LiveDiagnostics) != 2 {
		t.Fatal("live metadata missing")
	}
	for _, result := range j.LiveDiagnostics {
		if result.Output != nil {
			t.Fatal("live process output persisted in journal")
		}
	}
}
func TestLiveDiagnosticsRecoverableCandidateErrorsAndFatalFailures(t *testing.T) {
	for _, kind := range []string{"dirty", "stale before", "stale after", "input", "cleanup", "command"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := testRun(t)
			j.State = "implementing"
			j.Attempt = 1
			checker := &diagnosticPhaseChecker{}
			repo := &fakeRepository{}
			switch kind {
			case "dirty":
				repo.revisions = []Revision{{Head: testHead, Tree: testTree, Clean: false}}
			case "stale before":
				repo.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}}
			case "stale after":
				checker.after = func() { repo.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}} }
			case "input":
				os.Mkdir(filepath.Join(directory, "check-inputs"), 0700)
				os.WriteFile(filepath.Join(directory, "check-inputs", ".env"), []byte("fake=before"), 0600)
				j.Plan.CheckInputs = []Input{{Path: ".env", SHA256: patchDigest("fake=before")}}
				checker.after = func() { os.WriteFile(filepath.Join(directory, "check-inputs", ".env"), []byte("fake=after"), 0600) }
			case "cleanup":
				checker.cleanupErr = errors.New("private cleanup diagnostic")
			case "command":
				checker.failures = map[string]error{"one": errors.New("private credential transport")}
			}
			runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, repo)
			runner.Checker = checker
			runner.rawOutput = io.Discard
			response, err := runner.liveDiagnostics(context.Background(), directory, j, liveRequest("one", "two"))
			recoverable := strings.HasPrefix(kind, "stale") || kind == "dirty"
			if recoverable && (err != nil || response.Error == "") {
				t.Fatal("candidate issue stopped original turn", response, err)
			}
			if !recoverable && err == nil {
				t.Fatal("operational failure returned normal diagnostic feedback", response)
			}
			if len(response.Results) != 0 || j.Attempt != 1 || j.State != "implementing" {
				t.Fatal("unsafe failure leaked results or changed native state")
			}
		})
	}
}
func TestLiveDiagnosticMailboxRejectsUnsafeRequests(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "oversize", "duplicate key", "duplicate id", "trailing", "unknown", "bad uuid"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			requests, responses := filepath.Join(root, "requests"), filepath.Join(root, "responses")
			os.Mkdir(requests, 0777)
			os.Mkdir(responses, 0755)
			request := liveRequest("one")
			if kind == "duplicate id" {
				request.Requests = append(request.Requests, request.Requests[0])
			}
			data, _ := json.Marshal(request)
			switch kind {
			case "oversize":
				data = []byte(strings.Repeat("x", liveRequestLimit+1))
			case "duplicate key":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			case "trailing":
				data = append(data, []byte(" {}")...)
			case "unknown":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1)
			case "bad uuid":
				data = bytes.Replace(data, []byte(request.ID), []byte("malformed"), 1)
			}
			target := filepath.Join(requests, "request.json")
			if kind == "symlink" || kind == "hardlink" {
				source := filepath.Join(root, "source")
				os.WriteFile(source, data, 0644)
				if kind == "symlink" {
					os.Symlink(source, target)
				} else {
					os.Link(source, target)
				}
			} else {
				os.WriteFile(target, data, 0644)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			calls := 0
			err := serveDiagnosticMailbox(ctx, requests, responses, func(context.Context, LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
				calls++
				return LiveDiagnosticResponse{}, nil
			})
			validation := kind == "unknown" || kind == "duplicate id"
			if validation {
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(filepath.Join(responses, "response.json"))
				if readErr != nil || !bytes.Contains(data, []byte("Invalid live")) {
					t.Fatal("request validation did not return recoverable tool error", readErr)
				}
			} else if err == nil {
				t.Fatal("unsafe mailbox did not fail closed")
			}
			if calls != 0 {
				t.Fatal("invalid request reached checker")
			}
		})
	}
}
func helperFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	requests, responses, workspace := filepath.Join(root, "requests"), filepath.Join(root, "responses"), filepath.Join(root, "workspace")
	for _, path := range []string{requests, responses, workspace} {
		os.Mkdir(path, 0777)
	}
	formatterGit(t, workspace, "init", "-q")
	os.WriteFile(filepath.Join(workspace, "example.txt"), []byte("first"), 0600)
	formatterGit(t, workspace, "add", ".")
	formatterGit(t, workspace, "commit", "-qm", "Fixture checkpoint")
	script := strings.NewReplacer("/sdlc-diagnostic-requests", requests, "/sdlc-diagnostic-responses", responses, "/workspace", workspace).Replace(liveDiagnosticHelper)
	helper := filepath.Join(root, "helper.py")
	os.WriteFile(helper, []byte(script), 0444)
	return helper, requests, responses, workspace
}
func TestLiveHelpersSerializeThroughMatchingResponseWithoutNativeResume(t *testing.T) {
	helper, requests, responses, _ := helperFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callbackEntered := make(chan string, 2)
	allowFirst := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		calls := 0
		done <- serveDiagnosticMailbox(ctx, requests, responses, func(_ context.Context, request LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
			calls++
			callbackEntered <- request.Requests[0].ID
			if calls == 1 {
				<-allowFirst
			}
			return LiveDiagnosticResponse{Results: []VerificationResult{{Request: request.Requests[0], Passed: true}}}, nil
		})
	}()
	outputs := make([][]byte, 2)
	var wait sync.WaitGroup
	for i, id := range []string{"first", "second"} {
		wait.Add(1)
		go func(i int, id string) {
			defer wait.Done()
			input, _ := json.Marshal([]VerificationRequest{diagnosticRequest(id)})
			command := exec.CommandContext(ctx, "python3", "-I", helper)
			command.Stdin = bytes.NewReader(input)
			data, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("real helper failed: %v %s", err, data)
			}
			outputs[i] = data
		}(i, id)
	}
	<-callbackEntered
	select {
	case <-callbackEntered:
		t.Fatal("second helper overwrote active mailbox")
	case <-time.After(100 * time.Millisecond):
	}
	close(allowFirst)
	wait.Wait()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i, data := range outputs {
		var response LiveDiagnosticResponse
		if json.Unmarshal(data, &response) != nil || response.Error != "" || len(response.Results) != 1 || response.Results[0].Request.ID != []string{"first", "second"}[i] {
			t.Fatal("helper consumed another process response", string(data))
		}
	}
}

type livePatchChecker struct {
	diagnosticPhaseChecker
	patch      *SourcePatchResult
	patchCalls int
}

func (c *livePatchChecker) VerifySourcePatch(_ context.Context, _ string, _ VerificationRequest, _, _ string, _ io.Writer) (*SourcePatchResult, error) {
	c.patchCalls++
	return c.patch, nil
}
func (c *livePatchChecker) WithCandidate(_ context.Context, _ string, _ io.Writer, run func(Checker) error) error {
	c.phases++
	err := run(c)
	if c.after != nil {
		c.after()
	}
	return errors.Join(err, c.cleanupErr)
}
func TestLiveSourceCorrectionReturnsInMixedBatchWithoutRequiredGate(t *testing.T) {
	directory, j := testRun(t)
	j.State = "implementing"
	j.Attempt = 2
	patch := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
	checker := &livePatchChecker{patch: patch}
	checker.failures = map[string]error{"probe": ErrCheckFailed}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, &fakeRepository{})
	runner.Checker = checker
	runner.rawOutput = io.Discard
	request := liveRequest("probe")
	request.Requests = append(request.Requests, sourcePatchRequest())
	response, err := runner.liveDiagnostics(context.Background(), directory, j, request)
	if err != nil || len(response.Results) != 2 || response.Results[0].Passed || response.Results[1].SourcePatch == nil || response.Results[1].SourcePatch.Diff != patch.Diff || response.Results[1].Passed {
		t.Fatal("source correction lost or granted gate", response, err)
	}
	if checker.calls != 0 || checker.patchCalls != 1 || j.Attempt != 2 || j.Rounds != 0 || j.Evidence.Passed || len(j.Verification) != 0 {
		t.Fatal("live correction changed full gates or native budgets")
	}
	if len(j.LiveDiagnostics) != 2 || j.LiveDiagnostics[1].SourcePatch != nil || j.LiveDiagnostics[1].Output != nil {
		t.Fatal("artifact body entered live metadata")
	}
	receipts, _ := filepath.Glob(filepath.Join(directory, "live-source-patch-*.json"))
	if len(receipts) != 1 {
		t.Fatal("private source correction receipt missing")
	}
	if _, err := Load(directory); err != nil {
		t.Fatal("live source correction metadata cannot resume", err)
	}
}
func TestLiveSourceCorrectionStaleAndOversizeWithholdArtifacts(t *testing.T) {
	for _, kind := range []string{"stale", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := testRun(t)
			j.State = "implementing"
			diff := "--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n"
			if kind == "oversize" {
				diff = strings.Repeat("x", maxSourcePatchDiff+1)
			}
			checker := &livePatchChecker{patch: patchFixtureResult(diff)}
			repo := &fakeRepository{}
			if kind == "stale" {
				checker.after = func() { repo.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}} }
			}
			runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, repo)
			runner.Checker = checker
			runner.rawOutput = io.Discard
			request := liveRequest()
			request.Requests = []VerificationRequest{sourcePatchRequest()}
			response, err := runner.liveDiagnostics(context.Background(), directory, j, request)
			if len(response.Results) != 0 || len(j.LiveDiagnostics) != 0 {
				t.Fatal("invalid or stale correction leaked")
			}
			if kind == "oversize" && err == nil {
				t.Fatal("oversized artifact accepted")
			}
			if kind == "stale" && (err != nil || response.Error == "") {
				t.Fatal("stale correction did not produce recoverable retry", err)
			}
			receipts, _ := filepath.Glob(filepath.Join(directory, "live-source-patch-*.json"))
			if len(receipts) != 0 {
				t.Fatal("rejected correction exported receipt")
			}
		})
	}
}
