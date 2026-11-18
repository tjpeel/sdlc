package workrun

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/filelock"
)

// ReviewAcceptance is a human decision on one specific completed review, not a
// change to the review itself. The original findings and limitations remain.
type ReviewAcceptance struct {
	Publication Publication `json:"publication"`
	Attempt     int         `json:"attempt"`
	Tree        string      `json:"tree"`
	ScopeKey    string      `json:"scope_key"`
	Findings    []Finding   `json:"findings"`
	Reason      string      `json:"reason"`
	AcceptedAt  time.Time   `json:"accepted_at"`
}

func validateReviewAcceptances(j *Journal) error {
	if len(j.ReviewAcceptances) > 32 {
		return fmt.Errorf("too many review finding acceptances")
	}
	for _, o := range j.ReviewAcceptances {
		if o.AcceptedAt.IsZero() || o.Attempt < 1 || o.Attempt > j.Attempt || !objectID.MatchString(o.Tree) || len(o.ScopeKey) != 64 || strings.TrimSpace(o.Reason) == "" || len(o.Reason) > 8192 || !utf8.ValidString(o.Reason) || len(o.Findings) == 0 {
			return fmt.Errorf("invalid review finding acceptance")
		}
		for _, r := range o.Reason {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return fmt.Errorf("invalid review finding acceptance reason")
			}
		}
		found := false
		for _, report := range j.ReviewReports {
			c := report.Coverage
			if report.Attempt == o.Attempt && report.Publication == o.Publication && reflect.DeepEqual(report.Findings, o.Findings) && report.InspectionBoundary != nil && report.InspectionBoundary.Tree == o.Tree && report.InspectionBoundary.ScopeKey == o.ScopeKey && c != nil && c.Scope == "full" && len(c.Gaps) == 0 && c.BaseSHA == o.Publication.BaseSHA && c.HeadSHA == o.Publication.HeadSHA {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("review finding acceptance does not match a retained review")
		}
	}
	return nil
}

func currentReviewAcceptance(j *Journal) bool {
	if len(j.ReviewAcceptances) == 0 || len(j.ReviewReports) == 0 || !j.Evidence.Passed {
		return false
	}
	o := j.ReviewAcceptances[len(j.ReviewAcceptances)-1]
	r := j.ReviewReports[len(j.ReviewReports)-1]
	c := r.Coverage
	return j.Outcome.Status == "reviewed" && len(j.Outcome.Questions) == 0 && o.Publication == j.Publication && o.Attempt == j.Attempt && o.Tree == j.Evidence.Tree && o.ScopeKey == incrementalContextKey(j) && r.InspectionBoundary != nil && r.InspectionBoundary.Tree == o.Tree && r.InspectionBoundary.ScopeKey == o.ScopeKey && r.Attempt == o.Attempt && r.Publication == o.Publication && reflect.DeepEqual(o.Findings, j.Outcome.Findings) && reflect.DeepEqual(o.Findings, r.Findings) && c != nil && c.Scope == "full" && len(c.Gaps) == 0 && c.BaseSHA == o.Publication.BaseSHA && c.HeadSHA == o.Publication.HeadSHA
}

// ValidateReviewAcceptance only admits a completed repeated-findings hold. It
// does not waive questions, incomplete review coverage, feedback, or CI.
func HasCurrentReviewAcceptance(j Journal) bool { return currentReviewAcceptance(&j) }

func ReviewAcceptanceAwaitingResume(j Journal) bool {
	return j.State == "blocked" && j.ResumeState == "reviewing" && j.StopReason == reviewUnchangedBound && currentReviewAcceptance(&j)
}

func ValidateReviewAcceptance(j Journal, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 8192 || !utf8.ValidString(reason) {
		return fmt.Errorf("review finding acceptance requires a nonempty UTF-8 reason of at most 8192 bytes")
	}
	for _, r := range reason {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Errorf("review finding acceptance reason contains control characters")
		}
	}
	if j.State != "blocked" || j.ResumeState != "reviewing" || j.StopReason != reviewUnchangedBound || j.Outcome.Status != "reviewed" || len(j.Outcome.Findings) == 0 || len(j.Outcome.Questions) != 0 || len(j.ReviewReports) == 0 || !j.Evidence.Passed || j.CI.Status != "passed" || j.CandidatePhasePending || j.PendingInputs != nil || j.Reconciliation != nil || j.DependencyInvalid || j.RestackNeedsChecks || j.ReviewContinuation != nil || j.ReviewInterruption != nil || j.PendingRole != "" || j.ReviewAssessment == nil || j.ReviewAssessment.Pending || !j.ReviewAssessment.Acknowledged {
		return fmt.Errorf("review finding acceptance requires a settled repeated-findings hold with passing evidence and CI")
	}
	if j.DeferredAnswer != "" || pendingCIFeedback(&j) != nil || j.PRFeedback != nil && j.PRFeedback.FetchPending || j.PushRepair != nil && j.PushRepair.Pending != nil || j.RuntimeRecheck != nil && j.RuntimeRecheck.Pending || j.RunLimitOverride != nil && j.RunLimitOverride.PendingVerificationReplay || j.CIHeldReview != nil && j.CIHeldReview.Pending || j.ExternalPRRequest != "" || j.ExternalPR != nil && !j.ExternalPR.Completed {
		return fmt.Errorf("review finding acceptance cannot bypass pending recovery")
	}
	if j.ReviewAssessment.Publication != j.Publication || j.ReviewRepairBudget == nil || j.ReviewRepairBudget.LastGrantedHead != j.Publication.HeadSHA {
		return fmt.Errorf("review finding acceptance requires the assessed publication")
	}
	s := PRFeedbackStatus(j)
	if s.Pending != 0 || s.Unresolved != 0 || s.OutstandingReplies != 0 {
		return fmt.Errorf("review finding acceptance cannot bypass outstanding PR feedback")
	}
	r := j.ReviewReports[len(j.ReviewReports)-1]
	c := r.Coverage
	if r.Attempt != j.Attempt || r.Publication != j.Publication || !reflect.DeepEqual(r.Findings, j.Outcome.Findings) || c == nil || c.Scope != "full" || len(c.Gaps) != 0 || c.BaseSHA != j.Publication.BaseSHA || c.HeadSHA != j.Publication.HeadSHA || !objectID.MatchString(j.Evidence.Tree) || r.InspectionBoundary == nil || r.InspectionBoundary.Tree != j.Evidence.Tree || r.InspectionBoundary.ScopeKey != incrementalContextKey(&j) {
		return fmt.Errorf("review finding acceptance requires the exact completed full review of the current publication")
	}
	if currentReviewAcceptance(&j) {
		return fmt.Errorf("this review already has an override")
	}
	return validateReviewJournal(&j)
}

// GrantReviewAcceptance serializes against the controller and rejects a stale
// preview. It records only the decision; ordinary resume rechecks live state.
func GrantReviewAcceptance(dir string, checkpoint time.Time, reason string) error {
	info, err := os.Lstat(filepath.Join(dir, "run.lock"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("run controller lock is unsafe")
	}
	lock, err := filelock.Acquire(filepath.Join(dir, "run.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := Load(dir)
	if err != nil {
		return err
	}
	if !j.UpdatedAt.Equal(checkpoint) {
		return fmt.Errorf("run checkpoint changed; preview the override again")
	}
	if err := ValidateReviewAcceptance(j, reason); err != nil {
		return err
	}
	j.ReviewAcceptances = append(j.ReviewAcceptances, ReviewAcceptance{Publication: j.Publication, Attempt: j.Attempt, Tree: j.Evidence.Tree, ScopeKey: incrementalContextKey(&j), Findings: append([]Finding(nil), j.Outcome.Findings...), Reason: reason, AcceptedAt: time.Now().UTC()})
	return Save(dir, &j)
}
