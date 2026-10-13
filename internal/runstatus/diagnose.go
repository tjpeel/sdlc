package runstatus

import (
	"fmt"
	"github.com/tjpeel/sdlc/internal/workrun"
	"regexp"
	"time"
)

type NextAction struct {
	Category    string `json:"category"`
	Instruction string `json:"instruction"`
	Command     string `json:"command,omitempty"`
}

type DiagnosticPublication struct {
	Number int    `json:"number"`
	Head   string `json:"head,omitempty"`
	Base   string `json:"base,omitempty"`
}

type DiagnosticEvidence struct {
	Passed             bool   `json:"passed"`
	Head               string `json:"head,omitempty"`
	Tree               string `json:"tree,omitempty"`
	PendingAdditional  int    `json:"pending_additional"`
	PendingPreparation int    `json:"pending_preparation"`
}

type DiagnosticFeedback struct {
	Active             bool `json:"active"`
	Pending            int  `json:"pending"`
	Unresolved         int  `json:"unresolved"`
	OutstandingReplies int  `json:"outstanding_replies"`
}

// Diagnostic is an allowlisted saved-state projection. It is not a backup and
// contains no free-form stop text, commands, comments, account or source data.
type Diagnostic struct {
	Version               int                            `json:"version"`
	RunID                 string                         `json:"run_id"`
	CheckpointUpdatedAt   time.Time                      `json:"checkpoint_updated_at"`
	State                 string                         `json:"state"`
	ResumeState           string                         `json:"resume_state"`
	Live                  bool                           `json:"controller_active"`
	Evidence              DiagnosticEvidence             `json:"saved_evidence"`
	Publication           DiagnosticPublication          `json:"saved_publication"`
	CI                    string                         `json:"saved_ci"`
	Review                string                         `json:"saved_review"`
	ReviewFindings        int                            `json:"review_findings"`
	ReviewQuestions       int                            `json:"review_questions"`
	ReviewLimitations     int                            `json:"review_limitations"`
	Attempts              int                            `json:"provider_attempts"`
	Rounds                int                            `json:"repair_rounds"`
	CheckAttempts         int                            `json:"check_attempts"`
	Override              *RunLimitOverrideSummary       `json:"run_limit_override,omitempty"`
	PendingInputs         bool                           `json:"pending_inputs"`
	Feedback              DiagnosticFeedback             `json:"feedback"`
	Companions            int                            `json:"companion_sources"`
	PendingCompanions     int                            `json:"pending_companion_sources"`
	PublicationDiagnostic *workrun.PublicationDiagnostic `json:"saved_publication_diagnostic,omitempty"`
	NextAction            NextAction                     `json:"next_action"`
}

var diagnosticRunID = regexp.MustCompile(`^[0-9a-f]{24}$`)
var diagnosticObjectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func diagnosticHash(value string) string {
	if diagnosticObjectID.MatchString(value) {
		return value
	}
	return ""
}
func diagnosticState(value string) string {
	switch value {
	case "prepared", "implementing", "repairing", "checking", "publishing", "ci", "awaiting_reviewer", "reviewing", "pr_feedback", "ready", "blocked", "failed", "waiting_for_human":
		return value
	}
	return "unknown"
}
func diagnosticCI(value string) string {
	switch value {
	case "passed", "failed", "pending":
		return value
	}
	return "unknown"
}
func diagnosticReview(j workrun.Journal) string {
	if j.Outcome.Status == "reviewed" {
		return "reviewed"
	}
	if j.State == "reviewing" || j.State == "awaiting_reviewer" || j.ResumeState == "reviewing" || j.ResumeState == "awaiting_reviewer" {
		return "pending"
	}
	return "not_completed"
}

// NextRunAction gives a fixed, non-executing recovery instruction from saved state.
func NextRunAction(j workrun.Journal, live bool) NextAction {
	id := j.ID
	if !diagnosticRunID.MatchString(id) {
		return NextAction{Category: "inspect", Instruction: "Inspect the saved checkpoint before attempting ordinary resume."}
	}
	dump := "sdlc diagnose --run " + id + " --json"
	if live {
		return NextAction{Category: "observe", Instruction: "The controller is active; inspect its saved state without starting another controller.", Command: dump}
	}
	if j.PendingInputs != nil {
		return NextAction{Category: "input_recovery", Instruction: "Finish the immutable saved input transaction before resuming.", Command: "sdlc inputs --run " + id}
	}
	if j.State == "waiting_for_human" && len(j.Outcome.Questions) > 0 {
		return NextAction{Category: "human_answer", Instruction: "Answer the recorded question before resuming.", Command: "sdlc answer --run " + id}
	}
	if j.State == "blocked" && workrun.ValidateRunLimitOverride(j) == nil {
		if j.RunLimitOverride != nil {
			return NextAction{Category: "ordinary_resume", Instruction: "The saved numeric permission remains in force; use ordinary resume.", Command: "sdlc resume --run " + id}
		}
		return NextAction{Category: "numeric_override", Instruction: "Explicitly grant durable permission to continue past controller numeric limits.", Command: "sdlc resume --run " + id + " --override-run-limits"}
	}
	if workrun.CanReplaySavedPRFeedback(j) {
		return NextAction{Category: "feedback_recovery", Instruction: "Resume to validate and replay the saved feedback outcome before another native turn.", Command: "sdlc resume --run " + id}
	}
	if j.State == "ready" {
		return NextAction{Category: "human_review", Instruction: "Review the published change; request PR feedback after new human comments.", Command: "sdlc resume --run " + id + " --pr-feedback"}
	}
	return NextAction{Category: "inspect", Instruction: "Inspect this saved-state dump and any private diagnostic, resolve the hold, then use ordinary resume.", Command: dump}
}

func DiagnosticSnapshot(v View) (Diagnostic, error) {
	if !v.Available || v.Preparation || v.Journal == nil || !diagnosticRunID.MatchString(v.ID) || v.Journal.ID != v.ID {
		return Diagnostic{}, fmt.Errorf("saved run checkpoint is unavailable")
	}
	j := *v.Journal
	evidence := DiagnosticEvidence{Passed: j.Evidence.Passed, Head: diagnosticHash(j.Evidence.Head), Tree: diagnosticHash(j.Evidence.Tree)}
	for _, r := range j.Verification {
		if !r.Passed || r.Key == "" || r.Head != j.Evidence.Head || r.Tree != j.Evidence.Tree {
			evidence.PendingAdditional++
		}
	}
	for _, r := range j.FormatterPreparation {
		if !r.Passed || r.Key == "" || r.Head != j.Evidence.Head || r.Tree != j.Evidence.Tree {
			evidence.PendingPreparation++
		}
	}
	feedback := workrun.PRFeedbackStatus(j)
	result := Diagnostic{Version: 1, RunID: j.ID, CheckpointUpdatedAt: j.UpdatedAt, State: diagnosticState(j.State), ResumeState: diagnosticState(j.ResumeState), Live: v.Live, Evidence: evidence, Publication: DiagnosticPublication{Number: j.Publication.Number, Head: diagnosticHash(j.Publication.HeadSHA), Base: diagnosticHash(j.Publication.BaseSHA)}, CI: diagnosticCI(j.CI.Status), Review: diagnosticReview(j), ReviewFindings: len(j.Outcome.Findings), ReviewQuestions: len(j.Outcome.Questions), ReviewLimitations: len(j.Outcome.Limitations), Attempts: j.Attempt, Rounds: j.Rounds, CheckAttempts: j.CheckAttempt, Override: RunLimitOverrideStatus(j.RunLimitOverride), PendingInputs: j.PendingInputs != nil, Feedback: DiagnosticFeedback{Active: j.PRFeedback != nil && j.PRFeedback.Active, Pending: feedback.Pending, Unresolved: feedback.Unresolved, OutstandingReplies: feedback.OutstandingReplies}, Companions: len(j.CompanionRepositories), NextAction: NextRunAction(j, v.Live)}
	for _, request := range j.Outcome.RepositoryRequests {
		staged := false
		for _, source := range j.CompanionRepositories {
			staged = staged || source.Request == request
		}
		if !staged {
			result.PendingCompanions++
		}
	}
	if j.ResumeState == "" {
		result.ResumeState = ""
	}
	if d, err := workrun.LoadPublicationDiagnostic(v.Directory); err == nil {
		result.PublicationDiagnostic = &d
	}
	return result, nil
}
