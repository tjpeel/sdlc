package workrun

import "time"

// CheckTiming observes one controller operation. Command duration includes its
// worker startup; cache/copy/fingerprint/cleanup intervals are recorded separately.
type CheckTiming struct {
	Kind       string `json:"kind"`
	Phase      string `json:"phase"`
	Attempt    int    `json:"attempt"`
	Command    int    `json:"command,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type TimingChecker interface {
	WithTimingObserver(func(CheckTiming)) Checker
}

func (checker DockerChecker) WithTimingObserver(observer func(CheckTiming)) Checker {
	checker.onTiming = observer
	return checker
}

func (checker DockerChecker) recordTiming(kind string, baseline *VerificationBaseline, command int, started time.Time) {
	if checker.onTiming == nil {
		return
	}
	phase := "candidate"
	if baseline != nil {
		phase = "baseline"
	}
	checker.onTiming(CheckTiming{Kind: kind, Phase: phase, Command: command, DurationMS: time.Since(started).Milliseconds()})
}

const maximumCheckTimings = 1024

func (timings *Timings) addCheckTiming(observation CheckTiming) {
	if len(timings.CheckOperations) >= maximumCheckTimings {
		timings.CheckOperations = timings.CheckOperations[1:]
		timings.DroppedCheckOperations++
	}
	timings.CheckOperations = append(timings.CheckOperations, observation)
}
