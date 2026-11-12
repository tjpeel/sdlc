package workseries

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestReviewedProvisionalParentRequiresAcknowledgementAndPreservesLimitations(t *testing.T) {
	dir, state, runDir, j := admissionFixture(t)
	j.State, j.ResumeState, j.StopReason = "ready", "", ""
	j.CI.Status = "passed"
	j.CIHeldReview = nil
	j.Outcome.Limitations = []string{"Deployment observation remains open"}
	j.ReviewReports[0].Limitations = append([]string(nil), j.Outcome.Limitations...)
	j.PRFeedback.CompletedHead = j.Publication.HeadSHA
	if err := workrun.Save(runDir, &j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(runDir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	verify := func(_ context.Context, _ State, _ workrun.Journal, proof *workrun.ProvisionalParentProof) error {
		proof.CIStatus = "passed"
		return nil
	}
	preview, err := SetProvisionalParentWithAcknowledgement(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, true, false, verify)
	if err != nil || preview.Proof.Kind != "reviewed" || !strings.Contains(preview.Proof.ReviewLimitations, "Deployment observation") {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	if _, err := SetProvisionalParentWithAcknowledgement(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, false, verify); err == nil || !strings.Contains(err.Error(), "accept-review-limitations") {
		t.Fatal("unacknowledged limitations admitted")
	}
	if _, err := SetProvisionalParentWithAcknowledgement(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, true, verify); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	mark := saved.Results["01-one.md"].Provisional
	if mark == nil || mark.Proof.ReviewLimitations != preview.Proof.ReviewLimitations || len(saved.ProvisionalAudit) != 1 {
		t.Fatal("grant lost limitations or audit")
	}
	after, err := os.ReadFile(filepath.Join(runDir, "journal.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("parent journal changed")
	}
}

func TestReviewedProvisionalParentRejectsStaleOrIncompleteEvidence(t *testing.T) {
	_, _, _, base := admissionFixture(t)
	base.State, base.ResumeState, base.StopReason = "ready", "", ""
	base.CI.Status = "passed"
	base.CIHeldReview = nil
	base.PRFeedback.CompletedHead = base.Publication.HeadSHA
	cases := []struct {
		name   string
		change func(*workrun.Journal)
	}{
		{"ci", func(j *workrun.Journal) { j.CI.Status = "failed" }},
		{"checks", func(j *workrun.Journal) { j.Evidence.Passed = false }},
		{"check key", func(j *workrun.Journal) { j.Evidence.Key = "stale" }},
		{"skipped review", func(j *workrun.Journal) { j.ReviewSkip = &workrun.ReviewSkip{} }},
		{"review head", func(j *workrun.Journal) { j.ReviewReports[0].Publication.HeadSHA = strings.Repeat("f", 40) }},
		{"review limitations", func(j *workrun.Journal) { j.Outcome.Limitations = []string{"unrecorded"} }},
		{"feedback head", func(j *workrun.Journal) { j.PRFeedback.CompletedHead = strings.Repeat("f", 40) }},
		{"active feedback", func(j *workrun.Journal) { j.PRFeedback.Active = true }},
		{"pending inputs", func(j *workrun.Journal) { j.PendingInputs = &workrun.PendingInputRecovery{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := base
			j.ReviewReports = append([]workrun.ReviewReport(nil), base.ReviewReports...)
			feedback := *base.PRFeedback
			j.PRFeedback = &feedback
			tc.change(&j)
			if _, err := workrun.ProvisionalProofForParent(j); err == nil {
				t.Fatal("incomplete evidence accepted")
			}
		})
	}
}

func TestReviewedParentHeadChangeFencesStartedDescendantExplicitly(t *testing.T) {
	dir, state, runDir, j := admissionFixture(t)
	j.State, j.ResumeState, j.StopReason = "ready", "", ""
	j.CI.Status = "passed"
	j.CIHeldReview = nil
	j.PRFeedback.CompletedHead = j.Publication.HeadSHA
	if err := workrun.Save(runDir, &j); err != nil {
		t.Fatal(err)
	}
	parent := state.Results["01-one.md"]
	parent.HeadSHA = sha("old-parent")
	state.Results["01-one.md"] = parent
	state.Results["02-two.md"] = Result{RunID: strings.Repeat("2", 24), State: "blocked", BaseSHA: parent.HeadSHA}
	if err := Save(dir, state); err != nil {
		t.Fatal(err)
	}
	verify := func(_ context.Context, _ State, _ workrun.Journal, p *workrun.ProvisionalParentProof) error {
		p.CIStatus = "passed"
		return nil
	}
	var fenced []string
	invalidator := func(_ context.Context, ticket Ticket, _ Result) error {
		fenced = append(fenced, ticket.File)
		return nil
	}
	preview, err := SetProvisionalParentWithReconciliation(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, true, false, false, verify, invalidator)
	if err != nil || len(preview.ReconcileDescendants) != 1 || len(fenced) != 0 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := SetProvisionalParentWithReconciliation(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, false, false, verify, invalidator); err == nil {
		t.Fatal("head move accepted without reconciliation acknowledgement")
	}
	if len(fenced) != 0 {
		t.Fatal("descendant fenced before acknowledgement")
	}
	if _, err := SetProvisionalParentWithReconciliation(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, false, true, verify, invalidator); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fenced) != 1 || fenced[0] != "02-two.md" || !saved.Results["02-two.md"].DependencyInvalid || saved.Results["01-one.md"].Provisional == nil {
		t.Fatal("descendant was not fenced before granting parent")
	}
}
