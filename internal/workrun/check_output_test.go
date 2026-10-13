package workrun

import (
	"context"
	"encoding/json"
	"errors"
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
