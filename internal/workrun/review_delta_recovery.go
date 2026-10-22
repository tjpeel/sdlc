package workrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/runusage"
)

const unnamedReviewGapError = "partial or delta review must identify uninspected areas"

// A native delta is not cumulative approval. Keep its inspection as unfinished
// evidence until the controller reconciles the exact manifest and final judgement.
func normalizeReviewInspectionHandoff(out *Outcome, role string) bool {
	c := out.ReviewCoverage
	if role != "review" || (out.Status != "reviewed" && out.Status != "review_incomplete") || c == nil || (c.Scope != "delta" && c.Scope != "partial") || c.Gaps == nil || len(c.Gaps) != 0 || c.Inspection == nil {
		return false
	}
	c.Gaps = []string{fileInspectionGap}
	out.Status = "review_incomplete"
	return true
}

// Recover only the old decoder's specific hold. Captured controller events and
// attempt metadata bind the original native session, model and exact checkout.
// Nothing here approves a review, changes source or starts a provider session.
func (r Runner) recoverUnnamedReviewGap(ctx context.Context, dir string, j *Journal) error {
	if j.State != "failed" || j.ResumeState != "reviewing" || j.StopReason != unnamedReviewGapError || j.ReviewContinuation != nil {
		return nil
	}
	refuse := func() error {
		return fmt.Errorf("cannot recover unnamed review gap: original attempt, session or snapshot evidence is unavailable or changed")
	}
	if j.ReviewInterruption != nil {
		return refuse()
	}
	a, err := runusage.LoadAttempt(dir, j.ID, j.Attempt, "review")
	if err != nil {
		return refuse()
	}
	m := selectedReviewModel(j)
	boundary := reviewAttemptBoundary(j)
	final := a.Usage.NativeFinalization
	if a.Outcome != "failed" || a.StopCategory != "operational" || a.Resumed || a.Provider != m.Provider || a.Model != m.Name || a.Effort != m.Effort || a.ImageID != j.ImageID || boundary == nil || a.ReviewBoundary == nil || *boundary != *a.ReviewBoundary || final == nil || final.Provider != m.Provider || final.ExitCode != 0 || !sessionIdentifier.MatchString(a.SessionID) {
		return refuse()
	}
	path := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", a.Attempt))
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > 8*1024*1024 {
		return refuse()
	}
	f, err := os.Open(path)
	if err != nil {
		return refuse()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return refuse()
	}
	data, err := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return refuse()
	}
	after, err := f.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return refuse()
	}
	writer := &eventWriter{provider: m.Provider}
	if _, err := writer.Write(data); err != nil {
		return refuse()
	}
	if err := writer.finish("review"); err != nil || writer.result.SessionID != a.SessionID || writer.result.ReportedModel != m.Name || writer.modelChanged {
		return refuse()
	}
	out := writer.result.Outcome
	if out.Status != "review_incomplete" || out.ReviewCoverage == nil || out.ReviewCoverage.BaseSHA != j.Publication.BaseSHA || out.ReviewCoverage.HeadSHA != j.Publication.HeadSHA {
		return refuse()
	}
	snapshots, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("review-source-%d-*", a.Attempt)))
	if err != nil || len(snapshots) != 1 {
		return refuse()
	}
	relative, err := filepath.Rel(dir, snapshots[0])
	if err != nil {
		return refuse()
	}
	snapshot, err := reviewSnapshotPath(dir, relative)
	if err != nil {
		return refuse()
	}
	if err := realDirectory(filepath.Join(dir, fmt.Sprintf("native-review-%d", a.Attempt))); err != nil {
		return refuse()
	}
	revision, err := r.Repository.Inspect(ctx, snapshot)
	if err != nil || !revision.Clean || revision.Head != j.Publication.HeadSHA || revision.Tree != j.Evidence.Tree {
		return refuse()
	}
	if err := verifyInputs(snapshot, j.Plan.Inputs); err != nil {
		return refuse()
	}
	// A recovery cannot manufacture permission to inherit an older report.
	if j.ReviewPlan != nil {
		return refuse()
	}
	if err := r.reconcileReviewInspection(ctx, snapshot, &out); err != nil {
		return err
	}
	copy := *j
	copy.ReviewReports = append([]ReviewReport(nil), j.ReviewReports...)
	copy.Outcome = out
	copy.ReviewContinuation = &ReviewContinuation{Publication: j.Publication, Model: m, Speed: j.Plan.Roles.ReviewSpeed, Tree: revision.Tree, SessionID: a.SessionID, Workspace: relative, StartAttempt: a.Attempt, LastAttempt: a.Attempt, Gaps: out.ReviewCoverage.Gaps, Inspection: out.ReviewCoverage.Inspection, Findings: out.Findings, Recommendations: out.Recommendations}
	if err := validateReviewContinuation(&copy); err != nil {
		return err
	}
	if err := recordReviewReport(&copy); err != nil {
		return err
	}
	if err := r.save(dir, &copy); err != nil {
		return err
	}
	*j = copy
	fmt.Fprintln(r.Output, "Recovered unfinished inspection from the original reviewer session and unchanged snapshot; current CI and remaining file coverage are still required.")
	return nil
}
