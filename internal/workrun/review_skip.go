package workrun

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runusage"
)

// ReviewSkip records missing independent review, never an approval.
type ReviewSkip struct {
	EffortSelection *runusage.ReviewEffortSelection `json:"effort_selection,omitempty"`

	ModelEvidenceSource string      `json:"model_evidence_source,omitempty"`
	Publication         Publication `json:"publication"`
	Tree                string      `json:"tree"`
	EvidenceKey         string      `json:"evidence_key"`
	Provider            string      `json:"provider"`
	Model               string      `json:"model"`
	Effort              string      `json:"effort"`
	Attempt             int         `json:"attempt"`
	StopCategory        string      `json:"stop_category"`
	Source              string      `json:"source"`
	Window              string      `json:"window"`
	ObservedAt          time.Time   `json:"observed_at"`
	RecordedAt          time.Time   `json:"recorded_at"`
	ResetsAt            *int64      `json:"resets_at,omitempty"`
}

func reviewAttemptBoundary(j *Journal) *runusage.ReviewBoundary {
	if j.Publication.Number < 1 || !objectID.MatchString(j.Publication.HeadSHA) || !objectID.MatchString(j.Publication.BaseSHA) || !objectID.MatchString(j.Evidence.Tree) {
		return nil
	}
	return &runusage.ReviewBoundary{Number: j.Publication.Number, Head: j.Publication.HeadSHA, Base: j.Publication.BaseSHA, Tree: j.Evidence.Tree, EvidenceKey: j.Evidence.Key}
}

func nativeStopCategory(err error) string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 1 {
			return nativeStopCategory(children[0])
		}
		return "operational"
	}
	if err == errNativeIncomplete {
		return "native_incomplete"
	}
	if _, ok := err.(*providerauth.NativeExecutionError); ok {
		return "native_execution"
	}
	return "operational"
}

func ReviewSkipNotice(j Journal) string {
	s := j.ReviewSkip
	if s == nil || s.Publication != j.Publication || j.Outcome.Status == "reviewed" {
		return ""
	}
	return fmt.Sprintf("Independent review skipped: %s %s exhausted native usage (%s) at attempt %d on %s. Human review is required; no independent approval was recorded.", s.Provider, s.Model, s.Window, s.Attempt, s.Publication.HeadSHA)
}

func validateReviewSkip(j *Journal) error {
	s := j.ReviewSkip
	if s == nil {
		return nil
	}
	expectedEffort := j.Plan.Roles.Review.Effort
	if s.EffortSelection != nil {
		if j.Plan.Roles.ReviewEffortPolicy != "pr-v1" || s.Provider != "claude" {
			return fmt.Errorf("invalid review skip effort policy")
		}
		if err := s.EffortSelection.Validate(); err != nil {
			return err
		}
		expectedEffort = s.EffortSelection.Effort
	}

	if s.ModelEvidenceSource != "" && s.ModelEvidenceSource != "retained_native_events" {
		return fmt.Errorf("invalid review model recovery evidence")
	}
	if _, err := hex.DecodeString(s.EvidenceKey); err != nil {
		return fmt.Errorf("invalid retained review skip check key")
	}
	if j.State == "ready" && j.Outcome.Status != "reviewed" && (s.Publication != j.Publication || s.Tree != j.Evidence.Tree || s.EvidenceKey != j.Evidence.Key || !j.Evidence.Passed || j.CI.Status != "passed") {
		return fmt.Errorf("independent review skip differs from ready verification boundary")
	}
	if s.Attempt < 1 || s.Attempt > j.Attempt || s.RecordedAt.IsZero() || s.ObservedAt.IsZero() || s.RecordedAt.Before(s.ObservedAt) || s.Publication.Number < 1 || s.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, s.Publication.Number) || !objectID.MatchString(s.Publication.HeadSHA) || !objectID.MatchString(s.Publication.BaseSHA) || !objectID.MatchString(s.Tree) || (s.EvidenceKey != "" && len(s.EvidenceKey) != 64) || s.Provider != j.Plan.Roles.Review.Provider || s.Model != j.Plan.Roles.Review.Name || s.Effort != expectedEffort || (s.StopCategory != "native_execution" && s.StopCategory != "native_incomplete") || !validReviewUsageSignal(s.Provider, s.Source, s.Window) {
		return fmt.Errorf("invalid retained independent review skip")
	}
	return nil
}

func validReviewUsageSignal(provider, source, window string) bool {
	if provider == "codex" && source == "codex.exec.fatal_error" && window == "account_usage" {
		return true
	}
	if provider == "claude" && source == "claude.rate_limit_event" {
		switch window {
		case "five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "overage":
			return true
		}
	}
	return false
}

func reviewUsageRejection(a runusage.Attempt) (string, runusage.RateLimit, bool) {
	if stop := a.Usage.NativeStop; stop != nil && a.Provider == "codex" && stop.Category == "usage_exhausted" && validReviewUsageSignal(a.Provider, stop.Source, "account_usage") && !stop.ObservedAt.Before(a.StartedAt) && !stop.ObservedAt.After(a.EndedAt) {
		return "account_usage", runusage.RateLimit{Source: stop.Source, Status: "rejected", ObservedAt: stop.ObservedAt}, true
	}
	// Only explicit native rejection is conclusive. Unknown probes, warnings,
	// near-limit percentages and raw error messages do not establish exhaustion.
	for _, window := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "overage"} {
		limit, ok := a.Usage.RateLimits[window]
		if ok && limit.Status == "rejected" && validReviewUsageSignal(a.Provider, limit.Source, window) && !limit.ObservedAt.IsZero() && !limit.ObservedAt.Before(a.StartedAt) && !limit.ObservedAt.After(a.EndedAt) {
			return window, limit, true
		}
	}
	return "", runusage.RateLimit{}, false
}

func savedReviewStopCategory(j *Journal) string {
	if j.State != "failed" || j.ResumeState != "reviewing" {
		return ""
	}
	if j.Plan.Roles.Review.Provider == "claude" && j.StopReason == (&providerauth.NativeExecutionError{Provider: "claude"}).Error()+"\n"+providerModelMismatchMessage {
		return "claude_synthetic_quota"
	}
	// These exact controller-owned legacy reasons preceded typed categories.
	switch j.StopReason {
	case errNativeIncomplete.Error():
		return "native_incomplete"
	case (&providerauth.NativeExecutionError{Provider: j.Plan.Roles.Review.Provider}).Error():
		return "native_execution"
	}
	return ""
}

func (r Runner) reviewUsageProof(ctx context.Context, dir string, j *Journal, category string, legacy bool) (*ReviewSkip, error) {
	reconcileSynthetic := legacy && category == "claude_synthetic_quota"
	if reconcileSynthetic {
		category = "native_execution"
	}
	if category != "native_execution" && category != "native_incomplete" {
		return nil, nil
	}
	a, err := runusage.LoadAttempt(dir, j.ID, j.Attempt, "review")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := selectedReviewModel(j)
	if reconcileSynthetic {
		if err := recoverSyntheticQuotaModel(dir, j, &a); err != nil {
			return nil, err
		}
	}
	if a.Outcome != "failed" || a.Provider != m.Provider || a.Model != m.Name || a.Effort != m.Effort || a.ImageID != j.ImageID || a.Resumed || !a.EndedAt.IsZero() && a.EndedAt.After(j.UpdatedAt) && legacy || a.Usage.ReportedModel != "" && a.Usage.ReportedModel != m.Name {
		return nil, nil
	}
	if a.StopCategory != "" && a.StopCategory != category {
		return nil, nil
	}
	complete := a.Usage.NativeFinalization
	if complete != nil {
		if complete.Provider != a.Provider || category == "native_execution" && complete.ExitCode == 0 || category == "native_incomplete" && complete.ExitCode != 0 {
			return nil, nil
		}
	} else {
		if !legacy || a.ReviewBoundary != nil || a.StopCategory != "" {
			return nil, nil
		}
		ok, err := legacyReviewFinalized(dir, a)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
	}
	window, limit, rejected := reviewUsageRejection(a)
	if !rejected {
		return nil, nil
	}
	if a.ReviewBoundary != nil {
		expected := reviewAttemptBoundary(j)
		if expected == nil || *a.ReviewBoundary != *expected {
			return nil, fmt.Errorf("review usage evidence belongs to a different published boundary")
		}
	} else {
		if !legacy {
			return nil, nil
		}
		// Old metrics had no head. Corroborate the retained snapshot for that
		// exact failed attempt; never harvest provider transcripts or raw logs.
		paths, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("review-source-%d-*", a.Attempt)))
		if err != nil || len(paths) != 1 {
			return nil, nil
		}
		if err := realDirectory(paths[0]); err != nil {
			return nil, err
		}
		rev, err := r.Repository.Inspect(ctx, paths[0])
		if err != nil || !rev.Clean || rev.Head != j.Publication.HeadSHA || rev.Tree != j.Evidence.Tree {
			return nil, fmt.Errorf("saved exhausted review snapshot differs from the current publication")
		}
	}
	result := &ReviewSkip{Publication: j.Publication, Tree: j.Evidence.Tree, EvidenceKey: j.Evidence.Key, Provider: m.Provider, Model: m.Name, Effort: m.Effort, Attempt: a.Attempt, StopCategory: category, Source: limit.Source, Window: window, ObservedAt: limit.ObservedAt, ResetsAt: limit.ResetsAt}
	if a.ReviewEffortSelection != nil {
		copy := *a.ReviewEffortSelection
		result.EffortSelection = &copy
	} else if d := j.ReviewEffort; j.Plan.Roles.ReviewEffortPolicy == "pr-v1" && d != nil && d.Head == result.Publication.HeadSHA && d.Base == result.Publication.BaseSHA && d.Model == result.Model {
		copy := d.Selection
		result.EffortSelection = &copy
	}

	if reconcileSynthetic {
		result.ModelEvidenceSource = "retained_native_events"
	}
	return result, nil
}

func (r Runner) completeSkippedReview(ctx context.Context, dir string, j *Journal, skip *ReviewSkip) error {
	if j.ReviewInterruption != nil || j.Outcome.Status != "implemented" || !j.Outcome.LocalReview || len(j.Outcome.Findings) != 0 || len(j.Outcome.Questions) != 0 || len(j.Outcome.Limitations) != 0 || len(j.Outcome.RepositoryRequests) != 0 || j.PendingInputs != nil || j.ReviewAssessment != nil && (j.ReviewAssessment.Pending || !j.ReviewAssessment.Acknowledged) {
		return r.stop(dir, j, "blocked", "independent review usage exhaustion cannot bypass unresolved implementation or review work")
	}
	if status := PRFeedbackStatus(*j); status.Pending > 0 || status.OutstandingReplies > 0 {
		return r.stop(dir, j, "blocked", "human PR feedback assessments or replies remain outstanding")
	}
	if err := r.reviewHandoffPreflight(ctx, dir, j); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	skip.RecordedAt = time.Now().UTC()
	j.ReviewSkip = skip
	if err := validateReviewSkip(j); err != nil {
		return r.stop(dir, j, "blocked", err.Error())
	}
	if j.PRFeedback != nil && j.PRFeedback.Active {
		j.PRFeedback.Active = false
		j.PRFeedback.CompletedHead = j.Publication.HeadSHA
	}
	if err := r.transition(dir, j, "ready"); err != nil {
		return err
	}
	fmt.Fprintln(r.Output, ReviewSkipNotice(*j))
	fmt.Fprintln(r.Output, "Ready for human review:", j.Publication.URL)
	return nil
}

// Legacy helpers emitted after-work quota snapshots before persisting their
// opaque auth snapshot. Check transaction completion by path metadata only.
func legacyReviewFinalized(dir string, a runusage.Attempt) (bool, error) {
	if a.Usage.Subscription == nil || a.Usage.Subscription.After == nil {
		return false, nil
	}
	after := a.Usage.Subscription.After
	if after.ObservedAt.Before(a.StartedAt) || after.ObservedAt.After(a.EndedAt) {
		return false, nil
	}
	config := filepath.Join(dir, fmt.Sprintf("native-review-%d", a.Attempt), a.Provider)
	if err := realDirectory(config); err != nil {
		return false, nil
	}
	credential := map[string]string{"codex": "auth.json", "claude": ".credentials.json"}[a.Provider]
	if credential == "" {
		return false, nil
	}
	// Names match the official opaque-copy transaction in container.py. No
	// credential file or baseline content is opened or decoded here.
	for _, name := range []string{".native-auth-base.json", ".unsafe-native-auth-base.json", credential} {
		if _, err := os.Lstat(filepath.Join(config, name)); !errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("diagnostics-%d.log", a.Attempt))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, fmt.Errorf("saved reviewer diagnostic changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return false, err
	}
	if len(data) > 64*1024 || bytes.Contains(data, []byte("SDLC could not complete the native headless session safely.")) {
		return false, nil
	}
	return true, nil
}
