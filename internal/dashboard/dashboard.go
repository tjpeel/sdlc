// Package dashboard renders private local run status without controlling runs.
package dashboard

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tjpeel/sdlc/internal/runstatus"
)

// SafeText prevents repository names, questions and logs from controlling a terminal.
func SafeText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, value)
}

func clip(value string, width int) string {
	runes := []rune(SafeText(value))
	if len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return string(runes)
}

func age(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	duration := now.Sub(then)
	if duration < 0 {
		duration = 0
	}
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(duration.Hours()), int(duration.Minutes())%60)
}

func controller(v runstatus.View) string {
	if !v.Available {
		return "unknown"
	}
	if v.Live {
		return "live"
	}
	if v.Stale {
		return "stale"
	}
	return "stopped"
}

func priority(v runstatus.View) int {
	if v.State == "waiting_for_human" {
		return 0
	}
	if !v.Available || v.State == "blocked" || v.State == "failed" || v.Stale || (v.Stopped && v.NeedsAttention && v.State != "ready" && v.State != "awaiting_reviewer") {
		return 1
	}
	if v.State == "ready" {
		return 4
	}
	if v.NeedsAttention {
		return 2
	}
	return 3
}

func Ordered(views []runstatus.View) []runstatus.View {
	ordered := append([]runstatus.View(nil), views...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if priority(ordered[i]) != priority(ordered[j]) {
			return priority(ordered[i]) < priority(ordered[j])
		}
		if !ordered[i].UpdatedAt.Equal(ordered[j].UpdatedAt) {
			return ordered[i].UpdatedAt.After(ordered[j].UpdatedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// Select accepts a full ID or an unambiguous prefix of at least six characters.
func Select(views []runstatus.View, id string) (runstatus.View, error) {
	if !regexp.MustCompile(`^[0-9a-f]{6,24}$`).MatchString(id) {
		return runstatus.View{}, fmt.Errorf("run ID must be six to twenty-four lowercase hexadecimal characters")
	}
	var found []runstatus.View
	for _, v := range views {
		if strings.HasPrefix(v.ID, id) {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return runstatus.View{}, fmt.Errorf("no registered run matches %q", id)
	}
	if len(found) != 1 {
		return runstatus.View{}, fmt.Errorf("run prefix %q is ambiguous; use a longer ID", id)
	}
	return found[0], nil
}

func List(output io.Writer, views []runstatus.View, now time.Time) error {
	return ListPage(output, views, now, 1)
}

// PageSize bounds every overview, including redirected output and JSON.
const PageSize = 10

type Pagination struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
	Pages    int `json:"pages"`
}

// Page clamps a page after removals and keeps attention ordering across history.
func Page(views []runstatus.View, requested int) ([]runstatus.View, Pagination) {
	ordered := Ordered(views)
	pages := (len(ordered) + PageSize - 1) / PageSize
	if pages == 0 {
		pages = 1
	}
	if requested < 1 {
		requested = 1
	}
	if requested > pages {
		requested = pages
	}
	start := (requested - 1) * PageSize
	end := start + PageSize
	if end > len(ordered) {
		end = len(ordered)
	}
	return ordered[start:end], Pagination{requested, PageSize, len(ordered), pages}
}

func ListPage(output io.Writer, views []runstatus.View, now time.Time, requested int) error {
	rows, page := Page(views, requested)
	live, attention := 0, 0
	for _, v := range views {
		if v.Live {
			live++
		}
		if v.NeedsAttention {
			attention++
		}
	}
	if _, err := fmt.Fprintf(output, "SDLC  %s  |  %d runs  %d live  %d need attention\n\n", now.Format("15:04:05"), len(views), live, attention); err != nil {
		return err
	}
	if len(views) == 0 {
		_, err := fmt.Fprintln(output, "No registered runs. New runs register when their controller starts; resume older runs to register them.")
		return err
	}
	if _, err := fmt.Fprintf(output, "Page %d/%d  |  showing %d–%d of %d (up to %d per page)\n\n", page.Page, page.Pages, (page.Page-1)*PageSize+1, (page.Page-1)*PageSize+len(rows), page.Total, PageSize); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "%-4s %-12s %-17s %-21s %-19s %-8s %-7s %-7s\n", "", "RUN", "REPOSITORY", "TICKET", "STAGE", "CONTROL", "PROVIDER", "ACTIVITY"); err != nil {
		return err
	}
	for _, v := range rows {
		mark := ""
		if v.NeedsAttention {
			mark = "!"
		}
		id := v.ID
		if len(id) > 12 {
			id = id[:12]
		}
		stage := v.State
		if v.Live && v.Activity.WaitReason != "" {
			stage = "queued: " + stage
		}
		if _, err := fmt.Fprintf(output, "%-4s %-12s %-17s %-21s %-19s %-8s %-7s %-7s\n", mark, id, clip(filepath.Base(v.Root), 17), clip(filepath.Base(v.Ticket), 21), clip(stage, 19), controller(v), clip(v.Provider, 7), age(now, v.LastActivityAt)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "     %s | %s / %s | elapsed %s\n", clip(v.Reference, 40), clip(v.Model, 40), clip(v.Effort, 12), elapsed(v, now)); err != nil {
			return err
		}
		if v.Activity.Usage.Context != nil || v.Activity.Usage.Aggregate != nil {
			if _, err := fmt.Fprintln(output, "     "+SafeText(usageSummary(v.Activity.Usage))); err != nil {
				return err
			}
		}
		if reason := Reason(v); reason != "" {
			if _, err := fmt.Fprintf(output, "     %s\n", clip(reason, 112)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(output, "\nSelect: sdlc dashboard --run RUN_ID   History: --page N   Snapshot: --once   JSON: --json\nRemove from dashboard: sdlc dashboard forget --run RUN_ID (saved work is retained)\nCtrl-C closes this view; run controllers continue independently.")
	return err
}

func elapsed(v runstatus.View, now time.Time) string {
	if v.Stopped && !v.Activity.StoppedAt.IsZero() {
		now = v.Activity.StoppedAt
	}
	return age(now, v.StartedAt)
}

func Reason(v runstatus.View) string {
	if v.Error != "" {
		return "Run unavailable: " + v.Error
	}
	if v.ActivityError != "" {
		return "Activity unavailable: " + v.ActivityError
	}
	if v.StopReason != "" {
		return v.StopReason
	}
	if v.Stale {
		return "Controller heartbeat is stale; inspect the original process before resuming."
	}
	if v.Live && v.Activity.WaitReason != "" {
		if v.Activity.WaitReason == "runtime_busy" {
			return "Waiting for the runtime build to release its lease."
		}
		return "Waiting for another " + v.Activity.WaitingProvider + " operation to release its account cache."
	}
	if v.Stopped && v.NeedsAttention && v.State != "ready" {
		return "Controller stopped; checkpoint retained."
	}
	if v.State == "ready" {
		return "Draft PR is ready for human review."
	}
	return ""
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func tokenCount(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprint(*value)
}

func usageSummary(usage runstatus.Usage) string {
	context := "Context: unknown"
	if usage.Context != nil && usage.Context.Tokens != nil {
		context = "Context: " + tokenCount(usage.Context.Tokens) + " tokens"
		if usage.ModelMatches && usage.Context.Window != nil && *usage.Context.Window > 0 {
			context += fmt.Sprintf(" / %d (%.1f%%)", *usage.Context.Window, 100*float64(*usage.Context.Tokens)/float64(*usage.Context.Window))
		}
	}
	if usage.Aggregate != nil {
		context += fmt.Sprintf(" | Native totals: input %s, cached %s, output %s", tokenCount(usage.Aggregate.InputTokens), tokenCount(usage.Aggregate.CachedInputTokens), tokenCount(usage.Aggregate.OutputTokens))
	}
	return context
}

func Detail(output io.Writer, v runstatus.View, now time.Time, logs bool) error {
	var text strings.Builder
	fmt.Fprintf(&text, "Run: %s\nRepository: %s\nTicket: %s / %s\nStage: %s | controller: %s | elapsed: %s\nRole: %s | model: %s / %s / %s\nLast output: %s ago | heartbeat: %s ago\n", v.ID, v.Root, v.Reference, filepath.Base(v.Ticket), v.State, controller(v), elapsed(v, now), v.Role, v.Provider, v.Model, v.Effort, age(now, v.LastActivityAt), age(now, v.HeartbeatAt))
	fmt.Fprintln(&text, usageSummary(v.Activity.Usage))
	if reason := Reason(v); reason != "" {
		fmt.Fprintf(&text, "Attention: %s\n", reason)
	}
	if v.PR.URL != "" {
		fmt.Fprintf(&text, "PR: %s\n", v.PR.URL)
	}
	if v.CI.Status != "" {
		fmt.Fprintf(&text, "CI: %s %s\n", v.CI.Status, v.CI.Details)
	}
	if v.Journal != nil {
		j := v.Journal
		checks := "not run"
		if j.Evidence.Tree != "" {
			checks = "failed"
			if j.Evidence.Passed {
				checks = "passed"
			}
		}
		fmt.Fprintf(&text, "Local checks: %s | sessions: %d | repair rounds: %d\n", checks, j.Attempt, j.Rounds)
	}
	if v.State == "waiting_for_human" {
		for _, question := range v.Questions {
			fmt.Fprintf(&text, "Question: %s\n", question)
		}
	}
	for _, finding := range v.Findings {
		fmt.Fprintf(&text, "Finding: %s %s:%d %s — %s\n", finding.Priority, finding.Path, finding.Line, finding.Scenario, finding.Recommendation)
	}
	fmt.Fprintf(&text, "Private state: %s\n", v.Directory)
	if v.Available {
		fmt.Fprintf(&text, "Resume from repository: sdlc run --reference %s --ticket %s --resume %s", quote(v.Reference), quote(filepath.Base(v.Ticket)), v.ID)
		if v.State == "waiting_for_human" {
			text.WriteString(" --answer-file /PATH/TO/PRIVATE_ANSWER.txt")
		}
		text.WriteByte('\n')
	}
	for _, line := range strings.Split(text.String(), "\n") {
		if _, err := fmt.Fprintln(output, SafeText(line)); err != nil {
			return err
		}
	}
	if logs && v.Available {
		if _, err := fmt.Fprintln(output, "Recent output (private; bounded tail):"); err != nil {
			return err
		}
		path := ""
		if v.Journal != nil {
			if (v.State == "checking" || ((v.State == "blocked" || v.State == "failed") && v.Journal.ResumeState == "checking")) && v.Journal.CheckAttempt > 0 {
				path = fmt.Sprintf("checks-%d.log", v.Journal.CheckAttempt)
			} else if v.Journal.Attempt > 0 {
				path = fmt.Sprintf("events-%d.jsonl", v.Journal.Attempt)
			}
		}
		if path == "" {
			_, err := fmt.Fprintln(output, "No session output yet.")
			return err
		}
		tail, err := Tail(v.Directory, path)
		if err != nil {
			_, writeErr := fmt.Fprintln(output, "Output unavailable:", SafeText(err.Error()))
			return writeErr
		}
		for _, line := range tail {
			if _, err := fmt.Fprintln(output, clip(line, 240)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Tail reads only a fixed run-local filename, never links or arbitrary paths.
func Tail(directory, name string) ([]string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, fmt.Errorf("output directory must be an absolute clean path")
	}
	if filepath.Base(name) != name || name == "." {
		return nil, fmt.Errorf("invalid output filename")
	}
	for p := filepath.Clean(directory); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("output directory is unavailable")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("output must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("output changed while opening")
	}
	offset := info.Size() - 16*1024
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, 16*1024))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return lines, nil
}
