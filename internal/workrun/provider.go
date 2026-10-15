package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runusage"
)

type NativeProvider struct {
	Manager             providerauth.Manager
	ImageID             string
	diagnosticTransport func(string) (*diagnosticService, error)
}

func (provider NativeProvider) Status(ctx context.Context, name string) (string, error) {
	return provider.Manager.Status(ctx, name)
}

func (provider NativeProvider) Execute(ctx context.Context, session Session, stdout, stderr io.Writer) (result SessionResult, resultErr error) {
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
	nativeCtx, nativeCancel := context.WithCancel(ctx)
	defer nativeCancel()
	requestDirectory, responseDirectory, helper := "", "", ""
	var serviceDone chan error
	var serviceCancel context.CancelFunc
	defer func() {
		if serviceCancel != nil {
			serviceCancel()
			if serviceDone != nil {
				<-serviceDone
			}
		}
	}()
	if session.Diagnostics != nil && liveDiagnosticsSupported {
		if session.Role != "implementation" {
			return SessionResult{}, fmt.Errorf("live diagnostics require implementation role")
		}
		requestDirectory, responseDirectory, helper = filepath.Join(control, "requests"), filepath.Join(control, "responses"), filepath.Join(control, "diagnostics.py")
		for path, mode := range map[string]os.FileMode{requestDirectory: 0777, responseDirectory: 0755} {
			if err := os.Mkdir(path, mode); err != nil {
				return SessionResult{}, err
			}
			if err := os.Chmod(path, mode); err != nil {
				return SessionResult{}, err
			}
		}
		if err := os.WriteFile(helper, []byte(liveDiagnosticHelper), 0444); err != nil {
			return SessionResult{}, err
		}
		startTransport := provider.diagnosticTransport
		if startTransport == nil {
			startTransport = startDiagnosticService
		}
		transport, transportErr := startTransport(responseDirectory)
		if transportErr != nil {
			// Live diagnostics are optional. Keep the existing structured handoff
			// available when this host cannot expose a supported lifetime channel.
			helper, requestDirectory, responseDirectory = "", "", ""
			session.Prompt = withoutLiveDiagnosticInstructions(session.Prompt)
			session.Instructions = withoutLiveDiagnosticInstructions(session.Instructions)
			fmt.Fprintln(stderr, "Live diagnostics unavailable on this host; structured diagnostic handoffs remain available.")
		} else {
			serviceCtx, cancel := context.WithCancel(nativeCtx)
			serviceCancel = cancel
			serviceDone = make(chan error, 1)
			go func() {
				err := serveDiagnosticMailbox(serviceCtx, requestDirectory, responseDirectory, session.Diagnostics, transport)
				if err != nil {
					nativeCancel()
				}
				serviceDone <- err
			}()
		}
	}
	for path, content := range map[string]string{prompt: session.Prompt, schema: session.Schema, shared: session.Instructions} {
		if err := os.WriteFile(path, []byte(content), 0444); err != nil {
			return SessionResult{}, err
		}
		if err := os.Chmod(path, 0444); err != nil {
			return SessionResult{}, err
		}
	}
	events := &eventWriter{provider: session.Model.Provider, output: stdout}
	defer func() {
		if session.Role == "review" && resultErr != nil {
			var interruptionErr error
			result.ReviewInterruption, interruptionErr = unresolvedReviewHandoff(events.final)
			if interruptionErr != nil {
				resultErr = errors.Join(resultErr, interruptionErr)
			}
		}
	}()
	err = provider.Manager.Headless(nativeCtx, providerauth.HeadlessRequest{
		Provider: session.Model.Provider, Model: session.Model.Name, Effort: session.Model.Effort, Speed: session.Speed,
		Workspace: session.Workspace, SessionDirectory: session.Directory, PromptFile: prompt, SchemaFile: schema,
		ResumeID: session.ResumeID, ReadOnly: session.Role == "review",
		ImageID: provider.ImageID, InstructionsFile: shared, Headroom: session.Headroom,
		DiagnosticHelper: helper, DiagnosticRequests: requestDirectory, DiagnosticResponses: responseDirectory,
		OnHeadroomStats: func(stats headroom.Stats) { events.result.HeadroomStats = &stats },
	}, events, stderr)
	if serviceDone != nil {
		serviceCancel()
		err = errors.Join(err, <-serviceDone)
		serviceDone = nil
	}
	// A failed process can still leave an unterminated protocol line. Validate
	// that line before deciding whether the native failure may be quota-related.
	if events.err == nil && len(bytes.TrimSpace(events.buffer)) > 0 {
		events.err = events.consume(events.buffer)
		events.buffer = nil
	}

	// Stream copy failures can surface as process errors from the Docker
	// adapter. Retain the parser/output hold instead of calling them quota stops.
	if events.err != nil {
		return events.result, errors.Join(err, events.err)
	}
	if events.syntheticQuotaError && events.initializedModel != session.Model.Name {
		return events.result, errors.Join(err, errors.New("synthetic Claude quota failure has no matching native initialization"))
	}
	if events.modelChanged || events.result.ReportedModel != "" && events.result.ReportedModel != session.Model.Name {
		return events.result, errors.Join(err, errors.New(providerModelMismatchMessage))
	}
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
var errNativeIncomplete = errors.New("provider did not complete a resumable session successfully")

const providerModelMismatchMessage = "provider reported a different model during the session; job stopped"

type eventWriter struct {
	provider            string
	output              io.Writer
	buffer              []byte
	final               json.RawMessage
	result              SessionResult
	completed, failed   bool
	modelChanged        bool
	syntheticQuotaError bool
	initializedModel    string
	err                 error
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
		Message    json.RawMessage `json:"message"`
		Error      json.RawMessage `json:"error"`
		Item       struct {
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
		var message struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(event.Message, &message) == nil && message.Model != "" {
			reported = message.Model
		}
		var errorCode string
		_ = json.Unmarshal(event.Error, &errorCode)
		if runusage.ClaudeSyntheticQuotaError(writer.provider, event.Type, errorCode, message.Model) {
			writer.syntheticQuotaError = true
			reported = ""
		}
		if writer.provider == "claude" && event.Type == "system" && event.Subtype == "init" {
			writer.initializedModel = reported
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
		return errNativeIncomplete
	}
	if writer.modelChanged {
		return fmt.Errorf("provider reported a model change during the session; job stopped")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(writer.final, &fields) != nil {
		return fmt.Errorf("provider did not return a JSON handoff")
	}
	required := []string{"status", "summary", "questions", "findings", "limitations"}
	if role != "review" {
		required = append(required, "local_review", "pr_title", "pr_body")
	}
	for _, key := range required {
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
	if len(outcome.PRFeedbackAssessments) > maxPRFeedbackComments || role != "implementation" && len(outcome.PRFeedbackAssessments) > 0 {
		return fmt.Errorf("PR feedback assessments require the implementation role and a bounded comment batch")
	}
	seenFeedback := map[string]bool{}
	for _, assessment := range outcome.PRFeedbackAssessments {
		if seenFeedback[assessment.ID] {
			return fmt.Errorf("duplicate PR feedback assessment")
		}
		seenFeedback[assessment.ID] = true
		if err := validateFeedbackAssessment(assessment); err != nil {
			return err
		}
	}
	if err := validateDiagnosticOutcome(outcome, role, nil); err != nil {
		return err
	}
	if err := validateVerificationRequests(outcome.VerificationRequests, nil); err != nil {
		return err
	}
	if err := validateCompanionRepositoryRequests(outcome.RepositoryRequests, nil); err != nil {
		return err
	}
	if (role != "implementation" || outcome.Status != "checks_requested") && len(outcome.VerificationRequests) > 0 {
		return fmt.Errorf("additional verification requests require implementation checks_requested")
	}
	if role != "implementation" && !(role == "review" && outcome.Status == "checks_requested") && len(outcome.RepositoryRequests) > 0 {
		return fmt.Errorf("companion repository requests require the implementation role")
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
		if role == "review" {
			if len(outcome.RepositoryRequests) == 0 || len(outcome.Questions) != 0 || len(outcome.Findings) != 0 || len(outcome.Limitations) != 0 || len(outcome.VerificationRequests) != 0 || len(outcome.DiagnosticRequests) != 0 {
				return fmt.Errorf("review checks_requested requires source-only repository requests")
			}
		} else if role != "implementation" {
			return fmt.Errorf("unsupported handoff role")
		}
	case "diagnostics_requested":
		if role != "implementation" {
			return fmt.Errorf("review must use the supplied verification evidence")
		}
	case "implemented":
		if role != "implementation" || !outcome.LocalReview || len(outcome.Questions) > 0 || len(outcome.Findings) > 0 || len(outcome.Limitations) > 0 || outcome.PRTitle == "" || outcome.PRBody == "" {
			return fmt.Errorf("implementation handoff does not satisfy local review and PR metadata")
		}
	case "reviewed":
		if role != "review" || len(outcome.Questions) > 0 {
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

const outcomeSchema = `{"type":"object","additionalProperties":false,"required":["status","summary","questions","findings","local_review","limitations","pr_title","pr_body","verification_requests","repository_requests","pr_feedback_assessments","diagnostic_requests"],"properties":{"pr_feedback_assessments":{"type":"array","maxItems":100,"description":"Assess pending human PR comment revisions in any order, with individual rationale and disposition. A partial subset may accompany diagnostics, verification, companion source requests or human questions; continue the same session. Group related corrections and request configured checks once a coherent candidate is ready. Return [] when no comment is pending. Correction commits must be full original candidate IDs and may address several comments; the controller maps them to signed publication IDs.","items":{"type":"object","additionalProperties":false,"required":["id","fingerprint","disposition","reason","commits"],"properties":{"id":{"type":"string"},"fingerprint":{"type":"string","pattern":"^[a-f0-9]{64}$"},"disposition":{"type":"string","enum":["addressed","unaddressable","no_change"]},"reason":{"type":"string","maxLength":8192},"commits":{"type":"array","maxItems":32,"items":{"type":"string","pattern":"^[a-f0-9]{40}([a-f0-9]{24})?$"}}}}},"status":{"type":"string","enum":["implemented","checks_requested","diagnostics_requested","reviewed","waiting_for_human","blocked","failed"],"description":"Use implemented or reviewed only when that role is complete with no unresolved questions or limitations. Actionable review findings may remain for the implementer to repair."},"summary":{"type":"string","description":"Describe work, evidence and ordinary scope boundaries here, distinguishing controller verification from checks you ran yourself."},"questions":{"type":"array","items":{"type":"string"}},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["priority","path","line","scenario","recommendation"],"properties":{"priority":{"type":"string","enum":["P0","P1","P2","P3"]},"path":{"type":"string"},"line":{"type":"integer"},"scenario":{"type":"string"},"recommendation":{"type":"string"}}}},"local_review":{"type":"boolean"},"limitations":{"type":"array","description":"Only unresolved obstacles that prevent completing this role. Normal scope or isolation boundaries already covered by supplied verification evidence belong in summary. Return an empty array when complete; never omit a genuine missing requirement, tool, required review or verification gap.","items":{"type":"string"}},"pr_title":{"type":"string"},"pr_body":{"type":"string"},"repository_requests":{"type":"array","maxItems":8,"description":"Request private companion source through the controller using checks_requested; same owner as the selected repository, read-only inspection, default-branch checkout with history. blocked and failed stop work even with requests. Return [] when unused.","items":{"type":"object","additionalProperties":false,"required":["id","repository","purpose"],"properties":{"id":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"repository":{"type":"string","pattern":"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$"},"purpose":{"type":"string","maxLength":1024}}}},"verification_requests":{"type":"array","maxItems":16,"description":"Machine work for the controller, not human questions. Return [] when unused. Generic commands must pass. A baseline runs the same commands against candidate then a fresh candidate copy with only named original production blobs restored from captured StartingSHA or SourceSHA; final command must fail with the expected code and literal markers. Example: id regression, purpose prove new test catches old behavior, commands [[\"go\",\"test\",\"./internal/example\"]], baseline revision captured source SHA, paths [\"internal/example/example.go\"], expected_exit_code 1, failure_contains [\"TestRegression\"]. For a source correction artifact, use source_patch with explicit committed production paths and baseline:null. Commands run on a fresh source-only copy; a bounded private diff returns for review and commit. Use source_patch:null for ordinary requests.","items":{"type":"object","additionalProperties":false,"required":["id","purpose","commands","baseline","source_patch"],"properties":{"id":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"purpose":{"type":"string","maxLength":1024},"commands":{"type":"array","items":{"type":"array","items":{"type":"string"}}},"baseline":{"anyOf":[{"type":"object","additionalProperties":false,"required":["revision","paths","expected_exit_code","failure_contains"],"properties":{"revision":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"expected_exit_code":{"type":"integer","minimum":1,"maximum":255},"failure_contains":{"type":"array","items":{"type":"string"}}}},{"type":"null"}]},"source_patch":{"anyOf":[{"type":"object","additionalProperties":false,"required":["paths"],"properties":{"paths":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"string","maxLength":240}}}},{"type":"null"}]}}}},"diagnostic_requests":{"type":"array","maxItems":16,"description":"Development diagnostics only: submit a batch of related argv checks with diagnostics_requested. Results return to this implementation session without becoming required publication checks. No source_patch. Use checks_requested for the full configured suite and retained required verification gates.","items":{"type":"object","additionalProperties":false,"required":["id","purpose","commands","baseline","source_patch"],"properties":{"id":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"purpose":{"type":"string","maxLength":1024},"commands":{"type":"array","items":{"type":"array","items":{"type":"string"}}},"baseline":{"anyOf":[{"type":"object","additionalProperties":false,"required":["revision","paths","expected_exit_code","failure_contains"],"properties":{"revision":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"expected_exit_code":{"type":"integer","minimum":1,"maximum":255},"failure_contains":{"type":"array","items":{"type":"string"}}}},{"type":"null"}]},"source_patch":{"type":"null"}}}}}}`

func withoutLiveDiagnosticInstructions(value string) string {
	lines := strings.Split(value, "\n")
	retained := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, "For narrow checks during this running turn,") {
			retained = append(retained, line)
		}
	}
	return strings.Join(retained, "\n") + "\nLive diagnostics are unavailable for this turn. Use diagnostics_requested with diagnostic_requests for controller diagnostics; the original native session will resume with the results. Full configured checks and focused self-review remain required.\n"
}
