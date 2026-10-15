package dashboard

import (
	"fmt"
	"io"
	"sort"
	"strings"

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

// WriteSubscriptionOverview omits unavailable percentages, without treating them as zero.
func WriteSubscriptionOverview(output io.Writer, subscriptions map[string]*runusage.SubscriptionUsage) {
	providers := make([]string, 0, len(subscriptions))
	for p := range subscriptions {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	unavailable := []string{}
	for _, p := range providers {
		u := subscriptions[p]
		if u == nil || len(u.Windows) == 0 {
			unavailable = append(unavailable, p)
			continue
		}
		windows := make([]string, 0, len(u.Windows))
		for w := range u.Windows {
			windows = append(windows, w)
		}
		sort.Strings(windows)
		for _, name := range windows {
			w := u.Windows[name]
			fmt.Fprintf(output, "  Subscription %s %s:", SafeText(p), SafeText(name))
			if w.AfterRemainingPercent != nil {
				fmt.Fprintf(output, " %.1f%% remaining", *w.AfterRemainingPercent)
			} else {
				fmt.Fprint(output, " remaining allowance unavailable")
			}
			if w.UsedPercentagePoints != nil {
				fmt.Fprintf(output, "; %.1f percentage points used", *w.UsedPercentagePoints)
			} else {
				fmt.Fprint(output, "; change unavailable")
			}
			fmt.Fprintln(output, ".")
		}
	}
	if len(unavailable) > 0 {
		fmt.Fprintf(output, "  Subscription measurements unavailable: %s.\n", SafeText(strings.Join(unavailable, ", ")))
	}
}
