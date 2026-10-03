package workrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func codexEvents(outcome Outcome) string {
	data, _ := json.Marshal(outcome)
	message, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": string(data)}})
	return `{"type":"thread.started","thread_id":"` + testNative + `"}` + "\n" + string(message) + "\n" + `{"type":"turn.completed"}`
}
func TestEventWriterStreamsSplitEventsAndFinalLine(t *testing.T) {
	var output bytes.Buffer
	w := eventWriter{provider: "codex", output: &output}
	data := codexEvents(testOutcome("implemented"))
	for start := 0; start < len(data); start += 7 {
		end := start + 7
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write([]byte(data[start:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.finish("implementation"); err != nil {
		t.Fatal(err)
	}
	if output.String() != data || w.result.SessionID != testNative || w.result.Outcome.Status != "implemented" {
		t.Fatalf("stream or handoff lost: %+v", w.result)
	}
}
func TestEventWriterRejectsFailedIncompleteAndInvalidHandoffs(t *testing.T) {
	cases := map[string]string{"invalid JSON": "not JSON\n", "missing completion": strings.TrimSuffix(codexEvents(testOutcome("implemented")), `{"type":"turn.completed"}`), "failed turn": codexEvents(testOutcome("implemented")) + "\n" + `{"type":"turn.failed"}`, "missing session": `{"type":"turn.completed"}`, "invalid native identifier": strings.ReplaceAll(codexEvents(testOutcome("implemented")), testNative, "../escape"), "unknown final field": strings.Replace(codexEvents(testOutcome("implemented")), `\"status\":`, `\"extra\":true,\"status\":`, 1)}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			w := eventWriter{provider: "codex"}
			_, err := w.Write([]byte(data))
			if err == nil {
				err = w.finish("implementation")
			}
			if err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}
func TestClaudeUsesStructuredOutputAndRecordsActualModel(t *testing.T) {
	o := testOutcome("reviewed")
	data, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": o, "result": "non-JSON prose must be ignored"})
	w := eventWriter{provider: "claude"}
	init := `{"type":"system","subtype":"init","session_id":"` + testNative + `","model":"reported-model"}`
	if _, err := w.Write([]byte(init + "\n" + string(data))); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("review"); err != nil {
		t.Fatal(err)
	}
	if w.result.ReportedModel != "reported-model" || w.result.Outcome.Status != "reviewed" {
		t.Fatal("model or structured outcome lost")
	}
}
func TestClaudeResultProseIsNotStructuredOutput(t *testing.T) {
	w := eventWriter{provider: "claude"}
	data := `{"type":"result","subtype":"success","session_id":"` + testNative + `","result":"{\"status\":\"reviewed\"}"}`
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("review"); err == nil {
		t.Fatal("accepted prose instead of structured output")
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func TestEventWriterRetainsOutputFailure(t *testing.T) {
	w := eventWriter{provider: "codex", output: failingOutput{}}
	if _, err := w.Write([]byte(codexEvents(testOutcome("implemented")))); err == nil {
		t.Fatal("lost output failure")
	}
	if err := w.finish("implementation"); err == nil {
		t.Fatal("finished after output failure")
	}
}
func TestOutcomeRejectsIncompleteReviewAndImplementation(t *testing.T) {
	cases := []struct {
		name, role string
		outcome    Outcome
	}{{"missing local review", "implementation", testOutcome("implemented")}, {"missing metadata", "implementation", testOutcome("implemented")}, {"missing arrays", "implementation", testOutcome("implemented")}, {"question absent", "implementation", testOutcome("waiting_for_human")}, {"review cannot request checks", "review", testOutcome("checks_requested")}, {"implementation cannot review", "implementation", testOutcome("reviewed")}, {"unsafe finding", "review", testOutcome("reviewed")}, {"review limitation", "review", testOutcome("reviewed")}}
	cases[0].outcome.LocalReview = false
	cases[1].outcome.PRBody = ""
	cases[2].outcome.Limitations = nil
	cases[6].outcome.Findings = []Finding{{Priority: "P1", Path: "../private", Line: 1, Scenario: "failure", Recommendation: "fix"}}
	cases[7].outcome.Limitations = []string{"required evidence missing"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOutcome(tc.outcome, tc.role); err == nil {
				t.Fatal("invalid handoff accepted")
			}
		})
	}
}

func TestEventWriterRequiresEverySchemaField(t *testing.T) {
	for _, field := range []string{"status", "summary", "questions", "findings", "local_review", "limitations", "pr_title", "pr_body"} {
		t.Run(field, func(t *testing.T) {
			data, _ := json.Marshal(testOutcome("reviewed"))
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, field)
			result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": fields})
			w := eventWriter{provider: "claude"}
			if _, err := w.Write(result); err != nil {
				t.Fatal(err)
			}
			if err := w.finish("review"); err == nil {
				t.Fatalf("missing required %s accepted", field)
			}
		})
	}
}
func TestNativeEventsCaptureReportedModels(t *testing.T) {
	for _, tc := range []struct{ name, provider, event string }{{"codex top level", "codex", `{"type":"thread.started","thread_id":"` + testNative + `","model":"actual-model"}`}, {"claude assistant", "claude", `{"type":"assistant","message":{"model":"actual-model"}}`}} {
		t.Run(tc.name, func(t *testing.T) {
			w := eventWriter{provider: tc.provider}
			if _, err := w.Write([]byte(tc.event + "\n")); err != nil {
				t.Fatal(err)
			}
			if w.result.ReportedModel != "actual-model" {
				t.Fatal("reported model lost")
			}
		})
	}
}
func TestEventWriterStopsModelChangeWithinNativeSession(t *testing.T) {
	w := eventWriter{provider: "codex"}
	data := `{"type":"thread.started","thread_id":"` + testNative + `","model":"first-model"}` + "\n" + `{"type":"item.started","model":"different-model"}` + "\n" + codexEvents(testOutcome("implemented"))
	_, err := w.Write([]byte(data))
	if err == nil {
		err = w.finish("implementation")
	}
	if err == nil {
		t.Fatal("native model change accepted")
	}
}

func claudeResultEvent(t *testing.T, outcome any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": testNative, "structured_output": outcome})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func TestClaudeSubagentModelDoesNotChangeMainSelectedModel(t *testing.T) {
	w := eventWriter{provider: "claude"}
	data := `{"type":"system","subtype":"init","session_id":"` + testNative + `","model":"selected-main-model"}` + "\n" + `{"type":"assistant","parent_tool_use_id":"subagent-tool-1","message":{"model":"different-subagent-model"}}` + "\n" + claudeResultEvent(t, testOutcome("reviewed"))
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("review"); err != nil {
		t.Fatalf("subagent model incorrectly invalidated main session: %v", err)
	}
	if w.result.ReportedModel != "selected-main-model" {
		t.Fatalf("main model overwritten: %q", w.result.ReportedModel)
	}
}
func TestClaudeMainAssistantModelChangeStopsSession(t *testing.T) {
	for _, parent := range []string{"", `,"parent_tool_use_id":null`} {
		t.Run(map[bool]string{true: "absent", false: "null"}[parent == ""], func(t *testing.T) {
			w := eventWriter{provider: "claude"}
			data := `{"type":"system","subtype":"init","session_id":"` + testNative + `","model":"selected-main-model"}` + "\n" + `{"type":"assistant"` + parent + `,"message":{"model":"different-main-model"}}` + "\n" + claudeResultEvent(t, testOutcome("reviewed"))
			_, err := w.Write([]byte(data))
			if err == nil {
				err = w.finish("review")
			}
			if err == nil {
				t.Fatal("main assistant model change accepted")
			}
		})
	}
}
func TestEventWriterRejectsNullRequiredScalarFields(t *testing.T) {
	for _, field := range []string{"status", "summary", "local_review", "pr_title", "pr_body"} {
		t.Run(field, func(t *testing.T) {
			data, _ := json.Marshal(testOutcome("checks_requested"))
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage(`null`)
			w := eventWriter{provider: "claude"}
			if _, err := w.Write([]byte(claudeResultEvent(t, fields))); err != nil {
				t.Fatal(err)
			}
			if err := w.finish("implementation"); err == nil {
				t.Fatalf("null required scalar %s accepted", field)
			}
		})
	}
}
