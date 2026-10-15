package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// PRCheckpoint records an entry point, never an assertion that skipped gates passed.
type PRCheckpoint struct {
	StartAt         string     `json:"start_at"`
	Head            string     `json:"head"`
	Tree            string     `json:"tree"`
	RequestedAt     time.Time  `json:"requested_at"`
	HumanReviewedBy string     `json:"human_reviewed_by,omitempty"`
	HumanReviewedAt *time.Time `json:"human_reviewed_at,omitempty"`
	PreviousState   string     `json:"previous_state"`
	Archive         string     `json:"archive"`
	Completed       bool       `json:"completed"`
	ReviewOutcome   *Outcome   `json:"review_outcome,omitempty"`
}

func ValidPRStartAt(value string) bool {
	switch value {
	case "awaiting-feedback", "checks", "review", "feedback":
		return true
	}
	return false
}
func latestPRCheckpoint(j *Journal) *PRCheckpoint {
	if len(j.PRCheckpoints) == 0 {
		return nil
	}
	return &j.PRCheckpoints[len(j.PRCheckpoints)-1]
}
func activePRCheckpoint(j *Journal, mode string) bool {
	c := latestPRCheckpoint(j)
	return c != nil && c.StartAt == mode && !c.Completed
}
func validatePRCheckpoints(j *Journal) error {
	for _, c := range j.PRCheckpoints {
		if !ValidPRStartAt(c.StartAt) || !objectID.MatchString(c.Head) || !objectID.MatchString(c.Tree) || c.RequestedAt.IsZero() || !safeRelative(c.Archive) || filepath.Dir(c.Archive) != "pr-checkpoints" || c.HumanReviewedAt != nil && (c.HumanReviewedAt.IsZero() || c.HumanReviewedBy == "") || c.HumanReviewedAt == nil && c.HumanReviewedBy != "" {
			return fmt.Errorf("invalid PR checkpoint record")
		}
	}
	if j.State == "awaiting_feedback" || j.State == "checkpoint_checks" || j.State == "checkpoint_review" {
		if latestPRCheckpoint(j) == nil {
			return fmt.Errorf("PR entry state lacks a checkpoint")
		}
	}
	return nil
}

// Recovery is deliberately narrower than arbitrary state assignment. It admits
// the known invalid feedback-commit claim, not provider, CI, access or policy holds.
func ValidatePRCheckpointResume(j Journal) error {
	if j.Publication.Number < 1 || j.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, j.Publication.Number) || !objectID.MatchString(j.Publication.HeadSHA) || !objectID.MatchString(j.Publication.BaseSHA) {
		return fmt.Errorf("PR entry point requires an existing exact publication")
	}
	if len(j.Outcome.Questions) > 0 {
		return fmt.Errorf("PR entry point cannot discard required human questions")
	}
	if j.PendingInputs != nil || j.CandidatePhasePending || j.Reconciliation != nil || j.ReviewInterruption != nil || pendingCIFeedback(&j) != nil || j.PushRepair != nil && j.PushRepair.Pending != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay {
		return fmt.Errorf("PR entry point cannot bypass pending recovery or interrupted work")
	}
	if j.State == "awaiting_feedback" || j.State == "ready" {
		return validatePRCheckpoints(&j)
	}
	if j.State == "blocked" && j.ResumeState == "implementing" && j.StopReason == noPendingFeedbackHold && j.Outcome.Status == "implemented" && j.Outcome.LocalReview && len(j.Outcome.Questions) == 0 && len(j.Outcome.Findings) == 0 && len(j.Outcome.Limitations) == 0 && j.PRFeedback != nil && j.PRFeedback.Active && len(pendingFeedbackEntries(&j)) == 0 && len(j.Outcome.PRFeedbackAssessments) > 0 {
		cleaned := withoutInactiveFeedbackAssessments(&j, j.Outcome)
		if len(cleaned.PRFeedbackAssessments) == 0 {
			return validatePRCheckpoints(&j)
		}
	}
	if j.State == "blocked" && j.ResumeState == "implementing" && j.StopReason == "claimed feedback commit is absent from candidate history" && j.PRFeedback != nil && j.PRFeedback.Active && j.Outcome.Status == "checks_requested" && len(j.Outcome.Questions) == 0 && len(j.Outcome.Findings) == 0 {
		return validatePRCheckpoints(&j)
	}
	return fmt.Errorf("this stop cannot be replaced by a PR entry point; resolve its recorded hold")
}

func validateParkedPR(j Journal) error {
	if j.State != "awaiting_feedback" && !(j.PRFeedback != nil && j.PRFeedback.FetchPending && j.PRFeedback.FetchResumeState == "awaiting_feedback") {
		return fmt.Errorf("run is not awaiting feedback")
	}
	c := latestPRCheckpoint(&j)
	if c == nil || !c.Completed || c.Head != j.Publication.HeadSHA || !objectID.MatchString(c.Tree) || j.PendingInputs != nil || j.CandidatePhasePending {
		return fmt.Errorf("parked PR boundary is unavailable")
	}
	return validatePRCheckpoints(&j)
}

func (r Runner) checkpointBoundary(ctx context.Context, j *Journal) (Revision, error) {
	revision, err := r.Repository.Inspect(ctx, j.Workspace)
	if err != nil {
		return revision, err
	}
	if !revision.Clean || revision.Head != j.Publication.HeadSHA {
		return revision, fmt.Errorf("PR entry point requires the clean, unchanged published candidate")
	}
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return revision, err
	}
	if err := verifyInputs(filepath.Join(filepath.Dir(j.Workspace), "check-inputs"), j.Plan.CheckInputs); err != nil {
		return revision, err
	}
	p, ok := r.Publisher.(interface {
		ValidateFeedbackBoundary(context.Context, Plan, Publication) error
	})
	if !ok {
		return revision, fmt.Errorf("publisher cannot verify the PR entry boundary")
	}
	if err := p.ValidateFeedbackBoundary(ctx, j.Plan, j.Publication); err != nil {
		return revision, err
	}
	return revision, nil
}

func (r Runner) startPRCheckpoint(ctx context.Context, dir string, j *Journal) error {
	revision, err := r.checkpointBoundary(ctx, j)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(dir, "pr-checkpoints")
	if err := os.Mkdir(archiveDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := realDirectory(archiveDir); err != nil {
		return err
	}
	info, err := os.Lstat(archiveDir)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 {
		return fmt.Errorf("checkpoint archive must be private")
	}

	id, err := NewID()
	if err != nil {
		return err
	}
	archive := filepath.Join("pr-checkpoints", id+".json")
	if err := saveJSON(filepath.Join(dir, archive), j); err != nil {
		return err
	}
	now := time.Now().UTC()
	c := PRCheckpoint{StartAt: r.StartAt, Head: revision.Head, Tree: revision.Tree, RequestedAt: now, PreviousState: j.State, Archive: archive}
	if r.HumanReviewed {
		c.HumanReviewedAt = &now
		c.HumanReviewedBy = fmt.Sprintf("local uid %d", os.Getuid())
	}
	j.PRCheckpoints = append(j.PRCheckpoints, c)
	initial := j.State == "adopting"
	if initial && r.StartAt == "feedback" {
		latestPRCheckpoint(j).Completed = true
		return r.save(dir, j)
	}
	// The full prior journal is archived before superseding the rejected selection.
	// Session identity, metrics, attempts, remaining budgets and verification requests stay.
	j.PRFeedback = nil
	j.Outcome = Outcome{}
	j.Feedback = ""
	j.PendingRole = ""
	j.StopReason, j.ResumeState = "", ""
	j.ConfiguredFeedbackKey = ""
	switch r.StartAt {
	case "checks":
		return r.transition(dir, j, "checkpoint_checks")
	case "review":
		return r.transition(dir, j, "checkpoint_review")
	default:
		latestPRCheckpoint(j).Completed = true
		if err := r.transition(dir, j, "awaiting_feedback"); err != nil {
			return err
		}
		if r.StartAt == "feedback" {
			return r.beginPRFeedback(ctx, dir, j)
		}
		return nil
	}
}

func (r Runner) runPRCheckpointChecks(ctx context.Context, dir string, j *Journal) error {
	revision, err := r.checkpointBoundary(ctx, j)
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	c := latestPRCheckpoint(j)
	if c == nil || c.Head != revision.Head || c.Tree != revision.Tree {
		return r.stop(dir, j, "blocked", "PR checkpoint candidate changed")
	}
	j.CheckAttempt++
	if err := r.save(dir, j); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("checkpoint-checks-%d.log", j.CheckAttempt)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	capture := newCommandOutputCollector(io.MultiWriter(file, r.rawOutput))
	start := time.Now()
	check := func(checker Checker) error { return checker.Check(ctx, j.Workspace, effectiveChecks(j), capture) }
	if phase, ok := r.Checker.(CandidatePhaseChecker); ok {
		err = phase.WithCandidate(ctx, j.Workspace, capture, check)
	} else {
		err = check(r.Checker)
	}
	j.Timings.ChecksMS += time.Since(start).Milliseconds()
	err = errors.Join(err, file.Close())
	after, inspectErr := r.checkpointBoundary(ctx, j)
	if inspectErr != nil || after.Head != revision.Head || after.Tree != revision.Tree {
		j.Evidence = CheckEvidence{}
		return r.stop(dir, j, "blocked", "PR or candidate changed during checkpoint checks")
	}
	j.Evidence = CheckEvidence{Head: revision.Head, Tree: revision.Tree, Passed: err == nil, Commands: effectiveChecks(j), Log: filepath.Base(file.Name()), Output: capture.results()}
	if err != nil {
		return r.stop(dir, j, "blocked", "checkpoint checks did not pass; inspect retained check evidence")
	}
	c.Completed = true
	return r.transition(dir, j, "awaiting_feedback")
}

func (r Runner) runPRCheckpointReview(ctx context.Context, dir string, j *Journal) error {
	revision, err := r.checkpointBoundary(ctx, j)
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	c := latestPRCheckpoint(j)
	if c == nil || c.Head != revision.Head || c.Tree != revision.Tree {
		return r.stop(dir, j, "blocked", "PR checkpoint candidate changed")
	}
	status, err := r.Provider.Status(ctx, j.Plan.Roles.Review.Provider)
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if status != "stored" {
		return r.stop(dir, j, "blocked", "authenticate the selected independent reviewer before resuming")
	}
	if r.ReviewWorkspace == nil {
		return r.stop(dir, j, "blocked", "independent review snapshot unavailable")
	}
	workspace, err := r.ReviewWorkspace(ctx, *j, dir)
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if err := r.selectReviewEffort(ctx, dir, j, workspace); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	result, err := r.session(ctx, dir, j, "review", workspace, "")
	if result.ReviewInterruption != nil {
		interruption := *result.ReviewInterruption
		interruption.Publication, interruption.Attempt, interruption.Provider, interruption.Model = j.Publication, j.Attempt, j.Plan.Roles.Review.Provider, j.Plan.Roles.Review.Name
		j.ReviewInterruption = &interruption
		return r.stop(dir, j, "blocked", "standalone review stopped with unresolved structured work; inspect retained review evidence")
	}
	if err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	j.ReviewInterruption = nil
	j.Outcome = result.Outcome
	copy := result.Outcome
	c.ReviewOutcome = &copy
	if result.Outcome.Status == "waiting_for_human" {
		j.PendingRole = "review"
		return r.stop(dir, j, "waiting_for_human", "independent review requires a human answer")
	}
	if result.Outcome.Status == "checks_requested" {
		if err := validateOutcome(j.Outcome, "review"); err != nil {
			return r.stop(dir, j, "blocked", err.Error())
		}
		if err := r.stageReviewCompanions(ctx, dir, j, j.Outcome.RepositoryRequests); err != nil {
			return r.stop(dir, j, "blocked", "standalone review companion source could not be staged")
		}
		after, err := r.checkpointBoundary(ctx, j)
		if err != nil || after.Head != revision.Head || after.Tree != revision.Tree {
			return r.stop(dir, j, "blocked", "PR changed during review source handoff")
		}
		return r.transition(dir, j, "checkpoint_review")
	}
	if result.Outcome.Status != "reviewed" {
		return r.stop(dir, j, "blocked", "independent review did not complete")
	}
	if err := recordReviewReport(j); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	after, err := r.checkpointBoundary(ctx, j)
	if err != nil || after.Head != revision.Head || after.Tree != revision.Tree {
		return r.stop(dir, j, "blocked", "PR changed during independent review")
	}
	c.Completed = true
	return r.transition(dir, j, "awaiting_feedback")
}
