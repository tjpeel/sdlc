package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/tjpeel/sdlc/internal/featureguard"
	"github.com/tjpeel/sdlc/internal/filelock"
)

// ProvisionalParentProof is a saved-state prerequisite for starting dependent
// work. Admission must also recheck the live PR, branch and feedback before use.
type ProvisionalParentProof struct {
	Kind              string      `json:"kind,omitempty"`
	ReviewLimitations string      `json:"review_limitations,omitempty"`
	CIStatus          string      `json:"ci_status"`
	CIHistorical      bool        `json:"ci_historical,omitempty"`
	CIHead            string      `json:"ci_head"`
	CIIdentities      string      `json:"ci_identities,omitempty"`
	RunID             string      `json:"run_id"`
	Ticket            string      `json:"ticket"`
	Branch            string      `json:"branch"`
	Base              string      `json:"base"`
	Publication       Publication `json:"publication"`
	CandidateHead     string      `json:"candidate_head"`
	CandidateTree     string      `json:"candidate_tree"`
	CheckKey          string      `json:"check_key"`
	Attempt           int         `json:"attempt"`
	ScannedAt         time.Time   `json:"scanned_at"`
}

func ProvisionalProofForCIHold(j Journal) (ProvisionalParentProof, error) {
	if j.State != "blocked" || j.ResumeState != "ci" || j.CI.Status == "passed" || !ciHeldFeedbackEligible(&j) || j.PRFeedback == nil || j.PRFeedback.Active || j.PRFeedback.FetchPending || activeCIHeldFeedbackFailure(&j) != nil {
		return ProvisionalParentProof{}, fmt.Errorf("provisional parent requires a stopped CI hold, current checks, independent review and a completed feedback scan")
	}
	if ciHeldReviewOutcomeHasWork(j.Outcome) {
		return ProvisionalParentProof{}, fmt.Errorf("provisional parent requires a review without unresolved limitations")
	}
	feedbackStatus := PRFeedbackStatus(j)
	if feedbackStatus.Pending > 0 || feedbackStatus.Unresolved > 0 || feedbackStatus.OutstandingReplies > 0 {
		return ProvisionalParentProof{}, fmt.Errorf("provisional parent has unresolved feedback or replies")
	}
	scan := j.PRFeedback.CIHeldScan
	if scan == nil || scan.RunID != j.ID || scan.Publication != j.Publication || scan.Attempt != j.Attempt || scan.CandidateHead != j.Evidence.Head || scan.CandidateTree != j.Evidence.Tree || scan.CheckKey != j.Evidence.Key || scan.ScannedAt.IsZero() {
		return ProvisionalParentProof{}, fmt.Errorf("provisional parent feedback scan does not match the current reviewed candidate")
	}
	if j.ID == "" || j.Plan.Ticket == "" || j.Plan.Branch == "" {
		return ProvisionalParentProof{}, fmt.Errorf("provisional parent identity is incomplete")
	}
	status, identities := captureCIIdentity(&j)
	historical := false
	if status == "" && identities == "" && j.RuntimeRecheck != nil && j.RuntimeRecheck.Publication == j.Publication {
		status, identities = j.RuntimeRecheck.PreviousCIStatus, j.RuntimeRecheck.PreviousCIIdentities
		historical = status != "" || identities != ""
	}
	if status == "" {
		status = "unverified"
	}
	return ProvisionalParentProof{RunID: j.ID, Ticket: filepath.Base(j.Plan.Ticket), Branch: j.Plan.Branch, Base: j.Plan.Base, Publication: j.Publication, CandidateHead: j.Evidence.Head, CandidateTree: j.Evidence.Tree, CheckKey: j.Evidence.Key, Attempt: j.Attempt, ScannedAt: scan.ScannedAt, CIHead: j.Publication.HeadSHA, CIStatus: status, CIHistorical: historical, CIIdentities: identities}, nil
}

// ProvisionalProofForParent admits either an existing CI hold or a fully
// reviewed, unmerged ready parent. Limitations remain part of the proof.
func ProvisionalProofForParent(j Journal) (ProvisionalParentProof, error) {
	if j.State != "ready" {
		return ProvisionalProofForCIHold(j)
	}
	if j.CI.Status != "passed" || j.ReviewSkip != nil || !completedReviewAwaitingCI(&j) || len(j.ReviewReports) == 0 || j.PRFeedback == nil || j.PRFeedback.Active || j.PRFeedback.FetchPending || j.PRFeedback.LocalPending != nil || j.PRFeedback.CompletedHead != j.Publication.HeadSHA || j.PendingInputs != nil || j.CandidatePhasePending || j.DependencyInvalid || j.RestackNeedsChecks || j.RuntimeRecheck != nil && j.RuntimeRecheck.Pending || j.CIHeldReview != nil && j.CIHeldReview.Pending || activeCIHeldFeedbackFailure(&j) != nil || j.Reconciliation != nil || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingRole != "" || pendingCIFeedback(&j) != nil || j.PushRepair != nil && j.PushRepair.Pending != nil || j.ReviewAssessment != nil && j.ReviewAssessment.Pending || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay {
		return ProvisionalParentProof{}, fmt.Errorf("reviewed provisional parent requires current passed CI, independent review and completed feedback")
	}
	report := j.ReviewReports[len(j.ReviewReports)-1]
	if report.Publication != j.Publication || report.Attempt != j.Attempt || report.Coverage == nil || report.Coverage.Scope != "full" || len(report.Coverage.Gaps) != 0 || report.Coverage.BaseSHA != j.Publication.BaseSHA || report.Coverage.HeadSHA != j.Publication.HeadSHA || !slices.Equal(report.Limitations, j.Outcome.Limitations) || len(j.Outcome.Findings) != 0 || len(j.Outcome.Questions) != 0 || j.Outcome.Status != "reviewed" {
		return ProvisionalParentProof{}, fmt.Errorf("reviewed provisional parent has an unfinished or stale review")
	}
	if !j.Evidence.Passed || !objectID.MatchString(j.Evidence.Head) || !objectID.MatchString(j.Evidence.Tree) || j.Evidence.Key != verificationKey(&j, VerificationRequest{}, j.Evidence.Head, j.Evidence.Tree) || !verificationCurrent(&j, j.Evidence.Head, j.Evidence.Tree) {
		return ProvisionalParentProof{}, fmt.Errorf("reviewed provisional parent requires current configured checks")
	}
	feedback := PRFeedbackStatus(j)
	if feedback.Pending > 0 || feedback.Unresolved > 0 || feedback.OutstandingReplies > 0 || j.ID == "" || j.Plan.Ticket == "" || j.Plan.Branch == "" {
		return ProvisionalParentProof{}, fmt.Errorf("reviewed provisional parent has unresolved feedback or incomplete identity")
	}
	limitations, err := json.Marshal(j.Outcome.Limitations)
	if err != nil || len(limitations) > 32768 {
		return ProvisionalParentProof{}, fmt.Errorf("review limitations exceed provisional proof limits")
	}
	status, identities := captureCIIdentity(&j)
	return ProvisionalParentProof{Kind: "reviewed", ReviewLimitations: string(limitations), RunID: j.ID, Ticket: filepath.Base(j.Plan.Ticket), Branch: j.Plan.Branch, Base: j.Plan.Base, Publication: j.Publication, CandidateHead: j.Evidence.Head, CandidateTree: j.Evidence.Tree, CheckKey: j.Evidence.Key, Attempt: j.Attempt, CIHead: j.Publication.HeadSHA, CIStatus: status, CIIdentities: identities}, nil
}

// SameProvisionalBoundary compares the admission evidence while allowing CI
// status on the same head to change. Captured CI identity remains audit data.
func SameProvisionalBoundary(a, b ProvisionalParentProof) bool {
	a.CIStatus, a.CIHead, a.CIIdentities, a.CIHistorical = "", "", "", false
	b.CIStatus, b.CIHead, b.CIIdentities, b.CIHistorical = "", "", "", false
	return a == b
}

// ValidateProvisionalFeedback rejects new, changed or removed GitHub comments.
// It does not assess them; the operator must run the normal feedback action.
func ValidateProvisionalFeedback(j Journal, comments []PRFeedbackComment) error {
	if _, err := ProvisionalProofForParent(j); err != nil {
		return err
	}
	if len(comments) > maxPRFeedbackComments {
		return fmt.Errorf("too many comments to verify provisional feedback")
	}
	latest := map[string]PRFeedbackEntry{}
	for _, entry := range j.PRFeedback.Entries {
		latest[entry.Comment.ID] = entry
	}
	present := map[string]bool{}
	for _, comment := range comments {
		if err := validateFeedbackComment(comment); err != nil {
			return err
		}
		if present[comment.ID] {
			return fmt.Errorf("duplicate comment in provisional feedback scan")
		}
		present[comment.ID] = true
		prior, ok := latest[comment.ID]
		if !ok || prior.Comment.Deleted || !sameFeedbackRevision(prior.Comment, comment) {
			return fmt.Errorf("new or changed feedback requires --pr-feedback")
		}
	}
	for id, prior := range latest {
		if prior.Comment.Kind != "local" && !prior.Comment.Deleted && !present[id] {
			return fmt.Errorf("removed feedback requires --pr-feedback")
		}
	}
	return nil
}

// ValidateProvisionalDependency fences each child stage on the saved grant,
// parent checkpoint and the live PR, branch and human feedback.
func ValidateProvisionalDependency(ctx context.Context, plan Plan, remote dependencyRemote) error {
	proof := plan.ProvisionalParent
	if proof == nil {
		return nil
	}
	if err := featureguard.Check(plan.Root, plan.Reference); err != nil {
		return ErrDependencyInvalid
	}
	if proof.Ticket == "" || filepath.Base(proof.Ticket) != proof.Ticket || proof.Ticket == filepath.Base(plan.Ticket) || proof.RunID == "" || proof.Branch == "" || plan.Base != proof.Branch || plan.BaseSHA != proof.Publication.HeadSHA || proof.Base == "" {
		return ErrDependencyInvalid
	}
	series := filepath.Join(plan.Root, ".sdlc", "work", plan.Reference, "series", "journal.json")
	info, err := os.Lstat(series)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return ErrDependencyInvalid
	}
	data, err := os.ReadFile(series)
	if err != nil {
		return ErrDependencyInvalid
	}
	var state struct {
		Results map[string]struct {
			State       string
			Provisional *struct {
				Proof ProvisionalParentProof `json:"proof"`
			} `json:"provisional"`
		}
	}
	if json.Unmarshal(data, &state) != nil {
		return ErrDependencyInvalid
	}
	result, ok := state.Results[proof.Ticket]
	if !ok || result.State != "blocked" || result.Provisional == nil || result.Provisional.Proof != *proof {
		return ErrDependencyInvalid
	}
	runDir, err := RunDirectory(plan.Root, plan.Reference, proof.Ticket, proof.RunID, false)
	if err != nil {
		return ErrDependencyInvalid
	}
	lockPath := filepath.Join(runDir, "run.lock")
	before, err := os.Lstat(lockPath)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return ErrDependencyInvalid
	}
	lock, err := filelock.AcquireExisting(lockPath)
	if err != nil {
		return ErrDependencyInvalid
	}
	defer lock.Close()
	currentLock, statErr := os.Lstat(lockPath)
	opened, openErr := lock.Stat()
	if statErr != nil || openErr != nil || !os.SameFile(before, currentLock) || !os.SameFile(before, opened) {
		return ErrDependencyInvalid
	}
	parent, err := Load(runDir)
	if err != nil {
		return ErrDependencyInvalid
	}
	current, err := ProvisionalProofForParent(parent)
	if err != nil || !SameProvisionalBoundary(current, *proof) {
		return ErrDependencyInvalid
	}
	parentPlan := plan
	parentPlan.Branch, parentPlan.Base, parentPlan.BaseSHA = proof.Branch, proof.Base, proof.Publication.BaseSHA
	observed, err := remote.Observe(ctx, parentPlan, proof.Publication)
	if err != nil || observed.State != "OPEN" || observed.Base != proof.Base || observed.BaseSHA != proof.Publication.BaseSHA || observed.HeadSHA != proof.Publication.HeadSHA {
		return ErrDependencyInvalid
	}
	branch, err := remote.Branch(ctx, parentPlan, proof.Branch)
	if err != nil || branch.Branch != proof.Branch || branch.SHA != proof.Publication.HeadSHA {
		return ErrDependencyInvalid
	}
	feedback, ok := remote.(interface {
		Feedback(context.Context, Plan, Publication) ([]PRFeedbackComment, error)
	})
	if !ok {
		return ErrDependencyInvalid
	}
	comments, err := feedback.Feedback(ctx, parentPlan, proof.Publication)
	if err != nil || ValidateProvisionalFeedback(parent, comments) != nil {
		return ErrDependencyInvalid
	}
	if proof.Kind == "reviewed" {
		ci, ok := remote.(interface {
			Checks(context.Context, Plan, Publication) (CIResult, error)
		})
		if !ok {
			return ErrDependencyInvalid
		}
		checks, err := ci.Checks(ctx, parentPlan, proof.Publication)
		if err != nil || validateCIResult(checks, proof.Publication) != nil || checks.Status != "passed" {
			return ErrDependencyInvalid
		}
	}
	// Close the read race with another observation after the feedback scan.
	observed, err = remote.Observe(ctx, parentPlan, proof.Publication)
	if err != nil || observed.State != "OPEN" || observed.Base != proof.Base || observed.BaseSHA != proof.Publication.BaseSHA || observed.HeadSHA != proof.Publication.HeadSHA {
		return ErrDependencyInvalid
	}
	branch, err = remote.Branch(ctx, parentPlan, proof.Branch)
	if err != nil || branch.SHA != proof.Publication.HeadSHA {
		return ErrDependencyInvalid
	}
	return nil
}

// captureCIIdentity retains identifiers only when they belong to this exact PR
// head. It never copies logs or free-form CI output into a dependency proof.
func captureCIIdentity(j *Journal) (string, string) {
	type identity struct {
		Name       string `json:"name,omitempty"`
		Kind       string `json:"kind"`
		RunID      int64  `json:"run_id,omitempty"`
		RunAttempt int    `json:"run_attempt,omitempty"`
		JobID      int64  `json:"job_id,omitempty"`
		CheckID    int64  `json:"check_id,omitempty"`
		Conclusion string `json:"conclusion,omitempty"`
	}
	values := []identity{}
	if c := j.CI.FailureContext; c != nil && c.Head == j.Publication.HeadSHA {
		for _, check := range c.Checks {
			values = append(values, identity{Name: check.Name, Kind: check.Kind, RunID: check.RunID, RunAttempt: check.RunAttempt, JobID: check.JobID, CheckID: check.CheckID, Conclusion: check.Conclusion})
		}
	}
	for _, dispatch := range j.CIWorkflowDispatches {
		if dispatch.Publication == j.Publication && dispatch.RunID > 0 {
			values = append(values, identity{Name: dispatch.Workflow, Kind: "workflow_dispatch", RunID: dispatch.RunID})
		}
	}
	if len(values) == 0 {
		return j.CI.Status, ""
	}
	data, err := json.Marshal(values)
	if err != nil || len(data) > 32768 {
		return j.CI.Status, ""
	}
	return j.CI.Status, string(data)
}

// ProvisionalCIIdentity captures read-only current-head CI identifiers without
// changing the run's CI gate or treating them as child evidence.
func ProvisionalCIIdentity(j Journal, checks CIResult) (string, string, error) {
	if err := validateCIResult(checks, j.Publication); err != nil {
		return "", "", err
	}
	j.CI = checks
	status, identities := captureCIIdentity(&j)
	if status == "" {
		status = "unverified"
	}
	return status, identities, nil
}
