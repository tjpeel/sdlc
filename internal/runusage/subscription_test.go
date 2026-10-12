package runusage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func quotaEvent(phase string, remaining float64, reset int64, observed time.Time) string {
	return fmt.Sprintf(`{"type":"sdlc.subscription_snapshot","provider":"codex","phase":%q,"snapshot":{"source":"codex.app_server.rate_limits","status":"observed","observed_at":%q,"windows":{"codex/primary":{"remaining_percent":%v,"resets_at":%v,"window_duration_mins":300}}}}`, phase, observed.Format(time.RFC3339), remaining, reset)
}

func TestSubscriptionRemainingAndPercentagePoints(t *testing.T) {
	o := NewObserver("codex", "example-model")
	start := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	reset := start.Add(5 * time.Hour).Unix()
	read(o, quotaEvent("before", 80, reset, start))
	read(o, `{"type":"thread.started","thread_id":"one"}`)
	read(o, quotaEvent("after", 75, reset, start.Add(time.Hour)))
	value := o.Snapshot().Subscription
	change := value.Windows["codex/primary"]
	if change.Status != "observed" || *change.BeforeRemainingPercent != 80 || *change.AfterRemainingPercent != 75 || *change.UsedPercentagePoints != 5 {
		t.Fatalf("wrong percentage-point change: %+v", change)
	}
	if err := validateSubscription(value); err != nil {
		t.Fatal(err)
	}
	if o.Timing().Events != 1 {
		t.Fatal("quota probes counted as inference events")
	}
	*value.Before.Windows["codex/primary"].RemainingPercent = 1
	if *o.Snapshot().Subscription.Before.Windows["codex/primary"].RemainingPercent != 80 {
		t.Fatal("snapshot aliases observed data")
	}
}

func TestSubscriptionChangeRequiresComparableWindows(t *testing.T) {
	start := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	reset := start.Add(5 * time.Hour).Unix()
	for _, test := range []struct {
		name, before, after, status string
	}{
		{"reset", quotaEvent("before", 80, reset, start), quotaEvent("after", 95, reset+18000, start.Add(time.Hour)), "window_changed"},
		{"increase", quotaEvent("before", 80, reset, start), quotaEvent("after", 90, reset, start.Add(time.Hour)), "allowance_increased"},
		{"missing before", "", quotaEvent("after", 75, reset, start.Add(time.Hour)), "missing_before"},
		{"missing after", quotaEvent("before", 80, reset, start), "", "missing_after"},
		{"expired window", quotaEvent("before", 80, reset, start), quotaEvent("after", 75, reset, start.Add(6*time.Hour)), "window_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := NewObserver("codex", "example-model")
			read(o, test.before)
			read(o, test.after)
			change := o.Snapshot().Subscription.Windows["codex/primary"]
			if change.Status != test.status || change.UsedPercentagePoints != nil {
				t.Fatalf("incomparable quota became usage: %+v", change)
			}
		})
	}
}

func TestQuotaProbeDurationExcludedFromNativeTiming(t *testing.T) {
	clock := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	o := NewObserver("codex", "example-model")
	o.now = func() time.Time { return clock }
	clock = clock.Add(4 * time.Second)
	read(o, quotaEvent("before", 80, clock.Add(time.Hour).Unix(), clock))
	read(o, `{"type":"sdlc.native_work_boundary","provider":"codex","phase":"start"}`)
	clock = clock.Add(time.Second)
	read(o, `{"type":"thread.started","thread_id":"one"}`)
	clock = clock.Add(time.Second)
	read(o, `{"type":"sdlc.native_work_boundary","provider":"codex","phase":"end"}`)
	clock = clock.Add(4 * time.Second)
	read(o, quotaEvent("after", 75, clock.Add(time.Hour).Unix(), clock))
	o.Finish()
	timing := o.Timing()
	if timing.Events != 1 || timing.FirstEventMS == nil || *timing.FirstEventMS != 1000 || timing.FinalSilenceMS == nil || *timing.FinalSilenceMS != 1000 {
		t.Fatalf("quota probes inflated native timing: %+v", timing)
	}
}

func TestRunSubscriptionUsesFirstAndLastBoundariesAcrossRoles(t *testing.T) {
	for _, missingStart := range []bool{false, true} {
		directory := dir(t)
		start := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
		reset := start.Add(5 * time.Hour).Unix()
		for index, values := range [][2]float64{{80, 70}, {70, 60}} {
			record := attempt(index+1, []string{"implementation", "repair"}[index], "session", 100)
			record.Model = []string{"first-model", "second-model"}[index]
			observer := NewObserver("codex", record.Model)
			if !(missingStart && index == 0) {
				read(observer, quotaEvent("before", values[0], reset, start.Add(time.Duration(index)*time.Minute)))
			}
			read(observer, quotaEvent("after", values[1], reset, start.Add(time.Duration(index+1)*time.Minute)))
			record.Usage.Subscription = observer.Snapshot().Subscription
			if err := SaveAttempt(directory, record); err != nil {
				t.Fatal(err)
			}
		}
		summary, err := LoadSummary(directory, "run-one", 2)
		if err != nil {
			t.Fatal(err)
		}
		change := summary.Subscriptions["codex"].Windows["codex/primary"]
		if missingStart {
			if change.Status != "missing_before" || change.UsedPercentagePoints != nil {
				t.Fatal("later baseline substituted for missing run start")
			}
		} else if change.UsedPercentagePoints == nil || *change.UsedPercentagePoints != 20 {
			t.Fatalf("run did not use first start/last end: %+v", change)
		}
	}
}

func TestMalformedQuotaCannotBecomeZeroRemaining(t *testing.T) {
	start := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	valid := quotaEvent("before", 80, start.Add(time.Hour).Unix(), start)
	for _, invalid := range []string{
		strings.Replace(valid, `"remaining_percent":80,`, "", 1),
		strings.Replace(valid, `"remaining_percent":80`, `"remaining_percent":null`, 1),
		strings.Replace(valid, `"remaining_percent":80`, `"remaining_percent":101`, 1),
		strings.Replace(valid, `"provider":"codex"`, `"provider":"claude"`, 1),
		strings.Replace(valid, "codex.app_server.rate_limits", "private-source", 1),
	} {
		o := NewObserver("codex", "example-model")
		read(o, invalid)
		if o.Snapshot().Subscription != nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	var snapshot SubscriptionSnapshot
	if err := json.Unmarshal([]byte(`{"source":"codex.app_server.rate_limits","observed_at":"2026-10-12T10:00:00Z","status":"unavailable","reason":"native_error"}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := validateSubscriptionSnapshot(&snapshot); err != nil {
		t.Fatal(err)
	}
}
