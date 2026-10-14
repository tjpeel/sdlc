package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCommandOutputRetainsHeadTailAndDrains(t *testing.T) {
	b := &boundedCommandOutput{limit: 8}
	for _, p := range []string{"abcd", "0123456789", "WXYZ"} {
		if n, err := b.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	b.completed(7)
	r := b.snapshot()
	if r.CombinedOutput != "abcdWXYZ" || r.TotalBytes != 18 || r.OmittedBytes != 10 || !r.Truncated || r.HeadBytes != 4 || r.TailBytes != 4 || r.ExitCode != 7 || !r.Completed {
		t.Fatalf("incorrect bounded output: %+v", r)
	}
	r = trimCommandOutput(r, 4)
	if r.CombinedOutput != "abYZ" || r.OmittedBytes != 14 || r.HeadBytes != 2 || r.TailBytes != 2 {
		t.Fatalf("incorrect aggregate truncation: %+v", r)
	}
}

func TestSilentCommandsLeaveOutputBudgetForLaterFailure(t *testing.T) {
	c := newCommandOutputCollector(io.Discard)
	for command := 1; command <= 20; command++ {
		c.commandOutput(command, false).(interface{ completed(int) }).completed(0)
	}
	w := c.commandOutput(21, false)
	text := "unfamiliar assertion failure\n" + strings.Repeat("context\n", 1700)
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	w.(interface{ completed(int) }).completed(1)
	results := c.results()
	last := results[len(results)-1]
	if last.CombinedOutput != text || last.Truncated || !last.Completed || last.ExitCode != 1 {
		t.Fatal("silent setup commands prevented full failure evidence", last)
	}
	j := &Journal{Evidence: CheckEvidence{Output: results}}
	boundJournalOutput(j)
	var packet struct {
		Suite CheckEvidence `json:"suite"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(verificationFeedback(j), "Controller verification evidence: ")), &packet); err != nil {
		t.Fatal(err)
	}
	last = packet.Suite.Output[len(packet.Suite.Output)-1]
	if last.CombinedOutput != text || last.Truncated {
		t.Fatal("silent results consumed the returned evidence budget", last)
	}
}

type genericOutputRunner struct {
	text string
	fail bool
}

func (r genericOutputRunner) Run(_ context.Context, w io.Writer, _ ...string) error {
	_, err := io.WriteString(w, r.text)
	if err != nil {
		return err
	}
	if r.fail {
		return errors.New("synthetic process failure")
	}
	return nil
}

// Deliberately expose the ordinary checker boundary, not phase reuse, so this
// test exercises the existing fake Docker process and journal/session seams.
type commandEvidenceChecker struct{ docker DockerChecker }

func (c commandEvidenceChecker) Check(ctx context.Context, workspace string, commands [][]string, out io.Writer) error {
	return c.docker.Check(ctx, workspace, commands, out)
}

func TestGenericCommandOutputReturnsOnlyToOriginalImplementation(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		fail       bool
	}{{"unknown failure", "arbitrary tool: assertion mismatch\n", true}, {"success", "completed 17 assertions\n", false}, {"empty success", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			checker, _, _, _ := checkFixture(t)
			checker.DockerTests = false
			checker.Runner = genericOutputRunner{text: tc.text, fail: tc.fail}
			workspace, _ := sourceFixture(t)
			directory, j := testRun(t)
			if err := os.Remove(j.Workspace); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(workspace, j.Workspace); err != nil {
				t.Fatal(err)
			}
			provider := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
			if tc.fail {
				provider.outcomes = []Outcome{testOutcome("implemented"), testOutcome("waiting_for_human")}
			}
			runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			runner.Checker = commandEvidenceChecker{docker: checker}
			err := runner.Run(context.Background(), directory, j, "")
			if tc.fail && !errors.Is(err, ErrStopped) || !tc.fail && err != nil {
				t.Fatal(err)
			}
			if len(j.Evidence.Output) != 1 {
				t.Fatalf("missing process output %+v", j.Evidence)
			}
			r := j.Evidence.Output[0]
			if r.CombinedOutput != tc.text || r.TotalBytes != int64(len(tc.text)) || !r.Completed || r.ExitCode != map[bool]int{true: 1, false: 0}[tc.fail] {
				t.Fatalf("wrong process result %+v", r)
			}
			var session Session
			if tc.fail {
				session = provider.calls[1]
			} else {
				if len(provider.assessments) != 1 {
					t.Fatal("new suite was not assessed once")
				}
				session = provider.assessments[0]
			}
			if session.ResumeID != testNative || !strings.Contains(session.Prompt, `"combined_output"`) || !strings.Contains(session.Prompt, `"completed":true`) {
				t.Fatal("original native session did not receive command evidence")
			}
			if !tc.fail && strings.Contains(provider.calls[len(provider.calls)-1].Prompt, `"combined_output"`) {
				t.Fatal("implementation output reached independent reviewer")
			}
			if err := Save(directory, j); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(directory)
			if err != nil || len(loaded.Evidence.Output) != 1 || loaded.Evidence.Output[0].CombinedOutput != tc.text {
				t.Fatal("output did not survive private journal", err)
			}
		})
	}
}

func TestCommandCaptureExcludesControllerAnnouncements(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	checker.DockerTests = false
	checker.Runner = genericOutputRunner{text: "actual process bytes\n"}
	workspace, _ := sourceFixture(t)
	capture := newCommandOutputCollector(io.Discard)
	if err := checker.Check(context.Background(), workspace, [][]string{{"echo", "announcement-must-not-enter-output"}}, capture); err != nil {
		t.Fatal(err)
	}
	output := capture.results()
	if len(output) != 1 || output[0].CombinedOutput != "actual process bytes\n" {
		t.Fatalf("wrapper reached process output: %+v", output)
	}
}

func TestBinaryOutputEncodingPreservesBytesAcrossJSON(t *testing.T) {
	b := &boundedCommandOutput{limit: 4, result: CommandOutput{Command: 1, Phase: "candidate"}}
	b.Write([]byte{0xff, 0, 0xfe, 0x80, 0x81, 0x82})
	b.completed(0)
	r := b.snapshot()
	if r.Encoding != "base64" || r.TotalBytes != 6 || r.OmittedBytes != 2 {
		t.Fatal("binary bytes lost", r)
	}
	data, err := commandOutputBytes(r)
	if err != nil || string(data) != string([]byte{0xff, 0, 0x81, 0x82}) {
		t.Fatal("binary output was replaced", data, err)
	}
	directory, j := testRun(t)
	j.Evidence.Output = []CommandOutput{r}
	if err := Save(directory, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err = commandOutputBytes(loaded.Evidence.Output[0])
	if err != nil || string(data) != string([]byte{0xff, 0, 0x81, 0x82}) {
		t.Fatal("journal replaced binary bytes", data, err)
	}
}
func TestRetainedOutputAggregateBoundIsHonest(t *testing.T) {
	j := &Journal{}
	for i := 0; i < 16; i++ {
		c := newCommandOutputCollector(io.Discard)
		for k := 0; k < 32; k++ {
			w := c.commandOutput(k%16+1, k >= 16)
			w.Write([]byte(strings.Repeat("x", 8192)))
			w.(interface{ completed(int) }).completed(0)
		}
		j.Verification = append(j.Verification, VerificationResult{Output: c.results()})
	}
	boundJournalOutput(j)
	retained := 0
	for _, v := range j.Verification {
		for _, r := range v.Output {
			retained += len(r.CombinedOutput)
			if r.TotalBytes != 8192 || r.OmittedBytes != 8192-int64(len(r.CombinedOutput)) || !r.Truncated {
				t.Fatal("dishonest aggregate omission", r)
			}
		}
	}
	if retained > 16*1024 {
		t.Fatal("retained output exceeds aggregate budget", retained)
	}
	if err := validateCommandEvidence(j); err != nil {
		t.Fatal(err)
	}
	feedback := verificationFeedback(j)
	if len(feedback) > 256*1024 {
		t.Fatal("feedback grows beyond bounded command results", len(feedback))
	}
}

func TestFailedCommandRetainsMiddleFailureThroughAggregateBudget(t *testing.T) {
	collector := newCommandOutputCollector(io.Discard)
	failed := collector.commandOutput(1, false)
	setup := strings.Repeat("restoring synthetic packages\n", 440)
	failure := "[xUnit.net 00:00:01.20] ExampleTests.MiddleFailure [FAIL]\n  Failed ExampleTests.MiddleFailure [12 ms]\n  Error Message:\n   Assert.Equal() Failure\n  Stack Trace:\n     at ExampleTests.MiddleFailure() in /workspace/tests/ExampleTests.cs:line 42\n" + strings.Repeat("     at ExampleTests.FakeContext() in /workspace/tests/Context.cs:line 9\n", 40) + "/workspace/src/Example.cs(17,3): error CS0246: The type 'ExampleType' could not be found\n"
	cleanup := strings.Repeat("disposing synthetic test resources\n", 500)
	raw := setup + failure + cleanup
	for start := 0; start < len(raw); start += 317 {
		end := start + 317
		if end > len(raw) {
			end = len(raw)
		}
		failed.Write([]byte(raw[start:end]))
	}
	failed.(interface{ completed(int) }).completed(1)
	successful := collector.commandOutput(2, false)
	io.WriteString(successful, strings.Repeat("independent success\n", 380))
	successful.(interface{ completed(int) }).completed(0)
	output := collector.results()
	journal := Journal{Diagnostics: []VerificationResult{{Output: output}}}
	boundOutputs(&journal, 16*1024)
	r := journal.Diagnostics[0].Output[0]
	data, err := commandOutputBytes(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, actual := range []string{"ExampleTests.MiddleFailure [FAIL]", "ExampleTests.cs:line 42", "error CS0246"} {
		if !strings.Contains(string(data), actual) {
			t.Fatal("middle failure context omitted after aggregate trimming", actual)
		}
	}
	if r.FailureBytes <= 0 || r.FailureBytes > 8*1024 || r.FailureOffset != int64(len(setup)) {
		t.Fatal("failure segment identity not retained", r)
	}
	if string(data[:r.HeadBytes]) != raw[:r.HeadBytes] || string(data[r.HeadBytes:r.HeadBytes+r.FailureBytes]) != raw[int(r.FailureOffset):int(r.FailureOffset)+r.FailureBytes] || string(data[len(data)-r.TailBytes:]) != raw[len(raw)-r.TailBytes:] {
		t.Fatal("retained segments no longer describe actual process bytes")
	}
	if !r.Completed || r.ExitCode != 1 || r.TotalBytes != int64(len(raw)) || r.OmittedBytes != int64(len(raw)-len(data)) || !r.Truncated {
		t.Fatal("selection changed actual process metadata", r)
	}
	if err := validateCommandEvidence(&journal); err != nil {
		t.Fatal(err)
	}
	other := journal.Diagnostics[0].Output[1]
	if other.ExitCode != 0 || !other.Completed || !strings.Contains(other.CombinedOutput, "independent success") {
		t.Fatal("independent result lost")
	}
	retained := 0
	for _, r := range journal.Diagnostics[0].Output {
		bytes, err := commandOutputBytes(r)
		if err != nil {
			t.Fatal(err)
		}
		retained += len(bytes)
	}
	if retained > 16*1024 {
		t.Fatal("failure context widened aggregate budget", retained)
	}
}

func TestSuccessfulMarkersDoNotChangeHeadTailSelection(t *testing.T) {
	raw := strings.Repeat("setup\n", 30) + "error CS0246: synthetic expected probe\n[xUnit.net] Example.ExpectedProbe [FAIL]\n" + strings.Repeat("cleanup\n", 30)
	capture := &boundedCommandOutput{limit: 64}
	capture.Write([]byte(raw))
	capture.completed(0)
	result := capture.snapshot()
	if result.CombinedOutput != raw[:32]+raw[len(raw)-32:] || result.FailureBytes != 0 || result.FailureOffset != 0 {
		t.Fatal("successful output expectations changed", result)
	}
}

func TestFailureSegmentsPreserveUTF8AndValidateOriginalRanges(t *testing.T) {
	raw := strings.Repeat("setup noise\n", 1200) + "/workspace/Example.cs(17,3): error CS0246: synthetic Ω type\n" + strings.Repeat("  at ExampleTests.ContextΩ()\n", 450) + strings.Repeat("cleanup noise\n", 1300)
	capture := &boundedCommandOutput{limit: 16 * 1024, result: CommandOutput{Command: 1, Phase: "candidate"}}
	for start := 0; start < len(raw); start += 19 {
		end := start + 19
		if end > len(raw) {
			end = len(raw)
		}
		capture.Write([]byte(raw[start:end]))
	}
	capture.completed(1)
	result := trimCommandOutput(capture.snapshot(), 4097)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CommandOutput
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	data, err := commandOutputBytes(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "synthetic Ω type") || decoded.FailureBytes != 4097 || decoded.HeadBytes != 0 || decoded.TailBytes != 0 {
		t.Fatal("UTF8 failure context lost under tight aggregate budget", decoded)
	}
	if string(data) != raw[int(decoded.FailureOffset):int(decoded.FailureOffset)+decoded.FailureBytes] {
		t.Fatal("JSON encoding changed the selected raw bytes")
	}
	_, journal := testRun(t)
	journal.Evidence.Output = []CommandOutput{decoded}
	if err := validateCommandEvidence(journal); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"negative offset", "overlap head", "past original", "successful context", "oversize context", "offset without context"} {
		t.Run(kind, func(t *testing.T) {
			bad := decoded
			switch kind {
			case "negative offset":
				bad.FailureOffset = -1
			case "overlap head":
				bad.HeadBytes = 1
				bad.FailureBytes--
				bad.FailureOffset = 0
			case "past original":
				bad.FailureOffset = bad.TotalBytes
			case "successful context":
				bad.ExitCode = 0
			case "oversize context":
				bad.FailureBytes = 8*1024 + 1
			case "offset without context":
				bad.FailureBytes = 0
			}
			journal.Evidence.Output = []CommandOutput{bad}
			if validateCommandEvidence(journal) == nil {
				t.Fatal("invalid failure segment accepted")
			}
		})
	}
}

func TestCompleteFailureStillPrioritisedWhenAggregateLaterTruncates(t *testing.T) {
	raw := strings.Repeat("setup\n", 600) + "[FAIL] ExampleTests.CompleteFailure\n at ExampleTests.CompleteFailure() in ExampleTests.cs:line 71\n" + strings.Repeat("cleanup\n", 700)
	capture := &boundedCommandOutput{limit: 16 * 1024, result: CommandOutput{Command: 1, Phase: "candidate"}}
	capture.Write([]byte(raw))
	capture.completed(1)
	original := capture.snapshot()
	if original.CombinedOutput != raw || original.Truncated {
		t.Fatal("complete failed output changed")
	}
	trimmed := trimCommandOutput(original, 2048)
	data, err := commandOutputBytes(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExampleTests.CompleteFailure") || !strings.Contains(string(data), "line 71") || trimmed.FailureBytes != 2048 {
		t.Fatal("later aggregate truncation lost complete failure context", trimmed)
	}
	if err := validateCommandEvidence(&Journal{Evidence: CheckEvidence{Output: []CommandOutput{trimmed}}}); err != nil {
		t.Fatal(err)
	}
}

func TestFailureMarkerAcrossLongLineCaptureBlocks(t *testing.T) {
	setup := strings.Repeat("setup noise\n", 1100)
	raw := setup + strings.Repeat("x", 1022) + "[FAIL] ExampleTests.BoundaryFailure at ExampleTests.cs:line 53" + strings.Repeat("x", 5000) + "\n" + strings.Repeat("cleanup noise\n", 1500)
	capture := &boundedCommandOutput{limit: 16 * 1024, result: CommandOutput{Command: 1, Phase: "candidate"}}
	capture.Write([]byte(raw))
	capture.completed(1)
	result := trimCommandOutput(capture.snapshot(), 4096)
	data, err := commandOutputBytes(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[FAIL] ExampleTests.BoundaryFailure") || !strings.Contains(string(data), "line 53") {
		t.Fatal("marker split across bounded scan blocks was discarded")
	}
	if result.FailureBytes == 0 || string(data) != raw[int(result.FailureOffset):int(result.FailureOffset)+len(data)] {
		t.Fatal("boundary capture has incorrect original offsets", result)
	}
	if err := validateCommandEvidence(&Journal{Evidence: CheckEvidence{Output: []CommandOutput{result}}}); err != nil {
		t.Fatal(err)
	}
}

func TestSmallFailureBudgetKeepsMarkerInsteadOfPrefixNoise(t *testing.T) {
	raw := strings.Repeat("setup noise\n", 1100) + strings.Repeat("x", 300) + "[FAIL] ExampleTests.SmallBudgetFailure detail" + strings.Repeat("x", 5000) + "\n" + strings.Repeat("cleanup noise\n", 1500)
	for _, initial := range []int{128, 16 * 1024} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			capture := &boundedCommandOutput{limit: initial, result: CommandOutput{Command: 1, Phase: "candidate"}}
			capture.Write([]byte(raw))
			capture.completed(1)
			result := trimCommandOutput(capture.snapshot(), 128)
			data, err := commandOutputBytes(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "[FAIL] ExampleTests.SmallBudgetFailure detail") {
				t.Fatal("small budget retained prefix noise instead of actual failure marker")
			}
			if len(data) > 128 || result.ExitCode != 1 || result.TotalBytes != int64(len(raw)) || result.OmittedBytes != int64(len(raw)-len(data)) {
				t.Fatal("small marker selection changed bounds or process metadata", result)
			}
			if string(data) != raw[int(result.FailureOffset):int(result.FailureOffset)+len(data)] {
				t.Fatal("recentered failure offset no longer describes raw bytes", result)
			}
			if err := validateCommandEvidence(&Journal{Evidence: CheckEvidence{Output: []CommandOutput{result}}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
