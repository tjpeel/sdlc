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
// and corrects the quota proof copy without rewriting the original usage ledger.
func recoverSyntheticQuotaModel(dir string, j *Journal, a *runusage.Attempt) (auth bool, err error) {
	refuse := func() error {
		return fmt.Errorf("saved synthetic reviewer model hold lacks corroborated safe native quota evidence")
	}
	if a.Provider != "claude" || a.Model != j.Plan.Roles.Review.Name || a.Effort != selectedReviewModel(j).Effort || a.ImageID != j.ImageID || a.Outcome != "failed" || a.StartedAt.IsZero() || a.EndedAt.IsZero() || a.EndedAt.Before(a.StartedAt) || a.EndedAt.After(j.UpdatedAt) || a.StopCategory != "operational" || a.Usage.ReportedModel != "<synthetic>" || a.Resumed || a.ReviewBoundary == nil || a.Usage.NativeFinalization == nil || a.Usage.NativeFinalization.Provider != "claude" || a.Usage.NativeFinalization.ExitCode != 1 {
		return false, refuse()
	}
	expected := reviewAttemptBoundary(j)
	if expected == nil || *a.ReviewBoundary != *expected {
		return false, refuse()
	}
	window, stored, rejected := reviewUsageRejection(*a)
	if err := realDirectory(dir); err != nil {
		return false, err
	}
	path := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", a.Attempt))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 8*1024*1024 {
		return false, refuse()
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, refuse()
	}
	data, err := io.ReadAll(io.LimitReader(file, 8*1024*1024+1))
	if err != nil {
		return false, err
	}
	if len(data) > 8*1024*1024 {
		return false, refuse()
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return false, refuse()
	}
	writer := &eventWriter{provider: "claude"}
	if _, err := writer.Write(data); err != nil {
		return false, refuse()
	}
	if err := writer.finish("review"); err != errNativeIncomplete {
		return false, refuse()
	}
	if writer.modelChanged || writer.initializedModel != a.Model || writer.result.ReportedModel != a.Model || writer.syntheticQuotaError == writer.syntheticAuthError || writer.completed || !writer.failed {
		return false, refuse()
	}
	if pending, err := unresolvedReviewHandoff(writer.final); err != nil || pending != nil {
		return false, refuse()
	}
	observer := runusage.NewObserver("claude", a.Model)
	_, _ = observer.Write(data)
	observer.Finish()
	usage := observer.Snapshot()
	if usage.NativeFinalization == nil || usage.NativeFinalization.Provider != "claude" || usage.NativeFinalization.ExitCode != 1 {
		return false, refuse()
	}
	if writer.syntheticAuthError {
		if !writer.syntheticAuthExpired {
			return false, refuse()
		}
		for _, limit := range usage.RateLimits {
			if limit.Status == "rejected" {
				return false, refuse()
			}
		}
		if usage.ReportedModel != a.Model || !usage.ModelMatches {
			return false, refuse()
		}
		// Authentication failures never establish a quota skip. Retry the same
		// reviewer through the normal flow; the provider enforces its auth.
		if rejected {
			return false, refuse()
		}
		return true, nil
	}
	if !rejected {
		return false, refuse()
	}
	current, ok := usage.RateLimits[window]
	if !ok || current.Status != "rejected" || current.Source != stored.Source || !sameOptionalReset(current.ResetsAt, stored.ResetsAt) || usage.NativeFinalization == nil || usage.NativeFinalization.Provider != "claude" || usage.NativeFinalization.ExitCode != 1 {
		return false, refuse()
	}
	a.Usage.ReportedModel, a.Usage.ModelMatches = a.Model, true
	a.StopCategory = "native_execution"
	return false, nil
}

func sameOptionalReset(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
