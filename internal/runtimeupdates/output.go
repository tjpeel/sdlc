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

func summaryIdentity(value string) string {
	if index := strings.LastIndex(value, "@sha256:"); index >= 0 && len(value)-index == 72 {
		return value[:index+20]
	}
	return shortIdentity(value)
}

func primaryResult(result Result) bool {
	if result.Kind != "npm" {
		return true
	}
	if result.Track != "" {
		return false
	}
	switch result.Source {
	case "@openai/codex", "@anthropic-ai/claude-code", "npm", "yarn":
		return true
	}
	return false
}

// PrintSummary shows managed tools and images while counting bundled npm
// dependencies and Debian packages. Print retains the complete package report.
func (report Report) PrintSummary(output io.Writer) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "Dependency\tInstalled\tAvailable\tTrack\tStatus")
	updates, unavailable := 0, 0
	children, childUpdates, childUnavailable := 0, 0, 0
	var childErrors []string
	for _, result := range report.Results {
		if result.Status == Unavailable {
			unavailable++
		}
		if !primaryResult(result) {
			children++
			if result.Status == Update {
				childUpdates++
			}
			if result.Status == Unavailable {
				childUnavailable++
				if len(childErrors) < 5 {
					message := result.Name
					if result.Detail != "" {
						message += " (" + result.Detail + ")"
					}
					childErrors = append(childErrors, message)
				}
			}
			continue
		}
		track := result.Track
		if track == "" {
			track = "latest stable"
		}
		status := result.Status
		if result.Detail != "" {
			status += " (" + result.Detail + ")"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", result.Name, summaryIdentity(result.Installed), summaryIdentity(result.Candidate), track, status)
		switch result.Status {
		case Update, Changed, "tag changed":
			updates++
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if children > 0 {
		if report.Offline {
			if _, err := fmt.Fprintf(output, "Parent-managed npm packages: %d installed; upstream checks skipped.\n", children); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(output, "Parent-managed npm packages: %d checked; %d newer upstream versions; %d unavailable. Published parents control their installed versions.\n", children, childUpdates, childUnavailable); err != nil {
				return err
			}
			if err := printUnavailableExamples(output, "Parent-managed npm metadata unavailable", childUnavailable, childErrors); err != nil {
				return err
			}
		}
	}
	if report.Offline {
		_, err := fmt.Fprintf(output, "Debian packages: %d installed; upstream checks skipped (--offline). Use sdlc runtime status --all for package details.\n", report.PackageCount)
		return err
	}
	packageUpdates, packageUnavailable := 0, 0
	var packageErrors []string
	for _, item := range report.Packages {
		if item.Candidate == "" {
			packageUnavailable++
			if len(packageErrors) < 5 {
				packageErrors = append(packageErrors, item.Name+":"+item.Architecture)
			}
		} else if item.Update {
			packageUpdates++
		}
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
		if err := printUnavailableExamples(output, "Debian candidates unavailable", packageUnavailable, packageErrors); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(output, "Use sdlc runtime status --all for package details."); err != nil {
		return err
	}
	if report.Incomplete() {
		_, err := fmt.Fprintf(output, "Dependency check incomplete: %d component checks and %d package checks unavailable.\n", unavailable, packageUnavailable)
		return err
	}
	if updates+packageUpdates == 0 {
		_, err := fmt.Fprintln(output, "No direct tool, image or Debian updates found in the checked release tracks.")
		return err
	}
	_, err := fmt.Fprintf(output, "Tool, image or Debian updates found: %d. Preview with sdlc runtime update --dry-run; update dependencies and rebuild with sdlc runtime update.\n", updates+packageUpdates)
	return err
}

func printUnavailableExamples(output io.Writer, label string, count int, examples []string) error {
	if count == 0 {
		return nil
	}
	more := ""
	if count > len(examples) {
		more = fmt.Sprintf("; %d more (use --all)", count-len(examples))
	}
	_, err := fmt.Fprintf(output, "%s (%d): %s%s.\n", label, count, strings.Join(examples, "; "), more)
	return err
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
	_, err := fmt.Fprintf(output, "Updates or source changes found: %d. Preview with sdlc runtime update --dry-run; update dependencies and rebuild with sdlc runtime update.\n", updates+packageUpdates)
	return err
}
