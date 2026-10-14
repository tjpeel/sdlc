package workrun

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/runusage"
)

// Recover only the prior decoder's specific synthetic-envelope model hold.
// This reads controller-captured JSONL, never executes or resumes a provider,
// and corrects the proof copy without rewriting the original usage ledger.
func recoverSyntheticQuotaModel(dir string, j *Journal, a *runusage.Attempt) error {
	refuse := func() error {
		return fmt.Errorf("saved synthetic reviewer model hold lacks corroborated safe native quota evidence")
	}
	if a.Provider != "claude" || a.Model != j.Plan.Roles.Review.Name || a.Effort != j.Plan.Roles.Review.Effort || a.ImageID != j.ImageID || a.Outcome != "failed" || a.StopCategory != "operational" || a.Usage.ReportedModel != "<synthetic>" || a.Resumed || a.ReviewBoundary == nil || a.Usage.NativeFinalization == nil || a.Usage.NativeFinalization.Provider != "claude" || a.Usage.NativeFinalization.ExitCode != 1 {
		return refuse()
	}
	expected := reviewAttemptBoundary(j)
	if expected == nil || *a.ReviewBoundary != *expected {
		return refuse()
	}
	window, stored, rejected := reviewUsageRejection(*a)
	if !rejected {
		return refuse()
	}
	if err := realDirectory(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", a.Attempt))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 8*1024*1024 {
		return refuse()
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return refuse()
	}
	data, err := io.ReadAll(io.LimitReader(file, 8*1024*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 8*1024*1024 {
		return refuse()
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return refuse()
	}
	writer := &eventWriter{provider: "claude"}
	if _, err := writer.Write(data); err != nil {
		return refuse()
	}
	if err := writer.finish("review"); err != errNativeIncomplete {
		return refuse()
	}
	if writer.modelChanged || writer.initializedModel != a.Model || writer.result.ReportedModel != a.Model || !writer.syntheticQuotaError || writer.completed || !writer.failed {
		return refuse()
	}
	if pending, err := unresolvedReviewHandoff(writer.final); err != nil || pending != nil {
		return refuse()
	}
	observer := runusage.NewObserver("claude", a.Model)
	_, _ = observer.Write(data)
	observer.Finish()
	usage := observer.Snapshot()
	current, ok := usage.RateLimits[window]
	if !ok || current.Status != "rejected" || current.Source != stored.Source || !sameOptionalReset(current.ResetsAt, stored.ResetsAt) || usage.NativeFinalization == nil || usage.NativeFinalization.Provider != "claude" || usage.NativeFinalization.ExitCode != 1 {
		return refuse()
	}
	a.Usage.ReportedModel, a.Usage.ModelMatches = a.Model, true
	a.StopCategory = "native_execution"
	return nil
}

func sameOptionalReset(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
