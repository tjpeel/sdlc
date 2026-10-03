package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const dashboardUsage = "Usage: sdlc dashboard [--once | --watch | --json] [--run RUN_ID] [--logs] [--interval 2s]\nShows registered local runs across repositories, with attention items first.\nA terminal refreshes live by default; redirected output produces one snapshot.\n--run accepts a full ID or a unique prefix of at least six characters.\n--logs adds a bounded private output tail to a selected run.\nClosing the dashboard leaves run controllers working."

type dashboardOptions struct {
	once, watch, json, logs bool
	run                     string
	interval                time.Duration
}

func parseDashboardOptions(args []string) (dashboardOptions, error) {
	var options dashboardOptions
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.once, "once", false, "one status snapshot")
	flags.BoolVar(&options.watch, "watch", false, "refresh even with redirected output")
	flags.BoolVar(&options.json, "json", false, "one JSON snapshot")
	flags.BoolVar(&options.logs, "logs", false, "bounded private output tail")
	flags.StringVar(&options.run, "run", "", "selected run ID or unique prefix")
	flags.DurationVar(&options.interval, "interval", 2*time.Second, "refresh interval")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || (options.watch && (options.once || options.json)) || (options.logs && (options.run == "" || options.json)) {
		return options, fmt.Errorf("invalid dashboard options; run sdlc dashboard --help")
	}
	if options.run != "" && !regexp.MustCompile(`^[0-9a-f]{6,24}$`).MatchString(options.run) {
		return options, fmt.Errorf("run ID must be six to twenty-four lowercase hexadecimal characters")
	}
	if options.interval < 250*time.Millisecond || options.interval > time.Minute {
		return options, fmt.Errorf("refresh interval must be between 250ms and 1m")
	}
	return options, nil
}

func dashboardTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && file.Name() != os.DevNull
}

// dashboardCommand only reads installation metadata and run-local status/logs.
func dashboardCommand(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseDashboardOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardUsage)
		return err
	}
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	registry := runstatus.New(runtime.Directory)
	terminal := dashboardTerminal(output)
	watch := options.watch || (terminal && !options.once && !options.json)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		now := time.Now().UTC()
		views, err := registry.List(now)
		if err != nil {
			return err
		}
		views = dashboard.Ordered(views)
		if options.run != "" {
			selected, err := dashboard.Select(views, options.run)
			if err != nil {
				return err
			}
			views = []runstatus.View{selected}
		}
		if terminal && watch {
			if _, err := io.WriteString(output, "\x1b[H\x1b[2J"); err != nil {
				return err
			}
		}
		if options.json {
			return writeDashboardJSON(output, views, now)
		}
		if options.run != "" {
			err = dashboard.Detail(output, views[0], now, options.logs)
		} else {
			err = dashboard.List(output, views, now)
		}
		if err != nil || !watch {
			return err
		}
		timer := time.NewTimer(options.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// Exclude the full journal: it contains prompts and implementation instructions.
func writeDashboardJSON(output io.Writer, views []runstatus.View, now time.Time) error {
	type row struct {
		ID              string          `json:"id"`
		Root            string          `json:"root"`
		Reference       string          `json:"reference"`
		Ticket          string          `json:"ticket"`
		Directory       string          `json:"directory"`
		State           string          `json:"state"`
		Role            string          `json:"role"`
		Provider        string          `json:"provider"`
		Model           string          `json:"model"`
		Effort          string          `json:"effort"`
		Live            bool            `json:"live"`
		Stale           bool            `json:"stale"`
		Stopped         bool            `json:"stopped"`
		Available       bool            `json:"available"`
		NeedsAttention  bool            `json:"needs_attention"`
		StartedAt       time.Time       `json:"started_at"`
		UpdatedAt       time.Time       `json:"updated_at"`
		HeartbeatAt     time.Time       `json:"heartbeat_at"`
		LastActivityAt  time.Time       `json:"last_activity_at"`
		StopReason      string          `json:"stop_reason,omitempty"`
		Error           string          `json:"error,omitempty"`
		ActivityError   string          `json:"activity_error,omitempty"`
		CI              string          `json:"ci_status,omitempty"`
		PR              string          `json:"pr_url,omitempty"`
		Usage           runstatus.Usage `json:"usage"`
		WaitingProvider string          `json:"waiting_provider,omitempty"`
		WaitReason      string          `json:"wait_reason,omitempty"`
		WaitingSince    time.Time       `json:"waiting_since"`
	}
	rows := make([]row, 0, len(views))
	for _, v := range views {
		rows = append(rows, row{v.ID, v.Root, v.Reference, v.Ticket, v.Directory, v.State, v.Role, v.Provider, v.Model, v.Effort, v.Live, v.Stale, v.Stopped, v.Available, v.NeedsAttention, v.StartedAt, v.UpdatedAt, v.HeartbeatAt, v.LastActivityAt, v.StopReason, v.Error, v.ActivityError, v.CI.Status, v.PR.URL, v.Activity.Usage, v.Activity.WaitingProvider, v.Activity.WaitReason, v.Activity.WaitingSince})
	}
	return json.NewEncoder(output).Encode(struct {
		Version int       `json:"version"`
		At      time.Time `json:"at"`
		Runs    []row     `json:"runs"`
	}{1, now, rows})
}
