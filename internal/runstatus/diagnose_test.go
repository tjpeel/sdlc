package runstatus

import (
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/workrun"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticSnapshotOmitsFreeFormAndAccountData(t *testing.T) {
	const excluded = "excluded-private-content"
	id := strings.Repeat("a", 24)
	j := workrun.Journal{ID: id, State: "blocked", ResumeState: "publishing", StopReason: excluded, SessionID: excluded, Instructions: excluded, Feedback: excluded, Plan: workrun.Plan{Root: excluded, Ticket: excluded, Checks: [][]string{{excluded}}, Inputs: []workrun.Input{{Path: excluded, SHA256: excluded}}, GitHubProfile: excluded, SigningProfile: excluded, PublicationIdentity: &workrun.PublicationIdentity{SSHPublicKey: excluded, GitHubVolume: excluded}}, Outcome: workrun.Outcome{Summary: excluded, Questions: []string{excluded}, Limitations: []string{excluded}}, CI: workrun.CIResult{Status: excluded, Details: excluded}, Evidence: workrun.CheckEvidence{Head: excluded, Tree: strings.Repeat("b", 40), Output: []workrun.CommandOutput{{CombinedOutput: excluded}}, Commands: [][]string{{excluded}}, Log: excluded}, Publication: workrun.Publication{URL: "https://" + excluded, Number: 4, HeadSHA: strings.Repeat("c", 40)}, Attempt: 25, Rounds: 17, CheckAttempt: 40}
	j.Diagnostics = []workrun.VerificationResult{{Request: workrun.VerificationRequest{ID: excluded, Purpose: excluded, Commands: [][]string{{excluded}}}, Output: []workrun.CommandOutput{{CombinedOutput: excluded}}}}
	snapshot, err := DiagnosticSnapshot(View{Available: true, ID: id, Directory: t.TempDir(), Journal: &j})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), excluded) || strings.Contains(string(data), "https://") || strings.Contains(string(data), "stop_reason") || strings.Contains(string(data), "commands") || strings.Contains(string(data), "session_id") {
		t.Fatal("diagnostic exported free-form/private state", string(data))
	}
	if snapshot.State != "blocked" || snapshot.ResumeState != "publishing" || snapshot.Attempts != 25 || snapshot.CheckAttempts != 40 || snapshot.Evidence.Tree != strings.Repeat("b", 40) || snapshot.Publication.Number != 4 || snapshot.CI != "unknown" || snapshot.NextAction.Category != "inspect" {
		t.Fatal("saved diagnostic evidence or fixed unknown action missing")
	}
}

func TestDiagnosticNextActionSelectsNumericPermissionOrHumanAnswer(t *testing.T) {
	id := strings.Repeat("b", 24)
	j := workrun.Journal{ID: id, State: "blocked", ResumeState: "implementing", StopReason: "implementation reached the configured session-turn bound", Attempt: 24, SessionID: "original-session", UpdatedAt: time.Now().UTC()}
	action := NextRunAction(j, false)
	if action.Category != "numeric_override" || action.Command != "sdlc resume --run "+id+" --override-run-limits" {
		t.Fatal("numeric hold lacked durable recovery instruction", action)
	}
	j.State = "waiting_for_human"
	j.Outcome.Questions = []string{"excluded-private-question"}
	action = NextRunAction(j, false)
	if action.Category != "human_answer" || action.Command != "sdlc answer --run "+id || strings.Contains(action.Instruction, "excluded") {
		t.Fatal("human answer action missing or exposed question")
	}
	action = NextRunAction(j, true)
	if action.Category != "observe" || strings.Contains(action.Command, "resume") {
		t.Fatal("active controller recommended duplicate resume")
	}
	j.State = "blocked"
	j.StopReason = "unknown excluded-private-hold"
	j.Outcome.Questions = nil
	action = NextRunAction(j, false)
	if action.Category != "inspect" || action.Command != "sdlc diagnose --run "+id+" --json" || strings.Contains(action.Instruction, "excluded") {
		t.Fatal("unknown hold was guessed or exposed")
	}
}
