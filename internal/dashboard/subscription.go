package dashboard

import (
	"fmt"
	"io"
	"sort"

	"github.com/tjpeel/sdlc/internal/runusage"
)

// Render retained measurements only; opening usage never contacts an account.
func WriteSubscriptions(output io.Writer, subscriptions map[string]*runusage.SubscriptionUsage) {
	providers := make([]string, 0, len(subscriptions))
	for provider := range subscriptions {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	percent := func(value *float64) string {
		if value == nil {
			return "unknown"
		}
		return fmt.Sprintf("%.1f%%", *value)
	}
	for _, provider := range providers {
		usage := subscriptions[provider]
		if usage == nil || len(usage.Windows) == 0 {
			status := func(snapshot *runusage.SubscriptionSnapshot) string {
				if snapshot == nil {
					return "not captured"
				}
				return SafeText(snapshot.Status + " (" + snapshot.Reason + ")")
			}
			before, after := (*runusage.SubscriptionSnapshot)(nil), (*runusage.SubscriptionSnapshot)(nil)
			if usage != nil {
				before, after = usage.Before, usage.After
			}
			fmt.Fprintf(output, " Subscription %s: before %s; after %s; used unknown.\n", SafeText(provider), status(before), status(after))
			continue
		}
		windows := make([]string, 0, len(usage.Windows))
		for name := range usage.Windows {
			windows = append(windows, name)
		}
		sort.Strings(windows)
		for _, name := range windows {
			change := usage.Windows[name]
			used := "unknown (" + SafeText(change.Status) + ")"
			if change.UsedPercentagePoints != nil {
				used = fmt.Sprintf("%.1f percentage points", *change.UsedPercentagePoints)
			}
			fmt.Fprintf(output, " Subscription %s %s: remaining %s before -> %s after; used %s.\n", SafeText(provider), SafeText(name), percent(change.BeforeRemainingPercent), percent(change.AfterRemainingPercent), used)
		}
	}
}
