package runusage

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"time"
)

// SubscriptionUsage brackets native provider work. Percentages describe the
// whole account; concurrent work can contribute to the observed change.
type SubscriptionUsage struct {
	Before  *SubscriptionSnapshot         `json:"before,omitempty"`
	After   *SubscriptionSnapshot         `json:"after,omitempty"`
	Windows map[string]SubscriptionChange `json:"windows,omitempty"`
}

type SubscriptionSnapshot struct {
	Source     string                        `json:"source"`
	ObservedAt time.Time                     `json:"observed_at"`
	Status     string                        `json:"status"`
	Reason     string                        `json:"reason,omitempty"`
	Windows    map[string]SubscriptionWindow `json:"windows,omitempty"`
}

type SubscriptionWindow struct {
	RemainingPercent   *float64 `json:"remaining_percent"`
	ResetsAt           *int64   `json:"resets_at,omitempty"`
	WindowDurationMins *int64   `json:"window_duration_mins,omitempty"`
}

type SubscriptionChange struct {
	BeforeRemainingPercent *float64 `json:"before_remaining_percent,omitempty"`
	AfterRemainingPercent  *float64 `json:"after_remaining_percent,omitempty"`
	UsedPercentagePoints   *float64 `json:"used_percentage_points,omitempty"`
	Status                 string   `json:"status"`
}

var subscriptionWindow = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}/(primary|secondary)$`)

func validateSubscriptionSnapshot(snapshot *SubscriptionSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.Source != "codex.app_server.rate_limits" || snapshot.ObservedAt.IsZero() || len(snapshot.Windows) > 32 {
		return fmt.Errorf("invalid subscription snapshot")
	}
	switch snapshot.Status {
	case "observed":
		if snapshot.Reason != "" || len(snapshot.Windows) == 0 {
			return fmt.Errorf("subscription snapshot lacks windows")
		}
	case "unavailable":
		if len(snapshot.Windows) != 0 {
			return fmt.Errorf("unavailable subscription snapshot has windows")
		}
		switch snapshot.Reason {
		case "timeout", "unsupported_client", "invalid_response", "native_error", "native_rejected", "missing_windows":
		default:
			return fmt.Errorf("invalid subscription snapshot reason")
		}
	default:
		return fmt.Errorf("invalid subscription snapshot status")
	}
	for name, window := range snapshot.Windows {
		if !subscriptionWindow.MatchString(name) || window.RemainingPercent == nil || math.IsNaN(*window.RemainingPercent) || math.IsInf(*window.RemainingPercent, 0) || *window.RemainingPercent < 0 || *window.RemainingPercent > 100 {
			return fmt.Errorf("invalid subscription percentage")
		}
		for _, value := range []*int64{window.ResetsAt, window.WindowDurationMins} {
			if value != nil && (*value <= 0 || *value > maxTokenCount) {
				return fmt.Errorf("invalid subscription window")
			}
		}
	}
	return nil
}

func subscriptionChanges(usage *SubscriptionUsage) map[string]SubscriptionChange {
	var changes map[string]SubscriptionChange
	if usage == nil {
		return changes
	}
	for _, snapshot := range []*SubscriptionSnapshot{usage.Before, usage.After} {
		if snapshot == nil {
			continue
		}
		for name := range snapshot.Windows {
			if changes == nil {
				changes = map[string]SubscriptionChange{}
			}
			changes[name] = SubscriptionChange{}
		}
	}
	for name := range changes {
		change := SubscriptionChange{Status: "missing_before"}
		before, beforeKnown := SubscriptionWindow{}, false
		after, afterKnown := SubscriptionWindow{}, false
		if usage.Before != nil {
			before, beforeKnown = usage.Before.Windows[name]
		}
		if usage.After != nil {
			after, afterKnown = usage.After.Windows[name]
		}
		if beforeKnown {
			value := *before.RemainingPercent
			change.BeforeRemainingPercent = &value
		}
		if afterKnown {
			value := *after.RemainingPercent
			change.AfterRemainingPercent = &value
		}
		switch {
		case !beforeKnown:
		case !afterKnown:
			change.Status = "missing_after"
		case before.ResetsAt == nil || after.ResetsAt == nil || before.WindowDurationMins == nil || after.WindowDurationMins == nil:
			change.Status = "window_unknown"
		case usage.Before.Source != usage.After.Source || *before.ResetsAt != *after.ResetsAt || *before.WindowDurationMins != *after.WindowDurationMins || usage.After.ObservedAt.Before(usage.Before.ObservedAt) || usage.After.ObservedAt.Unix() >= *after.ResetsAt || usage.Before.ObservedAt.Unix() >= *before.ResetsAt:
			change.Status = "window_changed"
		case *after.RemainingPercent > *before.RemainingPercent:
			change.Status = "allowance_increased"
		default:
			change.Status = "observed"
			value := *before.RemainingPercent - *after.RemainingPercent
			change.UsedPercentagePoints = &value
		}
		changes[name] = change
	}
	return changes
}

func validateSubscription(usage *SubscriptionUsage) error {
	if usage == nil {
		return nil
	}
	for _, snapshot := range []*SubscriptionSnapshot{usage.Before, usage.After} {
		if err := validateSubscriptionSnapshot(snapshot); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(usage.Windows, subscriptionChanges(usage)) {
		return fmt.Errorf("subscription change does not match snapshots")
	}
	return nil
}

func observeSubscription(line []byte, provider string, usage *Usage) bool {
	var event struct {
		Provider string                `json:"provider"`
		Phase    string                `json:"phase"`
		Snapshot *SubscriptionSnapshot `json:"snapshot"`
	}
	if json.Unmarshal(line, &event) != nil || event.Provider != provider || provider != "codex" || event.Snapshot == nil || validateSubscriptionSnapshot(event.Snapshot) != nil {
		return false
	}
	if event.Phase != "before" && event.Phase != "after" {
		return false
	}
	if usage.Subscription == nil {
		usage.Subscription = &SubscriptionUsage{}
	}
	if event.Phase == "before" {
		if usage.Subscription.Before == nil {
			usage.Subscription.Before = event.Snapshot
		}
	} else {
		usage.Subscription.After = event.Snapshot
	}
	usage.Subscription.Windows = subscriptionChanges(usage.Subscription)
	return true
}

func mergeSubscription(target map[string]*SubscriptionUsage, provider string, incoming *SubscriptionUsage) {
	target[provider] = combineSubscription(target[provider], incoming)
}

func combineSubscription(current, incoming *SubscriptionUsage) *SubscriptionUsage {
	if current == nil {
		current = &SubscriptionUsage{}
		if incoming != nil {
			current.Before = incoming.Before
		}
	}
	current.After = nil
	if incoming != nil {
		current.After = incoming.After
	}
	current.Windows = subscriptionChanges(current)
	return current
}
