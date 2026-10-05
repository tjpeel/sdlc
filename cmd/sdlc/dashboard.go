package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/notify"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runusage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const dashboardUsage = "Usage: sdlc dashboard [--once | --watch | --json] [--page N] [--run RUN_ID] [--logs] [--interval 2s]\n  [--notify off|desktop|bell] [--sound]\nHistory: sdlc dashboard forget --run RUN_ID\nExport: sdlc dashboard export --run RUN_ID --to PRIVATE_DIRECTORY\nShows at most ten runs per page: questions, problems, queued/running work, then completed work.\nA terminal refreshes live by default; redirected output produces one snapshot.\nIn a live terminal: n Enter = next page, p Enter = previous page, q Enter = close.\n--run accepts a full ID or a unique prefix of at least six characters.\n--logs adds a bounded private output tail to a selected run.\nforget removes a stopped run from the dashboard and retains all saved work.\nNotifications are optional and report fixed messages without private work details.\nClosing the dashboard leaves run controllers working."

type dashboardOptions struct {
	once, watch, json, logs bool
	run                     string
	interval                time.Duration
	page                    int
	notifications           notify.Options
	scope                   string
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
	flags.StringVar(&options.scope, "scope", "installation", "project or installation: filter history before paging")
	flags.IntVar(&options.page, "page", 1, "history page, ten runs per page")
	flags.StringVar(&options.notifications.Mode, "notify", "off", "local notifications: off, desktop or bell")
	flags.BoolVar(&options.notifications.Sound, "sound", false, "desktop notification sound")
	flags.DurationVar(&options.interval, "interval", 2*time.Second, "refresh interval")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || (options.watch && (options.once || options.json)) || (options.logs && (options.run == "" || options.json)) {
		return options, fmt.Errorf("invalid dashboard options; run sdlc dashboard --help")
	}
	if options.scope != "project" && options.scope != "installation" {
		return options, fmt.Errorf("--scope must be project or installation")
	}
	if options.run != "" && !regexp.MustCompile(`^[0-9a-f]{6,24}$`).MatchString(options.run) {
		return options, fmt.Errorf("run ID must be six to twenty-four lowercase hexadecimal characters")
	}
	if options.interval < 250*time.Millisecond || options.interval > time.Minute {
		return options, fmt.Errorf("refresh interval must be between 250ms and 1m")
	}
	if options.page < 1 || (options.run != "" && options.page != 1) {
		return options, fmt.Errorf("page must be positive and only applies to the overview")
	}
	if err := options.notifications.Validate(); err != nil {
		return options, err
	}
	if options.notifications.Mode != "off" && (options.once || options.json) {
		return options, fmt.Errorf("notifications require a watching dashboard")
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

// The overview only reads local metadata. Forget is an explicit index mutation.
func dashboardCommand(ctx context.Context, args []string, output io.Writer) error {
	return dashboardCommandWithNotifications(ctx, args, output, notify.New)
}

func dashboardCommandWithNotifications(ctx context.Context, args []string, output io.Writer, newSender func(notify.Options, io.Writer) (notify.Sender, error)) error {
	if len(args) > 0 && args[0] == "export" {
		return dashboardExportCommand(ctx, args[1:], output)
	}
	if len(args) > 0 && (args[0] == "forget" || args[0] == "remove") {
		return dashboardForgetCommand(ctx, args[1:], output)
	}
	options, err := parseDashboardOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardUsage+"\nScope: --scope project|installation (external CLI defaults to installation; shell defaults to project).")
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
	var projectRoot string
	if options.scope == "project" {
		command := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
		root, err := command.Output()
		if err != nil {
			return fmt.Errorf("project history requires a Git checkout; use --scope installation outside a project")
		}
		projectRoot, err = filepath.EvalSymlinks(strings.TrimSpace(string(root)))
		if err != nil {
			return err
		}
	}
	terminal := dashboardTerminal(output)
	watch := options.watch || (terminal && !options.once && !options.json)
	if options.notifications.Mode != "off" && !watch {
		return fmt.Errorf("notifications require --watch or a live terminal")
	}
	if options.notifications.Mode == "bell" && !terminal {
		return fmt.Errorf("bell notifications require a terminal")
	}
	sender, err := newSender(options.notifications, output)
	if err != nil {
		return err
	}
	observer := notify.NewObserver(sender)
	warned := false
	var commands <-chan string
	if terminal && watch && options.run == "" {
		if input, openErr := openDashboardInput(); openErr == nil {
			defer input.Close()
			commands = readDashboardInput(ctx, input)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		now := time.Now().UTC()
		views, err := registry.List(now)
		if err != nil {
			return err
		}
		if projectRoot != "" {
			filtered := make([]runstatus.View, 0, len(views))
			for _, view := range views {
				if sameProjectRoot(view.Root, projectRoot) {
					filtered = append(filtered, view)
				}
			}
			views = filtered
		}
		views = dashboard.Ordered(views)
		newWarning := false
		if options.notifications.Mode != "off" {
			notificationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			newWarning = observer.Observe(notificationCtx, views) != nil && !warned
			cancel()
		}
		warned = warned || newWarning
		allViews := views
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
			rows, page := dashboard.Page(views, options.page)
			return writeDashboardJSONPage(output, rows, now, page)
		}
		if options.run != "" {
			err = dashboard.Detail(output, views[0], now, options.logs)
		} else {
			_, page := dashboard.Page(allViews, options.page)
			options.page = page.Page
			err = dashboard.ListPage(output, allViews, now, options.page)
			if commands != nil && err == nil {
				_, err = fmt.Fprintln(output, "Live paging: n Enter / p Enter. Close: q Enter.")
			}
		}
		if err == nil && (newWarning || terminal && warned) {
			_, err = fmt.Fprintln(output, "SDLC could not deliver a local notification; check local notification settings. Runs continue.")
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
		case command, ok := <-commands:
			timer.Stop()
			if !ok {
				commands = nil
				continue
			}
			page, quit := dashboardPageAction(options.page, command)
			if quit {
				return nil
			}
			options.page = page
		}
	}
}

func sameProjectRoot(left, right string) bool {
	canonical, err := filepath.EvalSymlinks(left)
	return err == nil && canonical == right
}

// Exclude the full journal: it contains prompts and implementation instructions.
func writeDashboardJSON(output io.Writer, views []runstatus.View, now time.Time) error {
	rows, page := dashboard.Page(views, 1)
	return writeDashboardJSONPage(output, rows, now, page)
}

func writeDashboardJSONPage(output io.Writer, views []runstatus.View, now time.Time, page dashboard.Pagination) error {
	type row struct {
		ID              string            `json:"id"`
		Root            string            `json:"root"`
		Reference       string            `json:"reference"`
		Ticket          string            `json:"ticket"`
		Directory       string            `json:"directory"`
		State           string            `json:"state"`
		Role            string            `json:"role"`
		Provider        string            `json:"provider"`
		Model           string            `json:"model"`
		Effort          string            `json:"effort"`
		Live            bool              `json:"live"`
		Stale           bool              `json:"stale"`
		Stopped         bool              `json:"stopped"`
		Available       bool              `json:"available"`
		NeedsAttention  bool              `json:"needs_attention"`
		StartedAt       time.Time         `json:"started_at"`
		UpdatedAt       time.Time         `json:"updated_at"`
		HeartbeatAt     time.Time         `json:"heartbeat_at"`
		LastActivityAt  time.Time         `json:"last_activity_at"`
		StopReason      string            `json:"stop_reason,omitempty"`
		Error           string            `json:"error,omitempty"`
		ActivityError   string            `json:"activity_error,omitempty"`
		CI              string            `json:"ci_status,omitempty"`
		PR              string            `json:"pr_url,omitempty"`
		Usage           runstatus.Usage   `json:"usage"`
		WaitingProvider string            `json:"waiting_provider,omitempty"`
		WaitReason      string            `json:"wait_reason,omitempty"`
		WaitingSince    time.Time         `json:"waiting_since"`
		Metrics         *runusage.Summary `json:"metrics,omitempty"`
		MetricsError    string            `json:"metrics_error,omitempty"`
		Timings         *workrun.Timings  `json:"timings,omitempty"`
	}
	rows := make([]row, 0, len(views))
	for _, v := range views {
		var timings *workrun.Timings
		if v.Journal != nil {
			timings = v.Journal.Timings.Recorded()
		}
		rows = append(rows, row{v.ID, v.Root, v.Reference, v.Ticket, v.Directory, v.State, v.Role, v.Provider, v.Model, v.Effort, v.Live, v.Stale, v.Stopped, v.Available, v.NeedsAttention, v.StartedAt, v.UpdatedAt, v.HeartbeatAt, v.LastActivityAt, v.StopReason, v.Error, v.ActivityError, v.CI.Status, v.PR.URL, v.Activity.Usage, v.Activity.WaitingProvider, v.Activity.WaitReason, v.Activity.WaitingSince, v.Metrics, v.MetricsError, timings})
	}
	return json.NewEncoder(output).Encode(struct {
		Version int       `json:"version"`
		At      time.Time `json:"at"`
		Runs    []row     `json:"runs"`
		dashboard.Pagination
	}{1, now, rows, page})
}
