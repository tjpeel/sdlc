package workrun

import (
	"fmt"
	"strings"
	"time"
)

const retainedVerificationBound = "outstanding verification request bound exceeded"
const verificationFeedbackBound = "verification evidence was already supplied for this unchanged candidate; submit a new structured verification request or complete local review before resuming"

// RunLimitOverride records explicit permission to continue past controller numeric
// ceilings in the original run. It never changes provider or evidence gates.
type RunLimitOverride struct {
	GrantedAt           time.Time `json:"granted_at"`
	CheckpointUpdatedAt time.Time `json:"checkpoint_updated_at"`
	GrantedAttempt      int       `json:"granted_attempt"`
	GrantedRounds       int       `json:"granted_rounds"`
	OriginalSessionID   string    `json:"original_session_id"`
	LimitKind           string    `json:"limit_kind"`
	// OriginalLimit is zero when the prior configured ceiling was not recorded.
	OriginalLimit                       int  `json:"original_limit"`
	PendingVerificationReplay           bool `json:"pending_verification_replay,omitempty"`
	VerificationReplayFeedbackProcessed bool `json:"verification_replay_feedback_processed,omitempty"`
}

func runLimitsOverridden(j *Journal) bool { return j.RunLimitOverride != nil }

func validatePersistedRunLimitOverride(j *Journal) error {
	o := j.RunLimitOverride
	if j.Attempt < 0 || j.Rounds < 0 || j.RepairWindowStart < 0 || j.RepairWindowStart > j.Rounds || j.CheckAttempt < 0 || j.VerificationFeedbackCount < 0 || j.PRMetadataCorrections < 0 {
		return fmt.Errorf("invalid run limit accounting")
	}
	if o == nil {
		return nil
	}
	if o.GrantedAt.IsZero() || o.CheckpointUpdatedAt.IsZero() || o.GrantedAt.Before(o.CheckpointUpdatedAt) || o.GrantedAttempt < 0 || o.GrantedAttempt > j.Attempt || o.GrantedRounds < 0 || o.GrantedRounds > j.Rounds || o.OriginalSessionID == "" || o.OriginalSessionID != j.SessionID || o.OriginalLimit < 0 {
		return fmt.Errorf("invalid persisted run limit override")
	}
	switch o.LimitKind {
	case "native_turns":
		if o.OriginalLimit != effectiveNativeTurnLimit(j) || o.GrantedAttempt < o.OriginalLimit {
			return fmt.Errorf("invalid native-turn override accounting")
		}
	case "configured_repairs":
		if o.OriginalLimit != 0 {
			return fmt.Errorf("invalid configured repair override accounting")
		}
	case "ci_repairs":
		if o.OriginalLimit != 0 && o.OriginalLimit != 3 {
			return fmt.Errorf("invalid CI override accounting")
		}
	case "review_handoffs":
		if o.OriginalLimit < 1 || o.OriginalLimit > 3 || j.ReviewRepairBudget == nil || o.OriginalLimit != j.ReviewRepairBudget.Limit {
			return fmt.Errorf("invalid review override accounting")
		}
	case "verification_feedback":
		if o.OriginalLimit != 2 {
			return fmt.Errorf("invalid verification feedback override accounting")
		}
	case "pr_metadata":
		if o.OriginalLimit != 2 {
			return fmt.Errorf("invalid metadata override accounting")
		}
	case "verification_requests":
		if o.OriginalLimit != 16 {
			return fmt.Errorf("invalid verification request override accounting")
		}
	default:
		return fmt.Errorf("unknown run limit override kind")
	}
	if o.VerificationReplayFeedbackProcessed && !o.PendingVerificationReplay {
		return fmt.Errorf("invalid verification replay progress")
	}
	if o.PendingVerificationReplay && (o.LimitKind != "verification_requests" || j.Outcome.Status != "checks_requested") {
		return fmt.Errorf("invalid pending verification replay")
	}
	return nil
}

func admittedRunLimit(j Journal) (string, int, error) {
	invalid := fmt.Errorf("run limit override requires a supported saved numeric-limit hold in the original session")
	if err := validatePersistedRunLimitOverride(&j); err != nil {
		return "", 0, err
	}
	if j.State != "blocked" || j.SessionID == "" || j.UpdatedAt.IsZero() || j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || j.Outcome.Status == "blocked" || j.Outcome.Status == "failed" || j.Outcome.Status == "waiting_for_human" {
		return "", 0, invalid
	}
	if strings.HasPrefix(j.StopReason, "PR metadata still violates the frozen publication contract: ") && j.ResumeState == "checking" && j.PRMetadataCorrections > 2 && j.Evidence.Passed && j.Outcome.Status == "implemented" && ValidatePRMetadata(j.Plan) != nil {
		return "pr_metadata", 2, nil
	}
	switch j.StopReason {
	case implementationTurnBound, reviewTurnBound:
		valid := j.StopReason == implementationTurnBound && (j.ResumeState == "implementing" || j.ResumeState == "repairing" || j.ResumeState == "ci") || j.StopReason == reviewTurnBound && (j.ResumeState == "reviewing" || j.ResumeState == "awaiting_reviewer")
		if valid && j.Attempt >= effectiveNativeTurnLimit(&j) {
			return "native_turns", effectiveNativeTurnLimit(&j), nil
		}
	case configuredCheckRepairBound:
		if j.ResumeState == "checking" && j.Rounds > j.RepairWindowStart && !j.Evidence.Passed && (j.Evidence.CompletedFailureKey != "" || formatterRepairFailure(j)) {
			return "configured_repairs", 0, nil
		}
	case ciRepairBound:
		b := j.CIRepairBudget
		if j.ResumeState == "ci" && b != nil && b.Used > 0 && j.CI.Status == "failed" && j.Publication.Number > 0 && objectID.MatchString(j.Publication.HeadSHA) && b.LastGrantedHead != j.Publication.HeadSHA {
			limit := 3
			if b.Used < 3 {
				limit = 0
			}
			return "ci_repairs", limit, nil
		}
	case reviewRepairBound:
		b := j.ReviewRepairBudget
		if j.ResumeState == "reviewing" && b != nil && b.Used >= b.Limit && j.Outcome.Status == "reviewed" && len(j.Outcome.Findings) > 0 && j.CI.Status == "passed" && j.Publication.Number > 0 && objectID.MatchString(j.Publication.HeadSHA) && b.LastGrantedHead != j.Publication.HeadSHA {
			return "review_handoffs", b.Limit, nil
		}
	case verificationFeedbackBound:
		if j.ResumeState == "checking" && j.VerificationFeedbackCount > 2 && j.VerificationFeedbackKey != "" && j.Evidence.Passed {
			return "verification_feedback", 2, nil
		}
	case retainedVerificationBound:
		if (j.ResumeState == "implementing" || j.ResumeState == "repairing") && j.Outcome.Status == "checks_requested" && j.Attempt > 0 && len(j.Verification) == 16 {
			copy := j
			copy.RunLimitOverride = nil
			if err := mergeVerification(&copy, j.Outcome.VerificationRequests); err != nil && err.Error() == retainedVerificationBound {
				return "verification_requests", 16, nil
			}
		}
	}
	return "", 0, invalid
}

// ValidateRunLimitOverride admits only actual controller numeric holds. Callers
// must also recheck input integrity before saving the audit record.
func ValidateRunLimitOverride(j Journal) error {
	if err := validateRetainedVerification(&j); err != nil {
		return err
	}
	if err := validateSourcePatchJournal(&j); err != nil {
		return err
	}
	if err := validateCommandEvidence(&j); err != nil {
		return err
	}
	if err := validateSessionTurnExtension(&j); err != nil {
		return err
	}
	if err := validateCIJournal(&j); err != nil {
		return err
	}
	if err := validateReviewJournal(&j); err != nil {
		return err
	}
	_, _, err := admittedRunLimit(j)
	return err
}

func validateRetainedVerification(j *Journal) error {
	requests := make([]VerificationRequest, 0, len(j.Verification))
	for _, result := range j.Verification {
		requests = append(requests, result.Request)
		if len(result.Key) > 64 || len(result.Log) > 256 || len(result.Diagnostic) > 4096 {
			return fmt.Errorf("invalid additional verification evidence")
		}
	}
	limit := 16
	if runLimitsOverridden(j) {
		limit = 0
	}
	return validateVerificationRequestSet(requests, &j.Plan, limit)
}

func formatterRepairFailure(j Journal) bool {
	if validateSourcePatchJournal(&j) != nil {
		return false
	}
	for _, result := range j.FormatterPreparation {
		if !result.Passed && result.Key != "" && (result.SourcePatch != nil && result.SourcePatch.Diff != "" || result.SourcePatch == nil && result.Diagnostic != "") && result.Key == verificationKey(&j, result.Request, result.Head, result.Tree) {
			return true
		}
	}
	return false
}
