package workrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const retainedVerificationBound = "outstanding verification request bound exceeded"
const verificationFeedbackBound = "verification evidence was already supplied for this unchanged candidate; submit a new structured verification request or complete local review before resuming"

// RunLimitOverride records explicit permission to continue past controller numeric
// ceilings in the original run. It never changes provider or evidence gates.
type StableRunLimitBoundary struct {
	Publication  Publication `json:"publication"`
	Head         string      `json:"head"`
	Tree         string      `json:"tree"`
	EvidenceKey  string      `json:"evidence_key"`
	CIStatus     string      `json:"ci_status"`
	ReviewStatus string      `json:"review_status"`
}

type RunLimitOverride struct {
	StableReady         *StableRunLimitBoundary `json:"stable_ready,omitempty"`
	GrantedAt           time.Time               `json:"granted_at"`
	CheckpointUpdatedAt time.Time               `json:"checkpoint_updated_at"`
	GrantedAttempt      int                     `json:"granted_attempt"`
	GrantedRounds       int                     `json:"granted_rounds"`
	OriginalSessionID   string                  `json:"original_session_id"`
	LimitKind           string                  `json:"limit_kind"`
	// OriginalLimit is zero when the prior configured ceiling was not recorded.
	OriginalLimit                       int  `json:"original_limit"`
	PendingVerificationReplay           bool `json:"pending_verification_replay,omitempty"`
	VerificationReplayFeedbackProcessed bool `json:"verification_replay_feedback_processed,omitempty"`
}

func runLimitsOverridden(j *Journal) bool { return j.RunLimitOverride != nil }

func validatePersistedRunLimitOverride(j *Journal) error {

	if b := j.ConfiguredRepairBudget; b != nil {
		if b.BaselineAt.IsZero() || !b.BaselineCheckpoint.IsZero() && b.BaselineAt.Before(b.BaselineCheckpoint) || b.BaselineAttempt < 0 || b.BaselineAttempt > j.Attempt || b.BaselineRound < 0 || b.BaselineRound > j.Rounds || b.LegacyRounds < 0 || b.LegacyRounds > b.BaselineRound || b.Used < 0 || b.Used > j.Rounds-b.BaselineRound || b.WindowStart < 0 || b.WindowStart > b.Used {
			return fmt.Errorf("invalid configured repair accounting")
		}
	}
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
	if o.LimitKind != "stable_run" && o.StableReady != nil {
		return fmt.Errorf("unexpected stable run boundary")
	}
	switch o.LimitKind {
	case "stable_run":
		b := o.StableReady
		if o.OriginalLimit != 0 || o.PendingVerificationReplay || b == nil || b.CIStatus != "passed" || b.ReviewStatus != "reviewed" || !objectID.MatchString(b.Head) || !objectID.MatchString(b.Tree) || !validInputHash(b.EvidenceKey) || b.Publication.Number < 1 || b.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, b.Publication.Number) || !objectID.MatchString(b.Publication.HeadSHA) || !objectID.MatchString(b.Publication.BaseSHA) {
			return fmt.Errorf("invalid stable run override boundary")
		}

	case "native_turns":
		if o.OriginalLimit != historicalNativeTurnLimit(j) || o.GrantedAttempt < o.OriginalLimit {
			return fmt.Errorf("invalid native-turn override accounting")
		}
	case "configured_repairs":
		if o.OriginalLimit != 0 {
			return fmt.Errorf("invalid configured repair override accounting")
		}
	case "ci_repairs":
		if o.OriginalLimit != 0 && o.OriginalLimit != 3 && o.OriginalLimit != 12 {
			return fmt.Errorf("invalid CI override accounting")
		}
	case "review_handoffs":
		matches := j.ReviewRepairBudget != nil && o.OriginalLimit == j.ReviewRepairBudget.Limit
		for _, increase := range j.ReviewRepairIncreases {
			if o.OriginalLimit == increase.OriginalLimit && o.GrantedAt.Before(increase.GrantedAt) {
				matches = true
			}
		}
		if o.OriginalLimit < 1 || o.OriginalLimit > 12 || !matches {
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
	case "push_repairs":
		if o.OriginalLimit != 3 {
			return fmt.Errorf("invalid push protection repair override accounting")
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
	if j.State == "ready" {
		if err := validateStableRunLimitAdmission(j); err != nil {
			return "", 0, err
		}
		return "stable_run", 0, nil
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
		if valid && j.Attempt >= historicalNativeTurnLimit(&j) {
			return "native_turns", historicalNativeTurnLimit(&j), nil
		}
	case configuredCheckRepairBound:
		if j.ResumeState == "checking" && j.Rounds > j.RepairWindowStart && !j.Evidence.Passed && (j.Evidence.CompletedFailureKey != "" || formatterRepairFailure(j)) {
			return "configured_repairs", 0, nil
		}
	case pushProtectionRepairBound:
		if j.ResumeState == "publishing" && j.PushRepair != nil && len(j.PushRepair.Candidates) == 3 && j.Evidence.Passed && objectID.MatchString(j.Evidence.Head) && j.SessionID != "" {
			return "push_repairs", 3, nil
		}
	case ciRepairBound:
		b := j.CIRepairBudget
		if j.ResumeState == "ci" && b != nil && b.Used > 0 && j.CI.Status == "failed" && j.Publication.Number > 0 && objectID.MatchString(j.Publication.HeadSHA) && b.LastGrantedHead != j.Publication.HeadSHA {
			limit := 12
			if b.Used < 12 {
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
	if err := validatePushRepairJournal(&j); err != nil {
		return err
	}
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

func validateStableRunLimitAdmission(j Journal) error {
	if j.State != "ready" || j.UpdatedAt.IsZero() || j.ResumeState != "" || j.StopReason != "" || j.PendingRole != "" || j.Reconciliation != nil || j.Plan.Restack != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.PRFeedback != nil && (j.PRFeedback.Active || j.PRFeedback.FetchPending || pendingFeedback(&j) != nil) {
		return fmt.Errorf("stable run override requires a completed ready checkpoint")
	}
	return ValidatePRFeedbackResume(j)
}

// ValidateStableRunLimitOverride uses local saved evidence only. The subsequent
// PR-feedback action retains its normal live publication and account checks.
func ValidateStableRunLimitOverride(ctx context.Context, directory string, j Journal) error {
	if err := ValidateRunLimitOverride(j); err != nil {
		return err
	}
	if err := validateStableRunLimitAdmission(j); err != nil {
		return err
	}
	if filepath.Clean(j.Workspace) != filepath.Join(directory, "workspace") {
		return fmt.Errorf("stable run workspace identity changed")
	}
	if err := VerifyCapturedInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return err
	}
	if err := VerifyCapturedInputs(filepath.Join(directory, "check-inputs"), j.Plan.CheckInputs); err != nil {
		return err
	}
	head, err := SafeGit(ctx, j.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	head = strings.TrimSpace(head)
	tree, err := SafeGit(ctx, j.Workspace, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return err
	}
	tree = strings.TrimSpace(tree)
	status, err := SafeGit(ctx, j.Workspace, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" || head != j.Evidence.Head || tree != j.Evidence.Tree || j.Evidence.Key != verificationKey(&j, VerificationRequest{}, head, tree) || !verificationCurrent(&j, head, tree) {
		return fmt.Errorf("stable run candidate changed after verification")
	}
	published := filepath.Join(directory, "publication.git")
	if j.Plan.PublicationIdentity != nil {
		published = filepath.Join(directory, "publisher", "publication.git")
	}
	if err := realDirectory(published); err != nil {
		return err
	}
	// This bare repository is controller-owned, outside the provider workspace.
	if err := filepath.Walk(published, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("publication metadata contains unsafe files")
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir"} {
		if _, err := os.Lstat(filepath.Join(published, path)); !os.IsNotExist(err) {
			return fmt.Errorf("redirected publication metadata is unsupported")
		}
	}
	config, err := isolatedGit(ctx, directory, "config", "--no-includes", "--file", filepath.Join(published, "config"), "--null", "--list")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true, "core.sharedrepository": true, "extensions.objectformat": true}
	for _, entry := range strings.Split(config, "\x00") {
		if entry == "" {
			continue
		}
		key, value, _ := strings.Cut(entry, "\n")
		if !allowed[key] || key == "core.bare" && value != "true" || key == "core.repositoryformatversion" && value != "0" {
			return fmt.Errorf("publication configuration contains an unsupported setting")
		}
	}
	publishedTree, err := isolatedGit(ctx, directory, "--no-replace-objects", "--git-dir="+published, "rev-parse", "--verify", j.Publication.HeadSHA+"^{tree}")
	if err != nil || strings.TrimSpace(publishedTree) != tree {
		return fmt.Errorf("stable published revision differs from the verified candidate tree")
	}
	return ctx.Err()
}

// GrantStableRunLimitOverride requires the caller to own the feature and run
// locks and to have reloaded the selected checkpoint while holding them.
func GrantStableRunLimitOverride(ctx context.Context, directory string, j *Journal) error {
	if err := ValidateStableRunLimitOverride(ctx, directory, *j); err != nil {
		return err
	}
	if j.RunLimitOverride != nil {
		return nil
	}
	before := *j
	j.RunLimitOverride = &RunLimitOverride{GrantedAt: time.Now().UTC(), CheckpointUpdatedAt: j.UpdatedAt, GrantedAttempt: j.Attempt, GrantedRounds: j.Rounds, OriginalSessionID: j.SessionID, LimitKind: "stable_run", StableReady: &StableRunLimitBoundary{Publication: j.Publication, Head: j.Evidence.Head, Tree: j.Evidence.Tree, EvidenceKey: j.Evidence.Key, CIStatus: j.CI.Status, ReviewStatus: j.Outcome.Status}}
	if err := Save(directory, j); err != nil {
		*j = before
		return err
	}
	return nil
}
