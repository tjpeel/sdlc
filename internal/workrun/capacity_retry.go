package workrun

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/runprogress"
	"github.com/tjpeel/sdlc/internal/runusage"
)

var capacityRetryDelays = [...]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}

func nativeReviewDirectory(directory string, attempt runusage.Attempt) string {
	start := attempt.SessionStartAttempt
	if start == 0 {
		start = attempt.Attempt
	}
	return filepath.Join(directory, fmt.Sprintf("native-review-%d", start))
}

func waitForCapacity(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Retry only a cleanly finalized native capacity failure. Mixed errors, parser
// failures, usage limits and authentication/cleanup failures retain their holds.
func capacityRetryEligible(directory string, j *Journal, role, resume string, result SessionResult, err error) bool {
	if nativeStopCategory(err) != "native_execution" || !sessionIdentifier.MatchString(result.SessionID) || resume != "" && resume != result.SessionID {
		return false
	}
	metricRole := role
	if role == "implementation" && resume != "" {
		metricRole = "repair"
	}
	a, loadErr := runusage.LoadAttempt(directory, j.ID, j.Attempt, metricRole)
	if loadErr != nil || a.Provider != "codex" || a.Outcome != "failed" || a.SessionID != result.SessionID {
		return false
	}
	s, f := a.Usage.NativeStop, a.Usage.NativeFinalization
	return s != nil && s.Category == "model_capacity" && f != nil && f.Provider == "codex" && f.ExitCode == 1 && a.Usage.CompletedTurns == 0
}

func (runner Runner) session(ctx context.Context, directory string, j *Journal, role, workspace, resume string) (result SessionResult, resultErr error) {
	// Keep one progress recorder across attempts and the cancellable backoff.
	if runner.progress == nil {
		recorder, err := runprogress.Open(directory, runner.ProgressOutput)
		if err != nil {
			return result, err
		}
		runner.progress = recorder
		defer func() { resultErr = errors.Join(resultErr, recorder.Close()) }()
	}
	wait := runner.capacityWait
	if wait == nil {
		wait = waitForCapacity
	}
	retryDirectory := ""
	firstAttempt := j.Attempt + 1
	for retry := 0; ; retry++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result, resultErr = runner.sessionAttempt(ctx, directory, j, role, workspace, resume, retryDirectory, firstAttempt)
		if ctx.Err() != nil {
			return result, errors.Join(resultErr, ctx.Err())
		}
		if resultErr == nil || !capacityRetryEligible(directory, j, role, resume, result, resultErr) {
			return result, resultErr
		}
		if retry == len(capacityRetryDelays) {
			return result, fmt.Errorf("model is still at capacity after %d retries; saved run retained for ordinary resume: %w", retry, resultErr)
		}
		if role == "implementation" {
			j.SessionID = result.SessionID
			if err := runner.save(directory, j); err != nil {
				return result, errors.Join(resultErr, err)
			}
		}
		retryDirectory = filepath.Join(directory, "native-implementation")
		if role == "review" {
			start := firstAttempt
			if c := j.ReviewContinuation; c != nil {
				start = c.StartAttempt
			}
			retryDirectory = filepath.Join(directory, fmt.Sprintf("native-review-%d", start))
		}
		resume = result.SessionID
		delay := capacityRetryDelays[retry]
		if err := runner.progress.Record(runprogress.Event{RunID: j.ID, Source: "sdlc", Stage: j.State, Attempt: j.Attempt,
			Text: fmt.Sprintf("Selected model is at capacity; waiting %s before retry %d/%d in the same session.", delay, retry+1, len(capacityRetryDelays))}); err != nil {
			return result, errors.Join(resultErr, err)
		}
		if err := wait(ctx, delay); err != nil {
			return result, errors.Join(resultErr, err)
		}
	}
}
