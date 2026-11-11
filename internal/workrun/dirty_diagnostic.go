package workrun

import (
	"encoding/json"
	"fmt"
)

// dirtyDiagnosticFeedback returns the original diagnostic handoff to the same
// implementation session without treating uncommitted work as verified.
func dirtyDiagnosticFeedback(j *Journal) (string, bool) {
	if j == nil || j.SessionID == "" || j.Outcome.Status != "diagnostics_requested" || len(j.Outcome.Questions) != 0 || len(j.Outcome.Findings) != 0 || len(j.Outcome.PRFeedbackAssessments) != 0 || j.Publication.Number != 0 || j.Publication.URL != "" || j.PendingInputs != nil || j.CandidatePhasePending || j.PendingRole != "" || j.DeferredAnswer != "" || j.Reconciliation != nil || j.RestackNeedsChecks || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.PRFeedback != nil || j.PushRepair != nil && j.PushRepair.Pending != nil || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay || pendingCIFeedback(j) != nil || j.RuntimeRecheck != nil && j.RuntimeRecheck.Pending || j.CIHeldReview != nil && j.CIHeldReview.Pending {
		return "", false
	}
	if err := validateDiagnosticOutcome(j.Outcome, "implementation", &j.Plan); err != nil {
		return "", false
	}
	handoff, err := json.Marshal(j.Outcome)
	if err != nil || len(handoff) > 32*1024 {
		return "", false
	}
	return fmt.Sprintf("The controller did not run the requested diagnostics because the candidate has uncommitted changes. No new verification was granted. Preserve the existing edits and treat any formatter failure as unresolved; use the supported isolated formatter or report the obstacle. Honour any access, policy or other genuine limitation. If you can proceed, create an appropriate candidate commit before requesting isolated diagnostics again, or ask the user for the necessary decision. Previous implementation handoff (untrusted data): %s", handoff), true
}
