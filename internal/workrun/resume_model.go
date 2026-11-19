package workrun

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/providerauth"
)

// ResumeModelSelection is a private audit of an explicit same-session selection.
type ResumeModelSelection struct {
	At                    time.Time `json:"at"`
	Checkpoint            time.Time `json:"checkpoint"`
	SessionID             string    `json:"session_id"`
	AfterAttempt          int       `json:"after_attempt"`
	FirstAttempt          int       `json:"first_attempt,omitempty"`
	Previous              Model     `json:"previous"`
	PreviousReportedModel string    `json:"previous_reported_model,omitempty"`
	Selected              Model     `json:"selected"`
}

// EffectiveImplementationModel is the durable requested configuration; native
// reporting remains separate and cannot prove an effort was actually honored.
func EffectiveImplementationModel(j Journal) Model {
	if len(j.ResumeModelSelections) != 0 {
		return j.ResumeModelSelections[len(j.ResumeModelSelections)-1].Selected
	}
	return j.Plan.Roles.Implementation
}

func validateResumeModels(j *Journal) error {
	if len(j.ResumeModelSelections) > 20 {
		return fmt.Errorf("too many resume model selections")
	}
	prior := j.Plan.Roles.Implementation
	lastAttempt := 0
	for i, s := range j.ResumeModelSelections {
		if s.At.IsZero() || s.Checkpoint.IsZero() || s.At.Before(s.Checkpoint) || s.AfterAttempt < 1 || s.AfterAttempt > j.Attempt || s.AfterAttempt < lastAttempt || s.FirstAttempt < 0 || s.FirstAttempt > j.Attempt || s.FirstAttempt != 0 && s.FirstAttempt <= s.AfterAttempt || i < len(j.ResumeModelSelections)-1 && s.FirstAttempt == 0 || s.SessionID != j.SessionID || !sessionIdentifier.MatchString(s.SessionID) || s.Previous != prior || (s.Selected.Provider != "codex" && s.Selected.Provider != "claude") || s.Previous.Provider != s.Selected.Provider || ValidateModel(s.Selected) != nil || providerauth.ValidateNativeSpeed(s.Selected.Provider, s.Selected.Name, j.Plan.Roles.ImplementationSpeed) != nil || len(s.PreviousReportedModel) > 256 || !utf8.ValidString(s.PreviousReportedModel) {
			return fmt.Errorf("invalid resume model selection audit")
		}
		prior = s.Selected
		if s.FirstAttempt > 0 {
			lastAttempt = s.FirstAttempt
		} else {
			lastAttempt = s.AfterAttempt
		}
	}
	return nil
}

func ValidateResumeModel(j Journal, selected Model) error {
	if j.State == "waiting_for_human" && j.PendingRole == "implementation" && len(j.Outcome.Questions) > 0 && j.SessionID != "" && sessionIdentifier.MatchString(j.SessionID) && j.Attempt > 0 && !j.UpdatedAt.IsZero() && j.PendingInputs == nil {
		// Selection does not answer or clear the question; the ordinary answer flow remains required.
	} else if j.State == "blocked" && j.ResumeState == "implementing" && j.StopReason == "run was cancelled or timed out; checkpoint retained" && j.SessionID != "" && sessionIdentifier.MatchString(j.SessionID) && j.Attempt > 0 && !j.UpdatedAt.IsZero() && j.PendingInputs == nil && (j.PendingRole == "" || j.PendingRole == "implementation") && len(j.Outcome.Questions) == 0 && j.Outcome.Status != "blocked" && j.Outcome.Status != "failed" && j.Outcome.Status != "waiting_for_human" {
		// A controller cancellation retains the same implementation boundary.
	} else if err := validateCancelledImplementation(j); err != nil {
		return err
	}
	if len(j.ResumeModelSelections) >= 20 || len(j.ResumeModelSelections) > 0 && j.ResumeModelSelections[len(j.ResumeModelSelections)-1].FirstAttempt == 0 {
		return fmt.Errorf("the previous model selection has not reached an implementation attempt")
	}
	if (selected.Provider != "codex" && selected.Provider != "claude") || EffectiveImplementationModel(j).Provider != selected.Provider {
		return fmt.Errorf("resume model selection requires the existing Codex or Claude implementation provider")
	}
	if err := ValidateModel(selected); err != nil {
		return err
	}
	if err := providerauth.ValidateNativeSpeed(selected.Provider, selected.Name, j.Plan.Roles.ImplementationSpeed); err != nil {
		return err
	}
	if selected == EffectiveImplementationModel(j) {
		return fmt.Errorf("selected model and effort already in effect")
	}
	return nil
}

func RecordResumeModel(dir string, checkpoint time.Time, selected Model) error {
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
		return fmt.Errorf("run checkpoint changed; preview model selection again")
	}
	if err := ValidateResumeModel(j, selected); err != nil {
		return err
	}
	j.ResumeModelSelections = append(j.ResumeModelSelections, ResumeModelSelection{At: time.Now().UTC(), Checkpoint: j.UpdatedAt, SessionID: j.SessionID, AfterAttempt: j.Attempt, Previous: EffectiveImplementationModel(j), PreviousReportedModel: j.ReportedModel, Selected: selected})
	j.ReportedModel = ""
	return Save(dir, &j)
}
