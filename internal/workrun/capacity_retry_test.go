package workrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
)

const capacityFailure = `{"type":"error","message":"Selected model is at capacity. Please try a different model."}
{"type":"turn.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}
`

func TestCapacityRetryResumesSameNativeSessionAndRetainsAttempts(t *testing.T) {
	for _, test := range []struct{ name, role, resume string }{
		{"initial implementation", "implementation", ""},
		{"existing repair", "implementation", testNative},
		{"independent review", "review", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			role := test.role
			dir, j := testRun(t)
			j.SessionID = test.resume
			if role == "review" {
				j.Plan.Roles = DefaultModels().Claude
			}
			p, docker := liveNativeFixture(t)
			var sessionDirectory, firstModel string
			docker.stream = func(_ context.Context, out io.Writer, args []string) error {
				mounts := nativeMounts(t, args)
				model := args[len(args)-4]
				if docker.streams == 1 {
					sessionDirectory, firstModel = mounts["/session"], model
					fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
					io.WriteString(out, capacityFailure)
					io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":1}\n")
					return errors.New("offline capacity failure")
				}
				if args[len(args)-2] != testNative || mounts["/session"] != sessionDirectory || model != firstModel {
					t.Fatal("retry changed session, storage or model")
				}
				prompt, err := os.ReadFile(mounts["/prompt.txt"])
				if err != nil || !strings.HasPrefix(string(prompt), "Continue the interrupted task") || len(prompt) > 1500 {
					t.Fatalf("retry did not use compact continuation: %s %v", prompt, err)
				}
				status := "implemented"
				if role == "review" {
					status = "reviewed"
				}
				io.WriteString(out, codexEvents(testOutcome(status))+"\n")
				io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":0}\n")
				return nil
			}
			var progress bytes.Buffer
			waits := 0
			r := Runner{Provider: p, Output: io.Discard, ProgressOutput: &progress, capacityWait: func(_ context.Context, delay time.Duration) error {
				waits++
				if delay != 30*time.Second {
					t.Fatal(delay)
				}
				return nil
			}}
			result, err := r.session(context.Background(), dir, j, role, j.Workspace, test.resume)
			if err != nil || result.SessionID != testNative || docker.streams != 2 || waits != 1 || j.Attempt != 2 {
				t.Fatalf("retry failed: %+v %v calls=%d waits=%d", result, err, docker.streams, waits)
			}
			if !strings.Contains(progress.String(), "waiting 30s before retry 1/3") {
				t.Fatal("wait was not visible")
			}
			firstRole := role
			if role == "implementation" && test.resume != "" {
				firstRole = "repair"
			}
			first, err := runusage.LoadAttempt(dir, j.ID, 1, firstRole)
			if err != nil || first.Outcome != "failed" || first.Usage.NativeStop.Category != "model_capacity" {
				t.Fatalf("lost failed attempt: %+v %v", first, err)
			}
			metricRole := role
			if role == "implementation" {
				metricRole = "repair"
			}
			second, err := runusage.LoadAttempt(dir, j.ID, 2, metricRole)
			if err != nil || second.Outcome != "completed" || !second.Resumed || second.SessionID != first.SessionID {
				t.Fatalf("lost resumed attempt: %+v %v", second, err)
			}
		})
	}
}

func TestCapacityRetryExhaustionCancellationAndOtherFailures(t *testing.T) {
	for _, fault := range []string{"exhausted", "cancelled", "usage", "mixed", "missing finalization", "malformed", "wrong model", "missing session", "cleanup"} {
		t.Run(fault, func(t *testing.T) {
			dir, j := testRun(t)
			p, docker := liveNativeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			docker.stream = func(_ context.Context, out io.Writer, _ []string) error {
				if fault != "missing session" {
					fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
				}
				if fault == "usage" {
					io.WriteString(out, rejectedCodexReview+"\n")
				} else {
					io.WriteString(out, capacityFailure)
				}
				switch fault {
				case "mixed":
					io.WriteString(out, "{\"type\":\"error\",\"message\":\"Login required\"}\n")
				case "malformed":
					io.WriteString(out, "not JSON\n")
				case "wrong model":
					io.WriteString(out, "{\"type\":\"turn.started\",\"model\":\"different-model\"}\n")
				}
				if fault != "missing finalization" && fault != "cleanup" {
					io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":1}\n")
				}
				return errors.New("offline native failure")
			}
			var delays []time.Duration
			r := Runner{Provider: p, Output: io.Discard, capacityWait: func(ctx context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				if fault == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			result, err := r.session(ctx, dir, j, "implementation", j.Workspace, "")
			if err == nil {
				t.Fatal("failure was hidden")
			}
			wantCalls, wantWaits := 1, 0
			if fault == "exhausted" {
				wantCalls, wantWaits = 4, 3
				if !strings.Contains(err.Error(), "after 3 retries") || result.SessionID != testNative {
					t.Fatal(err)
				}
				for i, delay := range delays {
					if delay != capacityRetryDelays[i] {
						t.Fatal(delays)
					}
				}
			}
			if fault == "cancelled" {
				wantWaits = 1
				if !errors.Is(err, context.Canceled) || j.SessionID != testNative {
					t.Fatalf("cancellation lost saved session: %v", err)
				}
			}
			if docker.streams != wantCalls || len(delays) != wantWaits {
				t.Fatalf("calls=%d waits=%v", docker.streams, delays)
			}
		})
	}
}

func TestCapacityWaitCanBeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(waitForCapacity(ctx, time.Hour), context.Canceled) {
		t.Fatal("wait ignored cancellation")
	}
}

func TestCapacityRetryKeepsReviewStorageForLaterInspectionContinuation(t *testing.T) {
	dir, j, r, _, workspace := continuationFixture(t)
	j.Plan.Roles = DefaultModels().Claude
	p, docker := liveNativeFixture(t)
	r.Provider = p
	r.capacityWait = func(context.Context, time.Duration) error { return nil }
	storage := ""
	docker.stream = func(_ context.Context, out io.Writer, args []string) error {
		mounts := nativeMounts(t, args)
		if storage == "" {
			storage = mounts["/session"]
		} else if storage != mounts["/session"] || args[len(args)-2] != testNative {
			t.Fatal("later inspection lost original native storage or thread")
		}
		if docker.streams == 1 {
			fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
			io.WriteString(out, capacityFailure)
			io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":1}\n")
			return errors.New("offline capacity")
		}
		outcome := testOutcome("reviewed")
		if docker.streams == 2 {
			outcome = unfinishedReview("Search tests")
		}
		io.WriteString(out, codexEvents(outcome)+"\n")
		io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":0}\n")
		return nil
	}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "reviewed" || docker.streams != 3 || j.ReviewContinuation != nil {
		t.Fatalf("review continuation failed: %+v %v calls=%d", result, err, docker.streams)
	}
	a, err := runusage.LoadAttempt(dir, j.ID, 2, "review")
	if err != nil || a.SessionStartAttempt != 1 {
		t.Fatalf("native storage origin not retained: %+v %v", a, err)
	}
}

func TestCapacityRetryPreservesExistingCIFeedbackGrant(t *testing.T) {
	dir, j, r, _, _ := ciFeedbackRun(t)
	p, docker := liveNativeFixture(t)
	r.Provider = p
	r.capacityWait = func(context.Context, time.Duration) error { return nil }
	docker.stream = func(_ context.Context, out io.Writer, args []string) error {
		mounts := nativeMounts(t, args)
		prompt, err := os.ReadFile(mounts["/prompt.txt"])
		if err != nil {
			t.Fatal(err)
		}
		if docker.streams == 1 {
			if !strings.Contains(string(prompt), "ExampleOptions.RequiredValue") {
				t.Fatal("initial feedback missing")
			}
			fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
			io.WriteString(out, capacityFailure)
			io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":1}\n")
			return errors.New("offline capacity")
		}
		if strings.Contains(string(prompt), "ExampleOptions.RequiredValue") || args[len(args)-2] != testNative {
			t.Fatal("retry replayed feedback or changed session")
		}
		io.WriteString(out, codexEvents(testOutcome("blocked"))+"\n")
		io.WriteString(out, "{\"type\":\"sdlc.native_finalization\",\"provider\":\"codex\",\"exit_code\":0}\n")
		return nil
	}
	err := r.Run(context.Background(), dir, j, "")
	if !errors.Is(err, ErrStopped) || docker.streams != 2 || j.Attempt != 14 || j.Rounds != 26 || j.CIRepairBudget.Used != 2 {
		t.Fatalf("retry changed repair grant: %v calls=%d attempt=%d", err, docker.streams, j.Attempt)
	}
	f := j.CIFeedbackRecoveries[0]
	if !f.Completed || f.DispatchAttempt != 14 || f.Text != "" {
		t.Fatalf("feedback completion lost: %+v", f)
	}
}
