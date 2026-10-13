package workrun

import (
	"fmt"
	"time"
)

const defaultNativeTurnLimit = 24
const implementationTurnBound = "implementation reached the configured session-turn bound"
const reviewTurnBound = "independent review reached the configured session-turn bound"

// SessionTurnExtension records a single explicit continuation without resetting attempts.
type SessionTurnExtension struct {
	GrantedAt         time.Time `json:"granted_at"`
	GrantedAttempt    int       `json:"granted_attempt"`
	OriginalLimit     int       `json:"original_limit"`
	Additional        int       `json:"additional"`
	OriginalSessionID string    `json:"original_session_id"`
}

func effectiveNativeTurnLimit(j *Journal) int {
	if j.SessionTurnExtension != nil {
		return defaultNativeTurnLimit + j.SessionTurnExtension.Additional
	}
	return defaultNativeTurnLimit
}

func validateSessionTurnExtension(j *Journal) error {
	e := j.SessionTurnExtension
	if e == nil {
		return nil
	}
	if e.GrantedAt.IsZero() || e.GrantedAttempt != defaultNativeTurnLimit || e.OriginalLimit != defaultNativeTurnLimit || e.Additional < 1 || e.Additional > defaultNativeTurnLimit || e.OriginalSessionID == "" || e.OriginalSessionID != j.SessionID || j.Attempt < e.GrantedAttempt || !runLimitsOverridden(j) && j.Attempt > effectiveNativeTurnLimit(j) {
		return fmt.Errorf("invalid persisted session-turn extension")
	}
	return nil
}

// ValidateSessionTurnExtension admits only the original native-capacity hold.
// Input integrity is checked again by the controller before persisting the grant.
func ValidateSessionTurnExtension(j Journal, additional int) error {
	validStage := j.StopReason == implementationTurnBound && (j.ResumeState == "implementing" || j.ResumeState == "repairing" || j.ResumeState == "ci") || j.StopReason == reviewTurnBound && (j.ResumeState == "reviewing" || j.ResumeState == "awaiting_reviewer")
	if additional < 1 || additional > defaultNativeTurnLimit || j.SessionTurnExtension != nil || j.State != "blocked" || !validStage || j.Attempt != defaultNativeTurnLimit || j.SessionID == "" || j.PendingInputs != nil || j.CandidatePhasePending {
		return fmt.Errorf("session-turn extension requires an unextended native-turn hold at attempt 24 in the original session (additional turns 1-24)")
	}
	return nil
}
