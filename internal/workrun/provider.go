package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/providerauth"
)

type NativeProvider struct {
	Manager providerauth.Manager
	ImageID string
}

func (provider NativeProvider) Status(ctx context.Context, name string) (string, error) {
	return provider.Manager.Status(ctx, name)
}

func (provider NativeProvider) Execute(ctx context.Context, session Session, stdout, stderr io.Writer) (SessionResult, error) {
	if err := os.MkdirAll(session.Directory, 0777); err != nil {
		return SessionResult{}, fmt.Errorf("cannot prepare native session storage")
	}
	if err := os.Chmod(session.Directory, 0777); err != nil {
		return SessionResult{}, err
	}
	control, err := os.MkdirTemp(filepath.Dir(session.Directory), ".provider-input-")
	if err != nil {
		return SessionResult{}, err
	}
	defer os.RemoveAll(control)
	prompt, schema, shared := filepath.Join(control, "prompt.txt"), filepath.Join(control, "schema.json"), filepath.Join(control, "instructions.md")
	for path, content := range map[string]string{prompt: session.Prompt, schema: session.Schema, shared: session.Instructions} {
		if err := os.WriteFile(path, []byte(content), 0444); err != nil {
			return SessionResult{}, err
		}
		if err := os.Chmod(path, 0444); err != nil {
			return SessionResult{}, err
		}
	}
	events := &eventWriter{provider: session.Model.Provider, output: stdout}
	err = provider.Manager.Headless(ctx, providerauth.HeadlessRequest{
		Provider: session.Model.Provider, Model: session.Model.Name, Effort: session.Model.Effort,
		Workspace: session.Workspace, SessionDirectory: session.Directory, PromptFile: prompt, SchemaFile: schema,
		ResumeID: session.ResumeID, ReadOnly: session.Role == "review",
		ImageID: provider.ImageID, InstructionsFile: shared, Headroom: session.Headroom,
		OnHeadroomStats: func(stats headroom.Stats) { events.result.HeadroomStats = &stats },
	}, events, stderr)
	if session.ResumeID != "" {
		reportedID := events.result.SessionID
		events.result.SessionID = session.ResumeID
		if err == nil && reportedID != session.ResumeID {
			return events.result, fmt.Errorf("provider did not resume the requested native session; job stopped")
		}
	}
	if err != nil {
		return events.result, err
	}
	if err = events.finish(session.Role); err != nil {
		return events.result, err
	}
	if events.result.ReportedModel != "" && events.result.ReportedModel != session.Model.Name {
		return events.result, fmt.Errorf("provider reported model %q instead of requested %q; job stopped", events.result.ReportedModel, session.Model.Name)
	}
	return events.result, nil
}

var sessionIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type eventWriter struct {
	provider          string
	output            io.Writer
	buffer            []byte
	final             json.RawMessage
	result            SessionResult
	completed, failed bool
	modelChanged      bool
	err               error
}

func (writer *eventWriter) Write(data []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	if writer.output != nil {
		if _, err := writer.output.Write(data); err != nil {
			writer.err = err
			return 0, err
		}
	}
	writer.buffer = append(writer.buffer, data...)
	for {
		at := bytes.IndexByte(writer.buffer, '\n')
		if at < 0 {
			break
		}
		line := writer.buffer[:at]
		writer.buffer = writer.buffer[at+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > 8*1024*1024 {
			writer.err = fmt.Errorf("provider event exceeds 8 MiB")
			return 0, writer.err
		}
		if err := writer.consume(line); err != nil {
			writer.err = err
			return 0, err
		}
	}
	if len(writer.buffer) > 8*1024*1024 {
		writer.err = fmt.Errorf("provider event exceeds 8 MiB")
		return 0, writer.err
	}
	return len(data), nil
}

func (writer *eventWriter) consume(line []byte) error {
	var event struct {
		Type       string          `json:"type"`
		Subtype    string          `json:"subtype"`
		ThreadID   string          `json:"thread_id"`
		SessionID  string          `json:"session_id"`
		Model      string          `json:"model"`
		ParentTool json.RawMessage `json:"parent_tool_use_id"`
		IsError    bool            `json:"is_error"`
		Structured json.RawMessage `json:"structured_output"`
		Result     string          `json:"result"`
		Message    struct {
			Model string `json:"model"`
		} `json:"message"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type == "" {
		return fmt.Errorf("provider emitted an invalid JSON event")
	}
	reported := ""
	// Subagents intentionally use other models. Only the main session's model
	// is compared with its requested role; nested events remain in the log.
	if len(event.ParentTool) == 0 || bytes.Equal(bytes.TrimSpace(event.ParentTool), []byte("null")) {
		reported = event.Model
		if event.Message.Model != "" {
			reported = event.Message.Model
		}
	}
	if reported != "" {
		if writer.result.ReportedModel != "" && writer.result.ReportedModel != reported {
			writer.modelChanged = true
		}
		writer.result.ReportedModel = reported
	}
	if event.Type == "error" || event.Type == "turn.failed" {
		writer.failed = true
	}
	if writer.provider == "codex" {
		if event.Type == "thread.started" {
			writer.result.SessionID = event.ThreadID
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			writer.final = json.RawMessage(event.Item.Text)
		}
		if event.Type == "turn.completed" {
			writer.completed = true
		}
	} else {
		if event.Type == "system" && event.Subtype == "init" {
			writer.result.SessionID = event.SessionID
		}
		if event.Type == "result" {
			writer.completed = event.Subtype == "success" && !event.IsError
			writer.failed = writer.failed || !writer.completed
			if event.SessionID != "" {
				writer.result.SessionID = event.SessionID
			}
			writer.final = event.Structured
		}
	}
	return nil
}

func (writer *eventWriter) finish(role string) error {
	if writer.err != nil {
		return writer.err
	}
	if len(bytes.TrimSpace(writer.buffer)) > 0 {
		if err := writer.consume(writer.buffer); err != nil {
			return err
		}
		writer.buffer = nil
	}
	if writer.failed || !writer.completed || !sessionIdentifier.MatchString(writer.result.SessionID) {
		return fmt.Errorf("provider did not complete a resumable session successfully")
	}
	if writer.modelChanged {
		return fmt.Errorf("provider reported a model change during the session; job stopped")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(writer.final, &fields) != nil {
		return fmt.Errorf("provider did not return a JSON handoff")
	}
	for _, key := range []string{"status", "summary", "questions", "findings", "local_review", "limitations", "pr_title", "pr_body"} {
		if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("provider handoff is missing required field %s", key)
		}
	}
	if value, ok := fields["verification_requests"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("verification_requests must be an array")
	}
	decoder := json.NewDecoder(bytes.NewReader(writer.final))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&writer.result.Outcome) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("provider did not return a valid structured handoff")
	}
	return validateOutcome(writer.result.Outcome, role)
}

func validateOutcome(outcome Outcome, role string) error {
	if err := validateVerificationRequests(outcome.VerificationRequests, nil); err != nil {
		return err
	}
	if (role != "implementation" || outcome.Status != "checks_requested") && len(outcome.VerificationRequests) > 0 {
		return fmt.Errorf("additional verification requests require implementation checks_requested")
	}
	if outcome.Summary == "" || outcome.Questions == nil || outcome.Findings == nil || outcome.Limitations == nil {
		return fmt.Errorf("provider handoff is incomplete")
	}
	switch outcome.Status {
	case "waiting_for_human":
		if len(outcome.Questions) == 0 {
			return fmt.Errorf("waiting handoff must include a human question")
		}
	case "blocked", "failed":
	case "checks_requested":
		if role != "implementation" {
			return fmt.Errorf("review must use the supplied verification evidence")
		}
	case "implemented":
		if role != "implementation" || !outcome.LocalReview || len(outcome.Questions) > 0 || len(outcome.Findings) > 0 || len(outcome.Limitations) > 0 || outcome.PRTitle == "" || outcome.PRBody == "" {
			return fmt.Errorf("implementation handoff does not satisfy local review and PR metadata")
		}
	case "reviewed":
		if role != "review" || len(outcome.Questions) > 0 || len(outcome.Limitations) > 0 {
			return fmt.Errorf("independent review is incomplete")
		}
	default:
		return fmt.Errorf("provider returned an unknown handoff status")
	}
	for _, finding := range outcome.Findings {
		if !safeRelative(finding.Path) || finding.Line < 1 || finding.Scenario == "" || finding.Recommendation == "" || !regexp.MustCompile(`^P[0-3]$`).MatchString(finding.Priority) {
			return fmt.Errorf("review finding needs a priority, file/line, failure scenario and recommendation")
		}
	}
	return nil
}

func safeRelative(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

const outcomeSchema = `{"type":"object","additionalProperties":false,"required":["status","summary","questions","findings","local_review","limitations","pr_title","pr_body","verification_requests"],"properties":{"status":{"type":"string","enum":["implemented","checks_requested","reviewed","waiting_for_human","blocked","failed"],"description":"Use implemented or reviewed only when that role is complete with no unresolved questions or limitations. Actionable review findings may remain for the implementer to repair."},"summary":{"type":"string","description":"Describe work, evidence and ordinary scope boundaries here, distinguishing controller verification from checks you ran yourself."},"questions":{"type":"array","items":{"type":"string"}},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["priority","path","line","scenario","recommendation"],"properties":{"priority":{"type":"string","enum":["P0","P1","P2","P3"]},"path":{"type":"string"},"line":{"type":"integer"},"scenario":{"type":"string"},"recommendation":{"type":"string"}}}},"local_review":{"type":"boolean"},"limitations":{"type":"array","description":"Only unresolved obstacles that prevent completing this role. Normal scope or isolation boundaries already covered by supplied verification evidence belong in summary. Return an empty array when complete; never omit a genuine missing requirement, tool, required review or verification gap.","items":{"type":"string"}},"pr_title":{"type":"string"},"pr_body":{"type":"string"},"verification_requests":{"type":"array","maxItems":16,"description":"Machine work for the controller, not human questions. Return [] when unused. Generic commands must pass. A baseline runs the same commands against candidate then a fresh candidate copy with only named original production blobs restored from captured StartingSHA or SourceSHA; final command must fail with the expected code and literal markers. Example: id regression, purpose prove new test catches old behavior, commands [[\"go\",\"test\",\"./internal/example\"]], baseline revision captured source SHA, paths [\"internal/example/example.go\"], expected_exit_code 1, failure_contains [\"TestRegression\"].","items":{"type":"object","additionalProperties":false,"required":["id","purpose","commands","baseline"],"properties":{"id":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"purpose":{"type":"string","maxLength":1024},"commands":{"type":"array","items":{"type":"array","items":{"type":"string"}}},"baseline":{"anyOf":[{"type":"object","additionalProperties":false,"required":["revision","paths","expected_exit_code","failure_contains"],"properties":{"revision":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"expected_exit_code":{"type":"integer","minimum":1,"maximum":255},"failure_contains":{"type":"array","items":{"type":"string"}}}},{"type":"null"}]}}}}}}`
