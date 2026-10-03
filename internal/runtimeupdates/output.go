package runtimeupdates

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func shortIdentity(value string) string {
	if validSHA(value) {
		return value[:12]
	}
	if strings.HasPrefix(value, "sha256:") && len(value) == 71 {
		return value[:19]
	}
	if value == "" {
		return "-"
	}
	return value
}

// Print includes every component result and every Debian update or unavailable candidate.
// Unchanged Debian packages are counted rather than flooding a normal status display.
func (report Report) Print(output io.Writer) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "Dependency\tInstalled\tAvailable\tTrack\tStatus")
	updates, unavailable := 0, 0
	for _, result := range report.Results {
		track := result.Track
		if track == "" {
			track = "latest stable"
		}
		status := result.Status
		if result.Detail != "" {
			status += " (" + result.Detail + ")"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", result.Name, shortIdentity(result.Installed), shortIdentity(result.Candidate), track, status)
		switch result.Status {
		case Update, Changed, "tag changed":
			updates++
		case Unavailable:
			unavailable++
		}
	}
	packageUpdates, packageUnavailable := 0, 0
	for _, p := range report.Packages {
		if p.Candidate == "" {
			packageUnavailable++
			fmt.Fprintf(writer, "Debian %s:%s\t%s\t-\tbookworm\tunavailable (no repository candidate)\n", p.Name, p.Architecture, p.Installed)
		} else if p.Update {
			packageUpdates++
			fmt.Fprintf(writer, "Debian %s:%s\t%s\t%s\tbookworm\t%s\n", p.Name, p.Architecture, p.Installed, p.Candidate, Update)
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if report.Offline {
		_, err := fmt.Fprintf(output, "Debian packages: %d installed; upstream checks skipped (--offline).\n", report.PackageCount)
		return err
	}
	if report.PackageError != "" {
		packageUnavailable = report.PackageCount
		if _, err := fmt.Fprintf(output, "Debian packages: unavailable (%s).\n", report.PackageError); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "Debian packages: %d checked; %d updates; %d unavailable.\n", len(report.Packages), packageUpdates, packageUnavailable); err != nil {
			return err
		}
	}
	if report.Incomplete() {
		_, err := fmt.Fprintf(output, "Dependency check incomplete: %d component checks and %d package checks unavailable.\n", unavailable, packageUnavailable)
		return err
	}
	if updates+packageUpdates == 0 {
		_, err := fmt.Fprintln(output, "No updates found in the checked release tracks.")
		return err
	}
	_, err := fmt.Fprintf(output, "Updates or source changes found: %d. Review the source pins and rebuild with sdlc runtime build after updating them.\n", updates+packageUpdates)
	return err
}
