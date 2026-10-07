package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/storage"
	"github.com/tjpeel/sdlc/internal/textview"
)

const storageUsage = "Usage: sdlc storage [status] [--older-than DAYS] [--json]\n       sdlc storage purge (--run RUN_ID | --all) [--yes | --dry-run]\nRead-only scan of the current project's .sdlc directory.\nShows logical regular-file bytes, categories and retained run ages.\n--older-than filters run rows; directory/category totals always cover the full scan.\nNo files are removed or run controllers changed."

func storageBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= 1024
		if value < 1024 || unit == "TiB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", size)
}
func storageCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "purge" {
		return storagePurgeCommand(ctx, args[1:], out)
	}
	if len(args) > 0 && args[0] == "status" {
		args = args[1:]
	}
	flags := flag.NewFlagSet("storage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	days := flags.Int("older-than", -1, "filter runs older than this number of days (0-36500)")
	structured := flags.Bool("json", false, "numeric JSON report without artifact contents")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, storageUsage)
		return err
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("storage accepts status, --older-than DAYS and --json")
	}
	var filter *int
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "older-than" {
			filter = days
		}
	})
	if filter != nil && (*days < 0 || *days > 36500) {
		return fmt.Errorf("--older-than requires 0-36500 days")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := checkoutRoot(ctx, cwd)
	if err != nil {
		return err
	}
	report, err := storage.Scan(ctx, root, storage.Options{Now: time.Now().UTC(), OlderThanDays: filter})
	if err != nil {
		return err
	}
	if *structured {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(report)
	} else {
		err = writeStorageReport(out, report)
	}
	if err != nil {
		return err
	}
	if !report.Complete {
		return fmt.Errorf("storage scan incomplete: %d paths could not be measured or read", len(report.Errors))
	}
	return nil
}
func writeStorageReport(out io.Writer, report storage.Report) error {
	var b strings.Builder
	fmt.Fprintln(&b, textview.Heading("Retained project storage"))
	fmt.Fprintln(&b, "Directory: "+dashboard.SafeText(report.Directory))
	if !report.Exists {
		fmt.Fprintln(&b, "No .sdlc directory; no retained project storage.")
		_, err := io.WriteString(out, b.String())
		return err
	}
	label := "Measured"
	if !report.Complete {
		label = "Partial measurement"
	}
	fmt.Fprintf(&b, "%s: %s (%d logical bytes), %d regular files, %d retained runs\n", label, storageBytes(report.Totals.Bytes), report.Totals.Bytes, report.Totals.Files, report.TotalRuns)
	fmt.Fprintln(&b, "Counts regular-file paths; hard links count per path. Symlinks, Git/authentication state and other filesystem devices are excluded.")
	fmt.Fprintln(&b, "Age uses the newest measured artifact modification time or saved checkpoint/activity timestamp; recent heartbeat is not proof a controller is running.")
	fmt.Fprintln(&b, "\n"+textview.Heading("Storage categories"))
	for _, category := range report.Categories {
		fmt.Fprintf(&b, "%-23s %10s  %d files\n", category.Name, storageBytes(category.Bytes), category.Files)
	}
	fmt.Fprintln(&b, "\n"+textview.Heading("Retained runs; oldest activity first"))
	if report.OlderThanDays != nil {
		fmt.Fprintf(&b, "Filter: older than %d days. %d/%d runs match; %s across matching runs. Full totals above are unchanged.\n", *report.OlderThanDays, len(report.Runs), report.TotalRuns, storageBytes(report.MatchingRuns.Bytes))
	}
	if len(report.Runs) == 0 {
		fmt.Fprintln(&b, "No retained runs match this view.")
	}
	for _, run := range report.Runs {
		age, updated := "unknown", "unknown"
		if !run.LastActivity.IsZero() {
			age = fmt.Sprintf("%.1f days", run.AgeDays)
			updated = run.LastActivity.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "\n%s  %s  %d files  age %s\n", dashboard.SafeText(run.ID), storageBytes(run.Bytes), run.Files, age)
		fmt.Fprintf(&b, "  %s / %s | %s | %s | controller: %s\n", dashboard.SafeText(run.Reference), dashboard.SafeText(run.Ticket), dashboard.SafeText(run.State), run.Retention, run.Controller)
		fmt.Fprintf(&b, "  Last activity: %s\n  Directory: %s\n", updated, dashboard.SafeText(run.Path))
		for _, category := range run.Categories {
			fmt.Fprintf(&b, "  %-23s %10s\n", category.Name, storageBytes(category.Bytes))
		}
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintln(&b, "\n"+textview.Heading("Excluded paths"))
		for _, item := range report.Skipped {
			fmt.Fprintf(&b, "%s: %s\n", dashboard.SafeText(item.Path), item.Reason)
		}
	}
	if len(report.Errors) > 0 {
		fmt.Fprintln(&b, "\n"+textview.Heading("Scan needs attention"))
		for _, item := range report.Errors {
			fmt.Fprintf(&b, "%s: %s\n", dashboard.SafeText(item.Path), dashboard.SafeText(item.Reason))
		}
	}
	fmt.Fprintln(&b, "\nRead-only inspection. Nothing deleted; age alone does not make a run safe to remove.")
	_, err := io.WriteString(out, b.String())
	return err
}
