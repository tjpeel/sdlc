package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

const invalidImplementationHandoff = "implementation handoff does not satisfy local review and PR metadata"

type ImplementationFollowup struct {
	FirstAcceptedAttempt int         `json:"first_accepted_attempt,omitempty"`
	AfterAttempt         int         `json:"after_attempt"`
	At                   time.Time   `json:"at"`
	Checkpoint           time.Time   `json:"checkpoint"`
	Candidate            Revision    `json:"candidate"`
	Publication          Publication `json:"publication"`
	Text                 string      `json:"text"`
	HumanAnswer          string      `json:"human_answer"`
}

// answeredImplementationQuestion identifies the saved answer, rather than treating
// the stale provider outcome's question as a new request for human input.
func answeredImplementationQuestion(j Journal) string {
	if j.Outcome.Status != "waiting_for_human" || len(j.Outcome.Questions) == 0 {
		return ""
	}
	prefix := "Recorded human questions:\n" + strings.Join(j.Outcome.Questions, "\n") + "\nHuman answer:\n"
	if !strings.HasPrefix(j.Feedback, prefix) {
		return ""
	}
	answer := strings.TrimPrefix(j.Feedback, prefix)
	for _, marker := range []string{"\nIgnored PR comment IDs (", "\nController-staged companion source metadata"} {
		if n := strings.Index(answer, marker); n >= 0 {
			answer = answer[:n]
		}
	}
	answer = strings.TrimSpace(answer)
	if validateTakeoverReason(answer) != nil {
		return ""
	}
	return answer
}

func validateImplementationFollowups(j *Journal) error {
	if len(j.ImplementationFollowups) > 20 {
		return fmt.Errorf("too many implementation follow-ups")
	}
	for _, f := range j.ImplementationFollowups {
		if f.AfterAttempt < 1 || f.AfterAttempt > j.Attempt || f.FirstAcceptedAttempt < 0 || f.FirstAcceptedAttempt > j.Attempt || (f.FirstAcceptedAttempt != 0 && f.FirstAcceptedAttempt <= f.AfterAttempt) || f.At.IsZero() || f.Checkpoint.IsZero() || f.At.Before(f.Checkpoint) || !f.Candidate.Clean || !objectID.MatchString(f.Candidate.Head) || !objectID.MatchString(f.Candidate.Tree) || f.Publication.Number < 1 || f.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, f.Publication.Number) || !objectID.MatchString(f.Publication.HeadSHA) || !objectID.MatchString(f.Publication.BaseSHA) || validateTakeoverReason(f.Text) != nil || validateTakeoverReason(f.HumanAnswer) != nil {
			return fmt.Errorf("invalid implementation follow-up audit")
		}
	}
	return nil
}

func ValidateImplementationFollowup(j Journal, text string) error {
	if err := validateTakeoverReason(text); err != nil {
		return err
	}
	if strings.TrimSpace(text) != text {
		return fmt.Errorf("follow-up text must not have surrounding whitespace")
	}
	if len(j.ImplementationFollowups) >= 20 || j.State != "failed" || j.ResumeState != "implementing" || j.StopReason != invalidImplementationHandoff || j.PendingRole != "implementation" || j.SessionID == "" || !sessionIdentifier.MatchString(j.SessionID) || answeredImplementationQuestion(j) == "" || !j.Evidence.Passed || !objectID.MatchString(j.Evidence.Head) || !objectID.MatchString(j.Evidence.Tree) || !verificationCurrent(&j, j.Evidence.Head, j.Evidence.Tree) || j.Publication.Number < 1 || j.PendingInputs != nil {
		return fmt.Errorf("follow-up acceptance requires the rejected handoff, a recorded human answer and current passing candidate evidence")
	}
	if len(j.ImplementationFollowups) > 0 {
		return fmt.Errorf("a follow-up acceptance is already recorded")
	}
	return nil
}

// ValidateRejectedImplementationFollowup verifies the exact final native handoff;
// the retained events remain evidence and are never replayed as a success.
func ValidateRejectedImplementationFollowup(dir string, j Journal, text string) error {
	if err := ValidateImplementationFollowup(j, text); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", j.Attempt))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64*1024*1024 {
		return fmt.Errorf("retained rejected handoff events are unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("retained handoff events changed while opening")
	}
	writer := &eventWriter{provider: EffectiveImplementationModel(j).Provider}
	if _, err := io.Copy(writer, io.LimitReader(file, 64*1024*1024+1)); err != nil {
		return fmt.Errorf("retained handoff events are invalid")
	}
	if err := writer.finish("implementation"); err != nil {
		return fmt.Errorf("retained handoff is incomplete or invalid")
	}
	o := writer.result.Outcome
	if writer.result.SessionID != j.SessionID || o.Status != "implemented" || len(o.Limitations) != 1 || o.Limitations[0] != text || writer.result.ReportedModel != "" && writer.result.ReportedModel != EffectiveImplementationModel(j).Name {
		return fmt.Errorf("follow-up does not match the retained rejected implementation handoff")
	}
	return nil
}

func RecordImplementationFollowup(dir string, checkpoint time.Time, text string) error {
	path := filepath.Join(dir, "run.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("run controller lock is unsafe")
	}
	lock, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := Load(dir)
	if err != nil {
		return err
	}
	if !j.UpdatedAt.Equal(checkpoint) {
		return fmt.Errorf("run checkpoint changed; preview follow-up again")
	}
	if err := ValidateRejectedImplementationFollowup(dir, j, text); err != nil {
		return err
	}
	j.ImplementationFollowups = append(j.ImplementationFollowups, ImplementationFollowup{AfterAttempt: j.Attempt, At: time.Now().UTC(), Checkpoint: j.UpdatedAt, Candidate: Revision{Head: j.Evidence.Head, Tree: j.Evidence.Tree, Clean: true}, Publication: j.Publication, Text: text, HumanAnswer: answeredImplementationQuestion(j)})
	return Save(dir, &j)
}

func implementationFollowupContext(j Journal, role string) string {
	if len(j.ImplementationFollowups) == 0 {
		return ""
	}
	data, _ := json.Marshal(j.ImplementationFollowups)
	if role == "review" {
		return "\nThe following human-approved implementation follow-up is outstanding, not completed. Review independently and consider its rollout implication; this is not review acceptance: " + string(data) + "\n"
	}
	return "\nThe human explicitly accepted the following outstanding implementation follow-up on the recorded candidate. Preserve its exact text in the PR body and report it in limitations; do not claim it is completed. Other limitations still block implementation. Existing verification, feedback and independent review requirements remain: " + string(data) + "\n"
}

func (runner Runner) validateImplementationFollowupOutcome(ctx context.Context, j *Journal, o Outcome) error {
	if o.Status != "implemented" {
		return nil
	}
	if len(j.ImplementationFollowups) == 0 {
		if len(o.Limitations) != 0 {
			return fmt.Errorf("implementation limitations require explicit follow-up acceptance")
		}
		return nil
	}
	f := j.ImplementationFollowups[len(j.ImplementationFollowups)-1]
	revision, err := runner.Repository.Inspect(ctx, j.Workspace)
	if err != nil {
		return err
	}
	if !revision.Clean || !j.Evidence.Passed || j.Evidence.Head != revision.Head || j.Evidence.Tree != revision.Tree || !verificationCurrent(j, revision.Head, revision.Tree) || j.Publication.Number != f.Publication.Number || j.Publication.URL != f.Publication.URL || (f.FirstAcceptedAttempt == 0 && (revision != f.Candidate || j.Publication != f.Publication)) {
		return fmt.Errorf("accepted follow-up requires current checked candidate and the original PR")
	}
	if len(o.Limitations) != 1 || o.Limitations[0] != f.Text || !strings.Contains(o.PRBody, f.Text) {
		return fmt.Errorf("accepted follow-up must be the sole implementation limitation and appear verbatim in the PR body")
	}
	return nil
}
