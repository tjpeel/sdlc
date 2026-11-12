package workrun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestSavedSyntheticAuthRecoveryRequiresMatchingEvidence(t *testing.T) {
	for _, fault := range []string{"", "no_init", "real_mismatch", "no_finalization", "malformed", "wrong_boundary", "mixed_quota", "other_auth"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.Attempt++
			j.State, j.ResumeState, j.StopReason = "blocked", "reviewing", "saved synthetic reviewer model hold lacks corroborated safe native quota evidence"
			now := time.Now().UTC()
			a := runusage.Attempt{Version: 1, RunID: j.ID, Attempt: j.Attempt, Role: "review", Provider: "claude", Model: j.Plan.Roles.Review.Name, Effort: j.Plan.Roles.Review.Effort, ImageID: j.ImageID, StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-time.Second), Outcome: "failed", OptimizerMode: "off", StopCategory: "operational", ReviewBoundary: reviewAttemptBoundary(j), Usage: runusage.Usage{ReportedModel: "<synthetic>", NativeFinalization: &runusage.NativeFinalization{Provider: "claude", ExitCode: 1, ObservedAt: now.Add(-time.Second)}}}
			eventFault := fault
			if fault == "" || fault == "wrong_boundary" || fault == "mixed_quota" || fault == "other_auth" {
				eventFault = "no_quota"
			}
			events := strings.Replace(syntheticClaudeFailureEvents(a.Model, eventFault), `"error":"rate_limit"`, `"error":"authentication_failed"`, 1)
			if fault != "other_auth" {
				events = strings.Replace(events, "Offline quota fixture.", "Failed to authenticate: this login reached its time limit and cannot be renewed.", 1)
			}
			if fault == "wrong_boundary" {
				a.ReviewBoundary.Head = strings.Repeat("a", 40)
			}
			if fault == "mixed_quota" {
				a.Usage.RateLimits = map[string]runusage.RateLimit{"five_hour": {Source: "claude.rate_limit_event", Status: "rejected", ObservedAt: now.Add(-2 * time.Second)}}
			}
			if err := runusage.SaveAttempt(dir, a); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", j.Attempt)), []byte(events), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			category := savedReviewStopCategory(j)
			if category != "claude_synthetic_quota" {
				t.Fatalf("hold category = %q", category)
			}
			proof, err := r.reviewUsageProof(t.Context(), dir, j, category, true)
			if fault == "" {
				if err != nil || proof != nil {
					t.Fatalf("auth recovery = %v, %v", proof, err)
				}
				if err := Save(dir, j); err != nil {
					t.Fatal(err)
				}
				provider := &exhaustedReviewProvider{err: errors.New("offline retry")}
				r.Provider = provider
				if err := r.Run(t.Context(), dir, j, ""); !errors.Is(err, ErrStopped) {
					t.Fatalf("retry: %v", err)
				}
				if len(provider.calls) != 1 || provider.calls[0].Model.Name != a.Model || provider.calls[0].Model.Provider != "claude" || j.ReviewSkip != nil {
					t.Fatal("did not retry configured reviewer without skipping")
				}
				return
			}
			if err == nil {
				t.Fatalf("%s accepted: %v", fault, proof)
			}
		})
	}
}
