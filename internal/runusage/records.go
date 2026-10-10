package runusage

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/headroom"
)

type ProxyStats struct {
	Source           string `json:"source"`
	Requests         *int64 `json:"requests"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	SavedTokens      *int64 `json:"saved_tokens"`
	ObservedAttempts int    `json:"observed_attempts"`
	MissingAttempts  int    `json:"missing_attempts"`
}

const Version = 1
const maxRecords = 4096
const maxRecordBytes = 512 * 1024

var clientVersion = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,8}$`)

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Attempt is private native-client telemetry. SessionID never appears in Summary.
// Elapsed time covers the entire attempt, including setup and queueing.
type Attempt struct {
	Version       int             `json:"version"`
	RunID         string          `json:"run_id"`
	Attempt       int             `json:"attempt"`
	Role          string          `json:"role"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Effort        string          `json:"effort,omitempty"`
	ImageID       string          `json:"image_id,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       time.Time       `json:"ended_at,omitempty"`
	Outcome       string          `json:"outcome"`
	SessionID     string          `json:"session_id,omitempty"`
	Resumed       bool            `json:"resumed,omitempty"`
	OptimizerMode string          `json:"optimizer_mode"`
	HeadroomStats *headroom.Stats `json:"headroom_stats,omitempty"`
	ClientVersion string          `json:"client_version,omitempty"`
	Usage         Usage           `json:"usage"`
	EventTiming   *EventTiming    `json:"native_event_timing,omitempty"`
}

type Group struct {
	Models            map[string]TokenUsage `json:"models,omitempty"`
	PeakContextTokens *int64                `json:"peak_context_tokens,omitempty"`
	Compactions       int64                 `json:"compactions,omitempty"`
	Retries           int64                 `json:"retries,omitempty"`
	CompletedTurns    int64                 `json:"completed_turns,omitempty"`
	RateLimits        map[string]RateLimit  `json:"rate_limits,omitempty"`
	Provider          string                `json:"provider"`
	Model             string                `json:"requested_model"`
	Role              string                `json:"role"`
	OptimizerMode     string                `json:"optimizer_mode"`
	Attempts          int                   `json:"attempts"`
	MeasuredAttempts  int                   `json:"measured_attempts"`
	UnknownAttempts   int                   `json:"unknown_attempts"`
	ElapsedMS         int64                 `json:"elapsed_ms"`
	Tokens            TokenUsage            `json:"tokens"`
	ProxyStats        *ProxyStats           `json:"proxy_stats,omitempty"`
	EventTiming       *EventTimingSummary   `json:"native_event_timing,omitempty"`
}
type Summary struct {
	Version         int     `json:"version"`
	RunID           string  `json:"run_id"`
	Attempts        int     `json:"attempts"`
	Completed       int     `json:"completed"`
	Failed          int     `json:"failed"`
	Aborted         int     `json:"aborted"`
	Pending         int     `json:"pending"`
	Missing         int     `json:"missing"`
	UnknownBaseline int     `json:"unknown_baseline"`
	Incomplete      int     `json:"incomplete"`
	ElapsedMS       int64   `json:"elapsed_ms"`
	ElapsedScope    string  `json:"elapsed_scope"`
	Groups          []Group `json:"groups"`
}

func checkDirectory(dir string, create bool) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Refuse symlinks in every existing path component; do not follow a private
	// metrics directory onto another account's filesystem location.
	current := filepath.VolumeName(abs) + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err = os.Mkdir(current, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 && (current == "/var" || current == "/tmp") {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr == nil && resolved == "/private"+current {
				continue
			}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("metrics path must be a real directory")
		}
	}
	return nil
}

// Hold the verified directory open so record operations cannot be redirected
// by a concurrent directory replacement or symlink insertion.
func openMetricsRoot(dir string, create bool) (*os.Root, error) {
	if err := checkDirectory(dir, create); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) || checkDirectory(dir, false) != nil || (!create && opened.Mode().Perm() != 0700) {
		root.Close()
		return nil, fmt.Errorf("metrics directory changed while opening")
	}
	return root, nil
}

func validateAttempt(a Attempt) error {
	if a.Version != Version || !safeID.MatchString(a.RunID) || a.Attempt < 1 || a.Attempt > maxRecords || !safeID.MatchString(a.Role) || !safeModel(a.Model) || !safeID.MatchString(a.Provider) || len(a.SessionID) > 256 || len(a.ImageID) > 512 || len(a.Effort) > 64 || len(a.OptimizerMode) > 64 {
		return fmt.Errorf("invalid metrics attempt identity")
	}
	switch a.Outcome {
	case "running", "completed", "failed", "aborted":
	default:
		return fmt.Errorf("invalid metrics outcome")
	}
	if a.StartedAt.IsZero() || (!a.EndedAt.IsZero() && a.EndedAt.Before(a.StartedAt)) {
		return fmt.Errorf("invalid metrics attempt timing")
	}
	if a.Outcome != "running" && a.EndedAt.IsZero() {
		return fmt.Errorf("finished metrics attempt has no end time")
	}
	if a.Outcome == "running" && !a.EndedAt.IsZero() {
		return fmt.Errorf("running metrics attempt has end time")
	}
	switch a.Provider {
	case "codex", "claude":
	default:
		return fmt.Errorf("invalid metrics provider")
	}
	switch a.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	case "none", "minimal", "ultra":
		if a.Provider != "codex" {
			return fmt.Errorf("invalid metrics effort")
		}
	default:
		return fmt.Errorf("invalid metrics effort")
	}
	switch a.OptimizerMode {
	case "off", "passthrough", "optimize":
	default:
		return fmt.Errorf("invalid metrics optimizer mode")
	}
	if (a.ClientVersion != "" && !clientVersion.MatchString(a.ClientVersion)) || (a.SessionID != "" && !safeModel(a.SessionID)) || (a.ImageID != "" && !safeModel(a.ImageID)) {
		return fmt.Errorf("invalid metrics metadata")
	}
	if a.HeadroomStats != nil {
		st := a.HeadroomStats
		if st.Version != headroom.Version || st.Source != "headroom_proxy_aggregate" || st.Mode != a.OptimizerMode || a.OptimizerMode == "off" || (st.Error != "" && st.Error != "proxy metrics unavailable") {
			return fmt.Errorf("invalid proxy metadata")
		}
		all := st.Requests != nil && st.InputTokens != nil && st.OutputTokens != nil && st.SavedTokens != nil
		if st.Complete != all || (st.Error != "" && (st.Requests != nil || st.InputTokens != nil || st.OutputTokens != nil || st.SavedTokens != nil)) {
			return fmt.Errorf("invalid proxy completeness")
		}
		for _, n := range []*int64{a.HeadroomStats.Requests, a.HeadroomStats.InputTokens, a.HeadroomStats.OutputTokens, a.HeadroomStats.SavedTokens} {
			if n != nil && (*n < 0 || *n > maxTokenCount/maxRecords) {
				return fmt.Errorf("invalid proxy metrics")
			}
		}
	}
	if err := validateUsage(a.Usage); err != nil {
		return err
	}
	if value := a.EventTiming; value != nil {
		if value.Events < 0 || value.Events > maxTokenCount || value.LongestGapMS < 0 || value.LongestGapMS > maxTokenCount {
			return fmt.Errorf("invalid native event timing")
		}
		for _, n := range []*int64{value.FirstEventMS, value.FinalSilenceMS} {
			if n != nil && (*n < 0 || *n > maxTokenCount) {
				return fmt.Errorf("invalid native event timing")
			}
		}
		if (value.Events == 0 && (value.FirstEventMS != nil || value.FinalSilenceMS != nil || value.LongestGapMS != 0)) || (value.Events > 0 && (value.FirstEventMS == nil || value.FinalSilenceMS == nil)) {
			return fmt.Errorf("native event timing lacks matching observations")
		}
	}
	if len(a.Usage.Models) > 64 || len(a.Usage.RateLimits) > 5 {
		return fmt.Errorf("metrics collection exceeds limit")
	}
	return nil
}
func recordName(a Attempt) string { return fmt.Sprintf("attempt-%06d-%s.json", a.Attempt, a.Role) }
func SaveAttempt(directory string, a Attempt) error {
	if a.ClientVersion == "" {
		a.ClientVersion = a.Usage.ClientVersion
	}
	if a.Version == 0 {
		a.Version = Version
	}
	if a.OptimizerMode == "" || a.OptimizerMode == "native" {
		a.OptimizerMode = "off"
	}
	if a.Outcome == "" {
		a.Outcome = "running"
	}
	if err := validateAttempt(a); err != nil {
		return err
	}
	dir := filepath.Join(directory, "metrics")
	root, err := openMetricsRoot(dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Chmod(".", 0700); err != nil {
		return err
	}
	name := recordName(a)
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("metrics record must be regular")
		}
		var old Attempt
		if err = readRecord(root, name, &old); err != nil {
			return err
		}
		if old.RunID != a.RunID || old.Provider != a.Provider || old.Model != a.Model || !old.StartedAt.Equal(a.StartedAt) {
			return fmt.Errorf("metrics identity changed")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("metrics record exceeds limit")
	}
	temporary := ".attempt-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
func readRecord(root *os.Root, name string, a *Attempt) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxRecordBytes {
		return fmt.Errorf("invalid metrics record file")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, actual) {
		return fmt.Errorf("metrics record changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("metrics record exceeds limit")
	}
	if err = json.Unmarshal(data, a); err != nil {
		return err
	}
	return validateAttempt(*a)
}

// LoadSummary exports no session identifiers, transcripts, paths or credentials.
// Unknown resumed baselines remain unknown; observed session totals are not added
// twice. Callers should treat elapsed_ms as summed attempt time, not wall time.
func LoadSummary(directory, runID string, expectedAttempts int) (Summary, error) {
	s := Summary{Version: Version, RunID: runID, ElapsedScope: "attempt_setup_queue_and_execution", Groups: []Group{}}
	if !safeID.MatchString(runID) || expectedAttempts < 0 || expectedAttempts > maxRecords {
		return s, fmt.Errorf("invalid summary identity")
	}
	dir := filepath.Join(directory, "metrics")
	root, err := openMetricsRoot(dir, false)
	if err != nil {
		if os.IsNotExist(err) {
			s.Missing = expectedAttempts
			return s, nil
		}
		return s, err
	}
	defer root.Close()
	directoryFile, err := root.Open(".")
	if err != nil {
		return s, err
	}
	entries, err := directoryFile.ReadDir(-1)
	directoryFile.Close()
	if err != nil {
		return s, err
	}
	if len(entries) > maxRecords*2 {
		return s, fmt.Errorf("too many metrics files")
	}
	records := []Attempt{}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "attempt-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var a Attempt
		if err = readRecord(root, e.Name(), &a); err != nil {
			return s, err
		}
		if a.RunID != runID || e.Name() != recordName(a) {
			return s, fmt.Errorf("metrics record identity mismatch")
		}
		records = append(records, a)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt.Equal(records[j].StartedAt) {
			return recordName(records[i]) < recordName(records[j])
		}
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
	groups := map[string]*Group{}
	baselines := map[string]TokenUsage{}
	modelBaselines := map[string]map[string]TokenUsage{}
	for _, a := range records {
		s.Attempts++
		switch a.Outcome {
		case "completed":
			s.Completed++
		case "failed":
			s.Failed++
		case "aborted":
			s.Aborted++
		default:
			s.Pending++
		}
		key := a.Provider + "\x00" + a.Model + "\x00" + a.Role + "\x00" + a.OptimizerMode
		g := groups[key]
		if g == nil {
			g = &Group{Provider: a.Provider, Model: a.Model, Role: a.Role, OptimizerMode: a.OptimizerMode, Tokens: TokenUsage{Source: "native.metrics", Scope: "compatible_attempt_deltas"}}
			groups[key] = g
		}
		g.Attempts++
		if g.EventTiming == nil {
			g.EventTiming = &EventTimingSummary{}
		}
		g.EventTiming.add(a.EventTiming)
		g.Compactions += a.Usage.Compactions
		g.Retries += a.Usage.Retries
		g.CompletedTurns += a.Usage.CompletedTurns
		if a.Usage.PeakContextTokens != nil && (g.PeakContextTokens == nil || *a.Usage.PeakContextTokens > *g.PeakContextTokens) {
			v := *a.Usage.PeakContextTokens
			g.PeakContextTokens = &v
		}
		if len(a.Usage.RateLimits) > 0 {
			if g.RateLimits == nil {
				g.RateLimits = map[string]RateLimit{}
			}
			for w, r := range a.Usage.RateLimits {
				old, ok := g.RateLimits[w]
				if !ok || r.ObservedAt.After(old.ObservedAt) {
					g.RateLimits[w] = r
				}
			}
		}

		if a.OptimizerMode != "native" && a.OptimizerMode != "off" && a.OptimizerMode != "" {
			if g.ProxyStats == nil {
				g.ProxyStats = &ProxyStats{Source: "headroom_proxy_aggregate"}
			}
			st := a.HeadroomStats
			if st == nil || st.Error != "" {
				g.ProxyStats.MissingAttempts++
				g.ProxyStats.Requests = nil
				g.ProxyStats.InputTokens = nil
				g.ProxyStats.OutputTokens = nil
				g.ProxyStats.SavedTokens = nil
			} else {
				first := g.ProxyStats.ObservedAttempts == 0 && g.ProxyStats.MissingAttempts == 0
				g.ProxyStats.ObservedAttempts++
				for _, pair := range []struct {
					dst **int64
					src *int64
				}{{&g.ProxyStats.Requests, st.Requests}, {&g.ProxyStats.InputTokens, st.InputTokens}, {&g.ProxyStats.OutputTokens, st.OutputTokens}, {&g.ProxyStats.SavedTokens, st.SavedTokens}} {
					if first && pair.src != nil {
						v := *pair.src
						*pair.dst = &v
					} else if *pair.dst == nil || pair.src == nil {
						*pair.dst = nil
					} else {
						v := **pair.dst + *pair.src
						*pair.dst = &v
					}
				}
			}
		}

		if !a.EndedAt.IsZero() {
			elapsed := a.EndedAt.Sub(a.StartedAt).Milliseconds()
			if elapsed < 0 || elapsed > maxTokenCount || s.ElapsedMS > maxTokenCount-elapsed {
				return s, fmt.Errorf("metrics elapsed exceeds bound")
			}
			g.ElapsedMS += elapsed
			s.ElapsedMS += elapsed
		}
		totals, known := wholeTree(a.Usage, a.Provider)
		complete := a.Usage.Completeness == "observed" && a.Outcome != "running"
		if !complete {
			s.Incomplete++
		}
		baselineKey := a.Provider + "\x00" + a.Model + "\x00" + a.OptimizerMode + "\x00" + a.ImageID + "\x00" + a.SessionID
		delta := totals
		if a.Resumed {
			old, ok := baselines[baselineKey]
			if !ok || a.SessionID == "" {
				known = false
				s.UnknownBaseline++
			} else {
				var compatible bool
				delta, compatible = subtract(totals, old)
				known = known && compatible
			}
		}
		// A known cumulative checkpoint establishes a baseline even when the
		// earlier interval was unmeasurable, so the next resume can be measured.
		_, checkpointKnown := wholeTree(a.Usage, a.Provider)
		if a.SessionID != "" && complete && checkpointKnown {
			baselines[baselineKey] = totals
		} else {
			delete(baselines, baselineKey)
		}
		if known && complete && a.Provider == "claude" {
			// Validate every model before applying any contribution to the group.
			deltas := make(map[string]TokenUsage, len(a.Usage.Models))
			if a.Resumed {
				for model := range modelBaselines[baselineKey] {
					if _, ok := a.Usage.Models[model]; !ok {
						known = false
					}
				}
			}
			for model, v := range a.Usage.Models {
				d := v
				if a.Resumed {
					if old, ok := modelBaselines[baselineKey][model]; ok {
						var valid bool
						d, valid = subtract(v, old)
						if !valid {
							known = false
						}
					}
				}
				deltas[model] = d
			}
			if known {
				if g.Models == nil {
					g.Models = map[string]TokenUsage{}
				}
				for model, d := range deltas {
					old, exists := g.Models[model]
					if err := addTokens(&old, d, !exists); err != nil {
						return s, err
					}
					old.Source = "claude.result.modelUsage"
					old.Scope = "compatible_attempt_deltas"
					g.Models[model] = old
				}
			}
		}
		if a.SessionID != "" && complete && checkpointKnown && a.Provider == "claude" {
			modelBaselines[baselineKey] = a.Usage.Models
		} else {
			delete(modelBaselines, baselineKey)
		}
		if !known || !complete {
			g.UnknownAttempts++
			continue
		}
		if err := addTokens(&g.Tokens, delta, g.MeasuredAttempts == 0); err != nil {
			return s, err
		}
		g.MeasuredAttempts++
	}
	s.Missing = expectedAttempts - s.Attempts
	if s.Missing < 0 {
		s.Missing = 0
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.Groups = append(s.Groups, *groups[k])
	}
	return s, nil
}
func tokenFields(u *TokenUsage) []**int64 {
	return []**int64{&u.InputTokens, &u.CachedInputTokens, &u.CacheCreationInputTokens, &u.OutputTokens, &u.ReasoningOutputTokens, &u.CacheWriteInputTokens}
}
func wholeTree(u Usage, provider string) (TokenUsage, bool) {
	if provider == "claude" {
		if len(u.Models) == 0 {
			return TokenUsage{}, false
		}
		var total TokenUsage
		first := true
		for _, v := range u.Models {
			if v.InputTokens == nil || v.CachedInputTokens == nil || v.CacheCreationInputTokens == nil || v.OutputTokens == nil {
				return TokenUsage{}, false
			}
			if err := addTokens(&total, v, first); err != nil {
				return TokenUsage{}, false
			}
			first = false
		}
		return total, true
	}
	if u.Aggregate == nil {
		return TokenUsage{}, false
	}
	v := *u.Aggregate
	return v, v.InputTokens != nil && v.CachedInputTokens != nil && v.OutputTokens != nil
}
func subtract(now, old TokenUsage) (TokenUsage, bool) {
	var d TokenUsage
	nf, of, df := tokenFields(&now), tokenFields(&old), tokenFields(&d)
	known := false
	for i := range nf {
		if *nf[i] == nil {
			continue
		}
		if *of[i] == nil {
			continue
		}
		if **nf[i] < **of[i] {
			return TokenUsage{}, false
		}
		n := **nf[i] - **of[i]
		*df[i] = &n
		known = true
	}
	return d, known
}
func addTokens(dst *TokenUsage, src TokenUsage, first bool) error {
	sf, df := tokenFields(&src), tokenFields(dst)
	for i := range sf {
		if *sf[i] == nil || (!first && *df[i] == nil) {
			*df[i] = nil
			continue
		}
		n := **sf[i]
		if n < 0 || n > maxTokenCount {
			return fmt.Errorf("metrics token exceeds bound")
		}
		if *df[i] != nil {
			if n > maxTokenCount-**df[i] {
				return fmt.Errorf("metrics token total exceeds bound")
			}
			n += **df[i]
		}
		*df[i] = &n
	}
	return nil
}

func validateUsage(u Usage) error {
	if (u.ClientVersion != "" && !clientVersion.MatchString(u.ClientVersion)) || (u.ReportedModel != "" && !safeModel(u.ReportedModel)) {
		return fmt.Errorf("invalid native metadata")
	}
	validateTokens := func(v TokenUsage) error {
		switch v.Source + "/" + v.Scope {
		case "/", "codex.turn.completed/session_total", "claude.result/reported_total", "claude.result.modelUsage/session_total":
		default:
			return fmt.Errorf("invalid native token source")
		}
		for _, f := range tokenFields(&v) {
			if *f != nil && (**f < 0 || **f > maxTokenCount) {
				return fmt.Errorf("invalid metrics token count")
			}
		}
		if v.ReasoningOutputTokens != nil && v.OutputTokens != nil && *v.ReasoningOutputTokens > *v.OutputTokens {
			return fmt.Errorf("invalid reasoning token subset")
		}
		if v.Source == "codex.turn.completed" && v.CachedInputTokens != nil && v.InputTokens != nil && *v.CachedInputTokens > *v.InputTokens {
			return fmt.Errorf("invalid cached token subset")
		}
		return nil
	}
	if u.Aggregate != nil {
		if err := validateTokens(*u.Aggregate); err != nil {
			return err
		}
	}
	for model, v := range u.Models {
		if !safeModel(model) {
			return fmt.Errorf("invalid native model")
		}
		if err := validateTokens(v); err != nil {
			return err
		}
	}
	if u.Context != nil {
		c := u.Context
		if c.Scope != "request_context" || (c.Source != "claude.assistant.message" && c.Source != "claude.result.modelUsage") {
			return fmt.Errorf("invalid context source")
		}
		for _, f := range []*int64{c.InputTokens, c.CachedInputTokens, c.CacheCreationInputTokens, c.Tokens, c.Window} {
			if f != nil && (*f < 0 || *f > maxTokenCount) {
				return fmt.Errorf("invalid context count")
			}
		}
	}
	for _, f := range []*int64{u.DurationMS, u.APIDurationMS, u.Turns, u.PeakContextTokens} {
		if f != nil && (*f < 0 || *f > maxTokenCount) {
			return fmt.Errorf("invalid metric counter")
		}
	}
	for _, n := range []int64{u.Compactions, u.Retries, u.CompletedTurns} {
		if n < 0 || n > maxRecords {
			return fmt.Errorf("invalid observed event count")
		}
	}
	if u.EstimatedCostUSD != nil {
		b, err := json.Marshal(*u.EstimatedCostUSD)
		if err != nil {
			return err
		}
		if _, ok := parseNumber(b, 1e9); !ok {
			return fmt.Errorf("invalid estimated cost")
		}
	}
	for window, r := range u.RateLimits {
		switch window {
		case "five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "overage":
		default:
			return fmt.Errorf("invalid quota window")
		}
		switch r.Status {
		case "allowed", "allowed_warning", "rejected":
		default:
			return fmt.Errorf("invalid quota status")
		}
		if r.Source != "claude.rate_limit_event" {
			return fmt.Errorf("invalid quota source")
		}
		if r.Utilization != nil {
			b, err := json.Marshal(*r.Utilization)
			if err != nil {
				return err
			}
			if _, ok := parseNumber(b, 1); !ok {
				return fmt.Errorf("invalid quota utilization")
			}
		}
		if r.ResetsAt != nil && (*r.ResetsAt < 0 || *r.ResetsAt > maxTokenCount) {
			return fmt.Errorf("invalid quota reset")
		}
	}
	switch u.Completeness {
	case "", "observed", "partial", "incomplete", "unavailable":
	default:
		return fmt.Errorf("invalid completeness")
	}
	return nil
}
