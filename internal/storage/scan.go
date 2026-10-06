// Package storage measures retained local artifacts without changing run state.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
)

type Totals struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}
type Category struct {
	Name string `json:"name"`
	Totals
}
type Run struct {
	ID           string    `json:"id"`
	Reference    string    `json:"reference"`
	Ticket       string    `json:"ticket"`
	Path         string    `json:"path"`
	State        string    `json:"state"`
	Retention    string    `json:"retention"`
	Controller   string    `json:"controller"`
	LastActivity time.Time `json:"last_activity"`
	AgeDays      float64   `json:"age_days"`
	Totals
	Categories []Category `json:"categories"`
}
type Issue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Report struct {
	Version       int        `json:"version"`
	Directory     string     `json:"directory"`
	Exists        bool       `json:"exists"`
	Complete      bool       `json:"complete"`
	MeasuredAt    time.Time  `json:"measured_at"`
	OlderThanDays *int       `json:"older_than_days,omitempty"`
	Totals        Totals     `json:"totals"`
	Categories    []Category `json:"categories"`
	TotalRuns     int        `json:"total_runs"`
	MatchingRuns  Totals     `json:"matching_runs"`
	Runs          []Run      `json:"runs"`
	Skipped       []Issue    `json:"skipped"`
	Errors        []Issue    `json:"errors"`
}
type Options struct {
	Now           time.Time
	OlderThanDays *int
}

func excluded(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".secrets", ".ssh", ".aws", ".codex", ".claude", "provider-auth", "auth", "credentials", ".credentials", "auth.json":
		return true
	}
	return false
}
func runPath(relative string) (string, []string) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) >= 5 && parts[0] == "work" && parts[2] == "runs" {
		return filepath.Join(parts[:5]...), parts
	}
	return "", parts
}
func category(parts []string, run bool) string {
	if run && len(parts) > 5 {
		artifact := parts[5:]
		if artifact[0] == "workspace" {
			return "workspace"
		}
		if strings.HasPrefix(artifact[0], "native-") || artifact[0] == "sessions" {
			return "provider sessions"
		}
		if artifact[0] == "inputs" {
			return "captured inputs"
		}
		for _, part := range artifact {
			if part == "logs" || strings.HasSuffix(part, ".log") {
				return "logs"
			}
		}
		if len(artifact) == 1 && strings.HasSuffix(artifact[0], ".json") {
			return "run metadata"
		}
		return "other run artifacts"
	}
	if len(parts) > 0 && parts[0] == "work" {
		return "work references"
	}
	return "project configuration"
}
func addCategory(categories map[string]Totals, name string, size int64) {
	t := categories[name]
	t.Bytes += size
	t.Files++
	categories[name] = t
}
func categories(rows map[string]Totals) []Category {
	result := make([]Category, 0, len(rows))
	for name, total := range rows {
		result = append(result, Category{name, total})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Bytes > result[j].Bytes || result[i].Bytes == result[j].Bytes && result[i].Name < result[j].Name
	})
	return result
}
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Scan counts logical bytes for each regular-file path, including hard links per
// path. It never follows symlinks, enters excluded directories or another device.
// Only bounded checkpoint/activity metadata is decoded; artifact contents stay private.
func Scan(ctx context.Context, root string, options Options) (Report, error) {
	return scan(ctx, root, options, device)
}

func scan(ctx context.Context, root string, options Options, deviceID func(os.FileInfo) (uint64, error)) (Report, error) {
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	report := Report{Version: 1, Directory: filepath.Join(root, ".sdlc"), Complete: true, MeasuredAt: options.Now, OlderThanDays: options.OlderThanDays, Runs: []Run{}, Skipped: []Issue{}, Errors: []Issue{}}
	if options.OlderThanDays != nil && (*options.OlderThanDays < 0 || *options.OlderThanDays > 36500) {
		return report, fmt.Errorf("older-than days must be between 0 and 36500")
	}
	info, err := os.Lstat(report.Directory)
	if os.IsNotExist(err) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return report, fmt.Errorf("project .sdlc must be a real directory")
	}
	rootDevice, err := deviceID(info)
	if err != nil {
		return report, err
	}
	report.Exists = true
	totals := map[string]Totals{}
	runs := map[string]*Run{}
	runCategories := map[string]map[string]Totals{}
	issue := func(path string, err error) {
		report.Complete = false
		report.Errors = append(report.Errors, Issue{path, err.Error()})
	}
	err = filepath.WalkDir(report.Directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(report.Directory, path)
		if err != nil {
			return err
		}
		if walkErr != nil {
			issue(relative, walkErr)
			return nil
		}
		if relative == "." {
			return nil
		}
		if excluded(entry.Name()) {
			report.Skipped = append(report.Skipped, Issue{relative, "excluded Git or authentication state"})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.Skipped = append(report.Skipped, Issue{relative, "symbolic link"})
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			issue(relative, err)
			return nil
		}
		fileDevice, err := deviceID(info)
		if err != nil {
			issue(relative, err)
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fileDevice != rootDevice {
			report.Skipped = append(report.Skipped, Issue{relative, "different filesystem device"})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		runKey, parts := runPath(relative)
		var run *Run
		if runKey != "" {
			run = runs[runKey]
			if run == nil {
				run = &Run{ID: parts[4], Reference: parts[1], Ticket: parts[3], Path: runKey, State: "unknown", Retention: "unknown", Controller: "unknown"}
				runs[runKey] = run
				runCategories[runKey] = map[string]Totals{}
			}
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			report.Skipped = append(report.Skipped, Issue{relative, "not a regular file"})
			return nil
		}
		report.Totals.Bytes += info.Size()
		report.Totals.Files++
		name := category(parts, run != nil)
		addCategory(totals, name, info.Size())
		if run != nil {
			run.Bytes += info.Size()
			run.Files++
			run.LastActivity = latest(run.LastActivity, info.ModTime())
			addCategory(runCategories[runKey], name, info.Size())
			if len(parts) == 6 && (entry.Name() == "journal.json" || entry.Name() == "activity.json" || entry.Name() == "preparation.json") {
				var metadata struct {
					ID             string    `json:"id"`
					State          string    `json:"state"`
					UpdatedAt      time.Time `json:"updated_at"`
					FailedAt       time.Time `json:"failed_at"`
					HeartbeatAt    time.Time `json:"heartbeat_at"`
					LastActivityAt time.Time `json:"last_activity_at"`
					Stopped        bool      `json:"stopped"`
				}
				if err := readMetadata(path, info, &metadata); err != nil {
					issue(relative, err)
				} else {
					if metadata.ID != "" && metadata.ID != run.ID {
						issue(relative, fmt.Errorf("metadata run ID differs from directory"))
						return nil
					}
					switch entry.Name() {
					case "journal.json":
						if validState(metadata.State) {
							run.State = metadata.State
						} else {
							issue(relative, fmt.Errorf("invalid checkpoint state"))
						}
						run.LastActivity = latest(run.LastActivity, metadata.UpdatedAt)
					case "preparation.json":
						if run.State == "unknown" {
							run.State = "blocked"
						}
						run.LastActivity = latest(run.LastActivity, metadata.FailedAt)
					case "activity.json":
						run.LastActivity = latest(run.LastActivity, latest(metadata.HeartbeatAt, metadata.LastActivityAt))
						if metadata.Stopped {
							run.Controller = "stopped"
						} else if !metadata.HeartbeatAt.IsZero() {
							run.Controller = "stale heartbeat"
							if options.Now.Sub(metadata.HeartbeatAt) <= runstatus.StaleAfter {
								run.Controller = "recent heartbeat"
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	report.Categories = categories(totals)
	report.TotalRuns = len(runs)
	for key, run := range runs {
		run.Categories = categories(runCategories[key])
		if !run.LastActivity.IsZero() {
			run.AgeDays = max(0, options.Now.Sub(run.LastActivity).Hours()/24)
		}
		if run.State == "" {
			run.State = "unknown"
		}
		switch run.State {
		case "ready":
			run.Retention = "completed; awaiting review"
		case "unknown", "":
			run.Retention = "unknown"
		default:
			run.Retention = "resumable"
		}
		if options.OlderThanDays != nil && (run.LastActivity.IsZero() || !run.LastActivity.Before(options.Now.Add(-time.Duration(*options.OlderThanDays)*24*time.Hour))) {
			continue
		}
		report.Runs = append(report.Runs, *run)
		report.MatchingRuns.Bytes += run.Bytes
		report.MatchingRuns.Files += run.Files
	}
	sort.Slice(report.Runs, func(i, j int) bool {
		return report.Runs[i].LastActivity.Before(report.Runs[j].LastActivity) || report.Runs[i].LastActivity.Equal(report.Runs[j].LastActivity) && report.Runs[i].Path < report.Runs[j].Path
	})
	return report, nil
}
func readMetadata(path string, expected os.FileInfo, value any) error {
	if expected.Size() > 1024*1024 {
		return fmt.Errorf("metadata exceeds 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return fmt.Errorf("metadata changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024+1))
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid run metadata")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("metadata contains trailing data")
	}
	return nil
}

func validState(state string) bool {
	if len(state) == 0 || len(state) > 64 {
		return false
	}
	for _, r := range state {
		if r != '_' && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}
