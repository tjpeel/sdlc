package runstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/workrun"
	"strings"
	"testing"
	"time"
)

func TestDiagnoseCIHeldFeedbackBoundaryRecovery(t *testing.T) {
	id, head, base := strings.Repeat("a", 24), strings.Repeat("b", 40), strings.Repeat("c", 40)
	pub := workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: head, BaseSHA: base}
	j := workrun.Journal{ID: id, State: "blocked", ResumeState: "pr_feedback", Plan: workrun.Plan{Repository: "example/project"}, Publication: pub, PRFeedback: &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, FetchPending: true, FetchResumeState: "ci", CandidateStart: head, CIHeldBoundaryFailures: []workrun.CIHeldFeedbackBoundaryFailure{{Publication: pub, At: time.Now()}}}}
	j.SessionID = "saved-session"
	j.Attempt = 1
	j.Plan.Checks = [][]string{{"go", "test"}}
	j.Evidence = workrun.CheckEvidence{Passed: true, Head: head, Tree: base}
	data, _ := json.Marshal(struct {
		Request           workrun.VerificationRequest
		Head, Tree, Image string
		Inputs            []workrun.Input
		Commands          [][]string
		Docker            bool
		Daemon            string
		DaemonMode        string `json:",omitempty"`
	}{workrun.VerificationRequest{}, head, base, j.ImageID, j.Plan.CheckInputs, j.Plan.Checks, false, "", ""})
	sum := sha256.Sum256(data)
	j.Evidence.Key = hex.EncodeToString(sum[:])
	coverage := &workrun.ReviewCoverage{BaseSHA: base, HeadSHA: head, Scope: "full", Gaps: []string{}}
	j.Outcome = workrun.Outcome{Status: "reviewed"}
	j.ReviewReports = []workrun.ReviewReport{{Publication: pub, Attempt: 1, Coverage: coverage}}
	j.CIHeldReview = &workrun.CIHeldReview{Publication: pub, HoldReason: "CI held", StartedAt: time.Now()}
	action := NextRunAction(j, false)
	if action.Category != "ci_feedback_boundary" || action.Command != "sdlc resume --run "+id+" --discard-ci-held-feedback-scan --dry-run" {
		t.Fatalf("unexpected diagnostic: %+v", action)
	}
}
