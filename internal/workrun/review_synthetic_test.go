package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
)

type syntheticClaudeDocker struct{ *liveNativeDocker }

func (d syntheticClaudeDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
		return json.Marshal([]map[string]any{{"Name": d.volume, "Driver": "local", "Options": map[string]string{}, "Labels": map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "provider-auth", "io.sdlc.installation": strings.Repeat("d", 32), "io.sdlc.provider": "claude"}}})
	}
	return d.liveNativeDocker.Output(ctx, args...)
}

func syntheticClaudeFixture(t *testing.T) (NativeProvider, *liveNativeDocker) {
	p, d := liveNativeFixture(t)
	d.volume = "sdlc-auth-" + strings.Repeat("d", 32) + "-claude"
	wrapped := syntheticClaudeDocker{d}
	p.Manager.Docker, p.Manager.Runtime.Docker = wrapped, wrapped
	return p, d
}

func syntheticClaudeFailureEvents(model, fault string) string {
	encode := func(event any) string { data, _ := json.Marshal(event); return string(data) + "\n" }
	data := encode(map[string]any{"type": "system", "subtype": "init", "model": model, "session_id": "fixture-review-session"})
	if fault == "no_init" {
		data = ""
	}
	if fault == "real_mismatch" {
		data += encode(map[string]any{"type": "assistant", "message": map[string]any{"id": "real-message", "model": "different-main-model"}})
	}
	if fault != "no_quota" {
		data += `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":2000000000}}` + "\n"
	}
	event := map[string]any{"type": "assistant", "error": "rate_limit", "message": map[string]any{"id": "error-message", "model": "<synthetic>", "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}, "content": []map[string]string{{"type": "text", "text": "Offline quota fixture."}}}}
	if fault == "no_error" {
		delete(event, "error")
	}
	data += encode(event)
	data += `{"type":"result","subtype":"success","is_error":true,"session_id":"fixture-review-session"}` + "\n"
	if fault != "no_finalization" {
		data += `{"type":"sdlc.native_finalization","provider":"claude","exit_code":1}` + "\n"
	}
	if fault == "malformed" {
		data += "{invalid trailing event"
	}
	return data
}

func TestSyntheticClaudeQuotaStreamSkipsOnlyCorroboratedUsageFailure(t *testing.T) {
	for _, fault := range []string{"", "no_init", "real_mismatch", "no_quota", "no_error", "no_finalization", "malformed"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.State, j.Outcome = "reviewing", testOutcome("implemented")
			p, d := syntheticClaudeFixture(t)
			events := syntheticClaudeFailureEvents(j.Plan.Roles.Review.Name, fault)
			d.stream = func(_ context.Context, out io.Writer, _ []string) error {
				if _, err := io.WriteString(out, events); err != nil {
					return err
				}
				return errors.New("offline native failure")
			}
			result, nativeErr := p.Execute(context.Background(), Session{Model: j.Plan.Roles.Review, Role: "review", Workspace: j.Workspace, Directory: filepath.Join(dir, "fixture-native"), Prompt: "Offline review fixture", Schema: reviewOutcomeSchema}, io.Discard, io.Discard)
			if nativeErr == nil {
				t.Fatal("native failure unexpectedly completed")
			}
			if fault == "" && (result.ReportedModel != j.Plan.Roles.Review.Name || nativeStopCategory(nativeErr) != "native_execution") {
				t.Fatal("synthetic error still failed model identity", result.ReportedModel, nativeErr)
			}
			r.Provider = &exhaustedReviewProvider{event: events, err: nativeErr, result: result, omitFinalization: true}
			err := r.Run(context.Background(), dir, j, "")
			if fault == "" {
				if err != nil || j.State != "ready" || j.ReviewSkip == nil || j.Outcome.Status != "implemented" {
					t.Fatal("corroborated synthetic quota failure did not skip honestly", err)
				}
			} else if err == nil || j.State == "ready" || j.ReviewSkip != nil {
				t.Fatal("uncorroborated or real failure bypassed hold", fault, err)
			}
		})
	}
}

func TestSavedSyntheticQuotaHoldRecoversWithoutProviderRetryOrMetricsRewrite(t *testing.T) {
	for _, fault := range []string{"", "no_init", "real_mismatch", "no_quota", "no_finalization", "no_error", "malformed", "missing_events"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.Attempt++
			j.State, j.ResumeState, j.StopReason, j.Outcome = "failed", "reviewing", "native claude headless session failed; inspect the private run log before retrying\n"+providerModelMismatchMessage, testOutcome("implemented")
			now := time.Now().UTC()
			reset := int64(2000000000)
			a := runusage.Attempt{Version: 1, RunID: j.ID, Attempt: j.Attempt, Role: "review", Provider: "claude", Model: j.Plan.Roles.Review.Name, Effort: j.Plan.Roles.Review.Effort, ImageID: j.ImageID, StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-time.Second), Outcome: "failed", OptimizerMode: "off", StopCategory: "operational", ReviewBoundary: reviewAttemptBoundary(j), Usage: runusage.Usage{ReportedModel: "<synthetic>", ModelMatches: false, NativeFinalization: &runusage.NativeFinalization{Provider: "claude", ExitCode: 1, ObservedAt: now.Add(-time.Second)}, RateLimits: map[string]runusage.RateLimit{"five_hour": {Source: "claude.rate_limit_event", Status: "rejected", ObservedAt: now.Add(-2 * time.Second), ResetsAt: &reset}}}}
			if err := runusage.SaveAttempt(dir, a); err != nil {
				t.Fatal(err)
			}
			metrics := filepath.Join(dir, "metrics", fmt.Sprintf("attempt-%06d-review.json", j.Attempt))
			before, _ := os.ReadFile(metrics)
			if fault != "missing_events" {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", j.Attempt)), []byte(syntheticClaudeFailureEvents(a.Model, fault)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			provider := &exhaustedReviewProvider{err: errors.New("provider retry must not occur")}
			r.Provider = provider
			attempt, rounds, checks := j.Attempt, j.Rounds, j.CheckAttempt
			err := r.Run(context.Background(), dir, j, "")
			if len(provider.calls) != 0 || j.Attempt != attempt || j.Rounds != rounds || j.CheckAttempt != checks {
				t.Fatal("saved hold burned new provider/check/repair capacity")
			}
			if fault == "" {
				if err != nil || j.State != "ready" || j.ReviewSkip == nil || j.ReviewSkip.ModelEvidenceSource != "retained_native_events" || j.Outcome.Status != "implemented" {
					t.Fatal("exact retained failure could not recover", err)
				}
			} else if err == nil || j.ReviewSkip != nil || j.State == "ready" {
				t.Fatal("saved real/ambiguous failure bypassed hold", fault, err)
			}
			after, _ := os.ReadFile(metrics)
			if string(after) != string(before) {
				t.Fatal("recovery rewrote original native usage evidence")
			}
		})
	}
}
