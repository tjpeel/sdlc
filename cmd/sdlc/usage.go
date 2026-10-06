package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runusage"
	"github.com/tjpeel/sdlc/internal/textview"
	"github.com/tjpeel/sdlc/internal/workrun"
)

type usageRow struct {
	outcomeRecorded bool
	checksRecorded  bool
	Timings         *workrun.Timings  `json:"timings,omitempty"`
	RunID           string            `json:"run_id"`
	State           string            `json:"state"`
	RepairRounds    int               `json:"repair_rounds"`
	CheckAttempts   int               `json:"check_attempts"`
	ChecksPassed    bool              `json:"checks_passed"`
	ReviewFindings  int               `json:"review_findings"`
	Metrics         *runusage.Summary `json:"metrics,omitempty"`
	MetricsError    string            `json:"metrics_error,omitempty"`
}

func usageDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days > 0 && days <= 365 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
		return 0, fmt.Errorf("--since requires 1-365 days or a positive duration up to 8760h")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 || duration > 365*24*time.Hour {
		return 0, fmt.Errorf("--since requires 1-365 days or a positive duration up to 8760h")
	}
	return duration, nil
}

// usageCommand reads private numeric records only. It never starts Docker or
// contacts providers, and its safe projection excludes native session IDs.
func usageCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("usage", flag.ContinueOnError)
	flags.SetOutput(output)
	since := flags.String("since", "7d", "include full records for runs updated within this duration")
	scope := flags.String("scope", "installation", "project or installation")
	run := flags.String("run", "", "select one exact registered run regardless of age")
	structured := flags.Bool("json", false, "numeric JSON summary, without transcripts or account identifiers")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*scope != "project" && *scope != "installation") {
		return fmt.Errorf("usage accepts --since, --scope project|installation, --run and --json")
	}
	duration, err := usageDuration(*since)
	if err != nil {
		return err
	}
	manager, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	root := ""
	if *scope == "project" {
		current, err := os.Getwd()
		if err != nil {
			return err
		}
		root, err = checkoutRoot(ctx, current)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	views, err := runstatus.New(manager.Directory).List(now)
	if err != nil {
		return err
	}
	rows := []usageRow{}
	for _, view := range views {
		if root != "" && !sameProjectRoot(view.Root, root) {
			continue
		}
		if *run != "" {
			if view.ID != *run {
				continue
			}
		} else if view.UpdatedAt.Before(now.Add(-duration)) {
			continue
		}
		row := usageRow{RunID: view.ID, State: view.State, Metrics: view.Metrics, MetricsError: view.MetricsError}
		if view.Journal != nil {
			row.outcomeRecorded = true
			row.checksRecorded = view.Journal.Evidence.Tree != ""
			row.Timings = view.Journal.Timings.Recorded()
			row.RepairRounds = view.Journal.Rounds
			row.CheckAttempts = view.Journal.CheckAttempt
			row.ChecksPassed = view.Journal.Evidence.Passed
			row.ReviewFindings = len(view.Journal.Outcome.Findings)
		}
		rows = append(rows, row)
	}
	if *run != "" && len(rows) == 0 {
		return fmt.Errorf("registered run is unavailable in the selected scope")
	}
	if *structured {
		return json.NewEncoder(output).Encode(struct {
			Version   int        `json:"version"`
			At        time.Time  `json:"at"`
			Selection string     `json:"selection"`
			Runs      []usageRow `json:"runs"`
		}{1, now, "Full recorded attempts of runs updated within --since; --run ignores age. Missing telemetry is unknown; dollar estimates do not measure subscription allowance.", rows})
	}
	return writeUsageRows(output, rows)
}

func writeUsageRows(output io.Writer, rows []usageRow) error {
	fmt.Fprintln(output, textview.Heading("Recorded usage"))
	if len(rows) == 0 {
		_, err := fmt.Fprintln(output, "No registered runs in the selected period.")
		return err
	}
	for _, row := range rows {
		fmt.Fprintf(output, "%s: %s\n", dashboard.SafeText(row.RunID), dashboard.SafeText(row.State))
		fmt.Fprintln(output, textview.Heading("Recorded measurements"))
		if row.outcomeRecorded {
			fmt.Fprintf(output, "Repairs %d | checks %d\n", row.RepairRounds, row.CheckAttempts)
		}
		if row.checksRecorded {
			result := "failed"
			if row.ChecksPassed {
				result = "passed"
			}
			fmt.Fprintln(output, "Check result: "+result)
		}
		if row.Timings != nil {
			fmt.Fprintf(output, " Observed controller %s; check worker %s; CI polling wait %s.\n", time.Duration(row.Timings.ControllerMS)*time.Millisecond, time.Duration(row.Timings.ChecksMS)*time.Millisecond, time.Duration(row.Timings.CIWaitMS)*time.Millisecond)
		}
		if s := row.Metrics; s != nil {
			if s.Attempts > 0 {
				fmt.Fprintf(output, " Attempts %d; elapsed %s (setup/queue/execution).\n", s.Attempts, time.Duration(s.ElapsedMS)*time.Millisecond)
			} else {
				fmt.Fprintln(output, "No recorded provider attempts.")
			}
			for _, group := range s.Groups {
				fmt.Fprintf(output, " %s %s (%s; optimizer %s): input %s, cache read %s, cache creation %s, output %s; measured %d/%d attempts.\n", dashboard.SafeText(group.Provider), dashboard.SafeText(group.Model), dashboard.SafeText(group.Role), dashboard.SafeText(group.OptimizerMode), usageCount(group.Tokens.InputTokens), usageCount(group.Tokens.CachedInputTokens), usageCount(group.Tokens.CacheCreationInputTokens), usageCount(group.Tokens.OutputTokens), group.MeasuredAttempts, group.Attempts)
			}
		}
		fmt.Fprintln(output, textview.Heading("Gaps and capacity observations"))
		if !row.outcomeRecorded {
			fmt.Fprintln(output, textview.Status("unverified", "Repair/check outcomes: unrecorded."))
		}
		if !row.checksRecorded {
			fmt.Fprintln(output, textview.Status("unverified", "Check result: not run (no check evidence recorded)."))
		}
		if row.Timings == nil {
			fmt.Fprintln(output, textview.Status("unverified", "Controller/check/CI timing: unrecorded."))
		}
		if row.MetricsError != "" {
			fmt.Fprintln(output, textview.Status("problem", "Recorded usage: "+dashboard.SafeText(row.MetricsError)))
		}
		if row.Metrics == nil {
			fmt.Fprintln(output, textview.Status("unverified", "Recorded usage unavailable; subscription capacity unknown."))
		} else {
			fmt.Fprintf(output, "Incomplete %d; missing %d; unknown resume baseline %d.\n", row.Metrics.Incomplete, row.Metrics.Missing, row.Metrics.UnknownBaseline)
			if len(row.Metrics.Groups) == 0 {
				fmt.Fprintln(output, textview.Status("unverified", "Subscription capacity: unknown (no native quota event recorded)."))
			}
			if row.Metrics.UnknownBaseline > 0 {
				fmt.Fprintln(output, textview.Status("unverified", "Some attempt baselines are unknown; missing measurements are not zero."))
			}
			for _, group := range row.Metrics.Groups {
				fmt.Fprintf(output, " %s %s (%s)\n", dashboard.SafeText(group.Provider), dashboard.SafeText(group.Model), dashboard.SafeText(group.Role))
				dashboard.WriteUsageSignals(output, group)
			}
		}
	}
	_, err := fmt.Fprintln(output, "Input categories retain native semantics: Codex input includes cache reads; Claude input excludes cache reads/writes. Compare matching models and outcomes; subscription capacity is separate.")
	return err
}

func usageCount(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatInt(*value, 10)
}
