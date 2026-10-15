package workrun

import (
	"bytes"
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

	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runusage"
)

type exhaustedReviewProvider struct {
	calls            []Session
	event            string
	err              error
	omitFinalization bool
	result           SessionResult
}

func (p *exhaustedReviewProvider) Status(context.Context, string) (string, error) {
	return "stored", nil
}
func (p *exhaustedReviewProvider) Execute(_ context.Context, s Session, stdout, _ io.Writer) (SessionResult, error) {
	p.calls = append(p.calls, s)
	_, _ = fmt.Fprintln(stdout, p.event)
	code := 0
	if nativeStopCategory(p.err) == "native_execution" {
		code = 1
	}
	if !p.omitFinalization {
		_, _ = fmt.Fprintf(stdout, "{\"type\":\"sdlc.native_finalization\",\"provider\":%q,\"exit_code\":%d}\n", s.Model.Provider, code)
	}
	return p.result, p.err
}

const rejectedClaudeReview = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792007400,"utilization":1}}`
const rejectedCodexReview = `{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again at the recorded reset time."}}`

func TestReviewerNativeUsageExhaustionReachesVisibleHumanCheckpoint(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.State, j.Outcome = "reviewing", testOutcome("implemented")
			if provider == "codex" {
				j.Plan.Roles = DefaultModels().Claude
				j.ReviewEffort = nil
			}
			p := &exhaustedReviewProvider{event: rejectedClaudeReview, err: errNativeIncomplete}
			if provider == "codex" {
				p.event = rejectedCodexReview
				p.err = &providerauth.NativeExecutionError{Provider: provider}
			}
			r.Provider = p
			var output bytes.Buffer
			r.Output = &output
			beforeAttempt, rounds, checks := j.Attempt, j.Rounds, j.CheckAttempt
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.State != "ready" || j.Outcome.Status != "implemented" || j.ReviewSkip == nil || j.ReviewSkip.Provider != provider || j.ReviewSkip.Publication != j.Publication || j.ReviewSkip.Attempt != beforeAttempt+1 || j.Rounds != rounds || j.CheckAttempt != checks || len(p.calls) != 1 || p.calls[0].Role != "review" || !strings.Contains(output.String(), "Independent review skipped") {
				t.Fatalf("exhaustion lost its honest checkpoint: %+v output=%s", j.ReviewSkip, output.String())
			}
			loaded, err := Load(dir)
			if err != nil || ReviewSkipNotice(loaded) == "" {
				t.Fatal("skip not retained", err)
			}
			if err := ValidatePRFeedbackResume(loaded); err != nil {
				t.Fatal("human feedback cannot resume skipped checkpoint", err)
			}
			if err := r.Run(context.Background(), dir, j, ""); err != nil || len(p.calls) != 1 || j.Attempt != beforeAttempt+1 {
				t.Fatal("ready resume spent another review attempt", err)
			}
		})
	}
}

func TestReviewUsageDoesNotBypassOtherHolds(t *testing.T) {
	for _, fault := range []string{"warning", "unknown", "missing_finalization", "authentication", "cleanup", "schema", "findings", "questions", "limitations", "local_review", "checks", "ci", "candidate"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, pub, _ := feedbackReadyRun(t)
			j.State, j.Outcome = "reviewing", testOutcome("implemented")
			p := &exhaustedReviewProvider{event: rejectedClaudeReview, err: errNativeIncomplete}
			switch fault {
			case "warning":
				p.event = strings.ReplaceAll(rejectedClaudeReview, `"rejected"`, `"allowed_warning"`)
			case "unknown":
				p.event = `{"type":"system","subtype":"init"}`
			case "missing_finalization":
				p.omitFinalization = true
			case "authentication":
				p.err = errors.New("native authentication unavailable")
			case "cleanup":
				p.err = errors.Join(errNativeIncomplete, errors.New("headless container cleanup failed"))
			case "schema":
				p.err = errors.New("provider did not return a valid structured handoff")
			case "findings":
				j.Outcome.Findings = reviewedFinding("P1").Findings
			case "questions":
				j.Outcome.Questions = []string{"Which contract applies?"}
			case "limitations":
				j.Outcome.Limitations = []string{"A required source is missing"}
			case "local_review":
				j.Outcome.LocalReview = false
			case "checks":
				j.Evidence.Passed = false
			case "ci":
				pub.statuses = []string{"failed"}
				pub.ciResults = []CIResult{{Status: "failed"}}
			case "candidate":
				r.Repository = &fakeRepository{revisions: []Revision{{Head: testHead, Tree: testTree, Clean: false}}}
			}
			r.Provider = p
			if err := r.Run(context.Background(), dir, j, ""); err == nil || j.State == "ready" || j.ReviewSkip != nil {
				t.Fatal("unrelated hold was treated as review usage exhaustion", fault)
			}
		})
	}
}

func TestLegacyReviewUsageHoldReusesCorroboratedAttempt(t *testing.T) {
	for _, fault := range []string{"", "auto_high", "auto_low", "wrong_effort", "no_snapshot", "other_boundary", "wrong_model", "wrong_category", "warning", "new_record_missing_marker", "safety_error", "retained_auth", "no_after"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.Attempt++
			j.State, j.ResumeState, j.StopReason, j.Outcome = "failed", "reviewing", errNativeIncomplete.Error(), testOutcome("implemented")
			if fault == "auto_high" || fault == "auto_low" || fault == "wrong_effort" {
				selection := assessReviewEffort(ReviewDiff{Sensitive: true})
				if fault == "auto_low" {
					selection = assessReviewEffort(ReviewDiff{Files: []ReviewDiffFile{{Path: "README.md", Added: 1}}})
				}
				j.ReviewEffort = &ReviewEffortDecision{Base: j.Publication.BaseSHA, Head: j.Publication.HeadSHA, Model: j.Plan.Roles.Review.Name, Selection: selection}
			}
			now := time.Now().UTC()
			a := runusage.Attempt{Version: 1, RunID: j.ID, Attempt: j.Attempt, Role: "review", Provider: j.Plan.Roles.Review.Provider, Model: j.Plan.Roles.Review.Name, Effort: selectedReviewModel(j).Effort, ImageID: j.ImageID, StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-time.Second), Outcome: "failed", OptimizerMode: "off", Usage: runusage.Usage{RateLimits: map[string]runusage.RateLimit{"five_hour": {Source: "claude.rate_limit_event", Status: "rejected", ObservedAt: now.Add(-2 * time.Second)}}}}
			snapshot := filepath.Join(dir, fmt.Sprintf("review-source-%d-fixture", j.Attempt))
			if fault != "no_snapshot" {
				if err := os.Mkdir(snapshot, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch fault {
			case "other_boundary":
				a.ReviewBoundary = reviewAttemptBoundary(j)
				a.ReviewBoundary.Head = testBase
			case "wrong_effort":
				a.Effort = "medium"
			case "wrong_model":
				a.Model = "different-review-model"
			case "wrong_category":
				a.StopCategory = "operational"
			case "warning":
				rate := a.Usage.RateLimits["five_hour"]
				rate.Status = "allowed_warning"
				a.Usage.RateLimits["five_hour"] = rate
			}
			a.Usage.Subscription = &runusage.SubscriptionUsage{After: &runusage.SubscriptionSnapshot{Source: "claude.control.get_usage", Status: "unavailable", Reason: "invalid_response", ObservedAt: now.Add(-time.Second)}}
			if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("native-review-%d", j.Attempt), a.Provider), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("diagnostics-%d.log", j.Attempt)), nil, 0600); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "new_record_missing_marker":
				a.ReviewBoundary = reviewAttemptBoundary(j)
				a.StopCategory = "native_incomplete"
			case "safety_error":
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("diagnostics-%d.log", j.Attempt)), []byte("SDLC could not complete the native headless session safely."), 0600); err != nil {
					t.Fatal(err)
				}
			case "retained_auth":
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("native-review-%d", j.Attempt), a.Provider, ".native-auth-base.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "no_after":
				a.Usage.Subscription = nil
			}
			if err := runusage.SaveAttempt(dir, a); err != nil {
				t.Fatal(err)
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			p := &exhaustedReviewProvider{err: errors.New("ordinary review failure")}
			r.Provider = p
			attempt, checks, rounds := j.Attempt, j.CheckAttempt, j.Rounds
			err := r.Run(context.Background(), dir, j, "")
			if fault == "" || fault == "auto_high" || fault == "auto_low" {
				if err != nil || j.State != "ready" || j.ReviewSkip == nil || len(p.calls) != 0 || j.Attempt != attempt || j.CheckAttempt != checks || j.Rounds != rounds {
					t.Fatal("corroborated legacy stop retried native review", err)
				}
				if fault == "auto_high" || fault == "auto_low" {
					// A later published revision must not reinterpret this historical skip's effort.
					j.State, j.Outcome = "ci", testOutcome("implemented")
					j.Publication.HeadSHA = strings.Repeat("d", 40)
					j.ReviewEffort = &ReviewEffortDecision{Base: j.Publication.BaseSHA, Head: j.Publication.HeadSHA, Model: j.Plan.Roles.Review.Name, Selection: assessReviewEffort(ReviewDiff{})}
					if err := validateReviewSkip(j); err != nil {
						t.Fatal("historical effort reinterpreted", err)
					}
					if ReviewSkipNotice(*j) != "" {
						t.Fatal("old skip applied to unrelated head")
					}
				}

			} else if err == nil || j.ReviewSkip != nil || j.State == "ready" {
				t.Fatal("uncorroborated legacy stop bypassed hold", fault)
			}
		})
	}
}

func TestReviewPacketKeepsUsefulContextAndOmitsImplementationSettings(t *testing.T) {
	_, j := testRun(t)
	j.Plan.PRBody = strings.Repeat("implementation-body-marker ", 1000)
	j.Plan.GitHubProfile, j.Plan.SigningProfile, j.Plan.Root = "account-setting-marker", "signing-setting-marker", "host-path-marker"
	j.Plan.Inputs = []Input{{Path: "tickets/selected.md", SHA256: strings.Repeat("a", 64)}}
	j.Plan.CheckInputs = []Input{{Path: "tests/private.env", SHA256: strings.Repeat("b", 64)}}
	j.CI = CIResult{Status: "passed"}
	j.Evidence = CheckEvidence{Head: testHead, Tree: testTree, Passed: true, Commands: [][]string{{"go", "test", "./..."}}}
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: testBase, HeadSHA: testHead}
	old := generalFeedback("comment:1", "superseded-comment-marker")
	current := old
	current.Body = "current-comment-marker"
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Entries: []PRFeedbackEntry{{Comment: old}, {Comment: current, Assessment: &PRFeedbackAssessment{Reason: "current-rationale-marker"}}}}
	j.ReviewHumanInput = &ReviewHumanInput{Questions: []string{"recorded-question-marker"}, Answer: "human-answer-marker"}
	prompt := (Runner{}).prompt(*j, "review")
	for _, want := range []string{j.Plan.Reference, j.Plan.Ticket, "tickets/selected.md", strings.Repeat("b", 64), testHead, testTree, "current-comment-marker", "current-rationale-marker", "human-answer-marker", "full Git history", "complete diff", `"ci"`, `"status": "passed"`} {
		if !strings.Contains(prompt, want) {
			t.Fatal("useful review context omitted", want)
		}
	}
	for _, excluded := range []string{"implementation-body-marker", "account-setting-marker", "signing-setting-marker", "host-path-marker", "superseded-comment-marker"} {
		if strings.Contains(prompt, excluded) {
			t.Fatal("review repeated irrelevant launch/history metadata", excluded)
		}
	}
	var schema struct {
		Required   []string
		Properties map[string]json.RawMessage
	}
	if err := json.Unmarshal([]byte(reviewOutcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pr_title", "pr_body", "pr_feedback_assessments", "diagnostic_requests", "verification_requests", "local_review"} {
		if _, ok := schema.Properties[field]; ok {
			t.Fatal("implementation-only review schema field", field)
		}
	}
	if len(reviewOutcomeSchema) >= len(outcomeSchema)/2 {
		t.Fatal("review schema did not reduce handoff overhead")
	}
}

func TestNativeReviewQuotaDoesNotHideMalformedStream(t *testing.T) {
	provider, docker := liveNativeFixture(t)
	docker.stream = func(_ context.Context, out io.Writer, _ []string) error {
		if _, err := fmt.Fprintln(out, rejectedCodexReview); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "invalid event after quota rejection")
		return err
	}
	dir, j := testRun(t)
	_, err := provider.Execute(context.Background(), Session{Model: j.Plan.Roles.Implementation, Role: "review", Workspace: j.Workspace, Directory: filepath.Join(dir, "native-review"), Prompt: "offline fixture", Schema: reviewOutcomeSchema}, io.Discard, io.Discard)
	if err == nil || nativeStopCategory(err) != "operational" {
		t.Fatal("malformed stream became a quota-skippable native failure", err)
	}
}

func TestCodexTopLevelUsageErrorRemainsNativeCompletionFailure(t *testing.T) {
	w := eventWriter{provider: "codex"}
	if _, err := w.Write([]byte(`{"type":"error","message":"You’ve hit your usage limit. Wait for reset."}` + "\n")); err != nil {
		t.Fatal("valid official fatal event rejected as malformed", err)
	}
	if err := w.finish("review"); err != errNativeIncomplete {
		t.Fatal("fatal event lost native failure category", err)
	}
}

func TestReviewSpecificHandoffDoesNotRequireImplementationFields(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"status": "reviewed", "summary": "No actionable findings", "questions": []string{}, "findings": []Finding{}, "limitations": []string{}, "repository_requests": []CompanionRepositoryRequest{}, "recommendations": []Finding{}, "review_coverage": coverage("full")})
	out := string(raw)
	w := eventWriter{provider: "claude"}
	data := `{"type":"result","subtype":"success","session_id":"` + testNative + `","structured_output":` + out + `}`
	_, _ = w.Write([]byte(data + "\n"))
	if err := w.finish("review"); err != nil {
		t.Fatal("review-specific contract rejected", err)
	}
	if w.result.Outcome.Status != "reviewed" || w.result.Outcome.LocalReview || w.result.Outcome.PRBody != "" {
		t.Fatal("review synthesized implementation work")
	}
}

func TestNativeQuotaAfterStructuredFindingsRetainsKnownWorkWithoutApproval(t *testing.T) {
	provider, docker := liveNativeFixture(t)
	out := reviewedFinding("P1")
	data, _ := json.Marshal(out)
	docker.stream = func(_ context.Context, stdout io.Writer, _ []string) error {
		event, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": string(data)}})
		_, _ = fmt.Fprintln(stdout, string(event))
		_, _ = fmt.Fprintln(stdout, rejectedCodexReview)
		_, _ = fmt.Fprintln(stdout, `{"type":"sdlc.native_finalization","provider":"codex","exit_code":1}`)
		return errors.New("offline native process failure")
	}
	dir, j, r, _, _ := feedbackReadyRun(t)
	j.State, j.Outcome, j.Plan.Roles = "reviewing", testOutcome("implemented"), DefaultModels().Claude
	j.ReviewEffort = nil
	result, err := provider.Execute(context.Background(), Session{Model: j.Plan.Roles.Review, Role: "review", Workspace: j.Workspace, Directory: filepath.Join(dir, "native-stream"), Prompt: "offline fixture", Schema: reviewOutcomeSchema}, io.Discard, io.Discard)
	if err == nil || result.ReviewInterruption == nil || len(result.ReviewInterruption.Findings) != 1 {
		t.Fatal("failed native stream discarded valid known findings", err)
	}
	r.Provider = &exhaustedReviewProvider{event: rejectedCodexReview, err: err, result: result}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || j.ReviewSkip != nil || j.Outcome.Status != "implemented" || j.ReviewInterruption == nil {
		t.Fatal("quota bypassed a known unresolved finding", err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.ReviewInterruption == nil || loaded.ReviewInterruption.Publication != j.Publication || loaded.ReviewInterruption.Findings[0].Scenario != out.Findings[0].Scenario {
		t.Fatal("interrupted finding was not durably actionable", err)
	}
}
