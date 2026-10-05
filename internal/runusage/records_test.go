package runusage

import (
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/headroom"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dir(t *testing.T) string {
	t.Helper()
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func attempt(i int, role, session string, input int64) Attempt {
	start := time.Date(2026, 10, 6, 12, 0, i, 0, time.UTC)
	return Attempt{Version: Version, RunID: "run-one", Attempt: i, Role: role, Provider: "codex", Model: "model", ImageID: "image-one", SessionID: session, OptimizerMode: "native", StartedAt: start, EndedAt: start.Add(time.Second), Outcome: "completed", Usage: Usage{Completeness: "observed", Aggregate: &TokenUsage{InputTokens: n(input), CachedInputTokens: n(input / 2), OutputTokens: n(input / 10)}}}
}
func TestResumedDeltaAndRoleRetention(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	a = attempt(2, "repair", "session", 150)
	a.Resumed = true
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	a = attempt(3, "reviewer", "review-session", 30)
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSummary(d, "run-one", 4)
	if e != nil {
		t.Fatal(e)
	}
	if s.Attempts != 3 || s.Missing != 1 || s.UnknownBaseline != 0 || s.ElapsedMS != 3000 {
		t.Fatalf("bad coverage %+v", s)
	}
	for _, g := range s.Groups {
		if g.Role == "repair" && *g.Tokens.InputTokens != 50 {
			t.Fatalf("resume counted twice %+v", g)
		}
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "review-session") || strings.Contains(string(b), "session_id") {
		t.Fatal("summary exposed native identifier")
	}
}
func TestUnknownResumeAndChangedModel(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	a.Resumed = true
	_ = SaveAttempt(d, a)
	a = attempt(2, "repair", "session", 150)
	a.Resumed = true
	_ = SaveAttempt(d, a)
	a = attempt(3, "repair", "session", 200)
	a.Resumed = true
	a.Model = "other"
	_ = SaveAttempt(d, a)
	s, e := LoadSummary(d, "run-one", 3)
	if e != nil {
		t.Fatal(e)
	}
	if s.UnknownBaseline != 2 {
		t.Fatalf("invented baseline %+v", s)
	}
	for _, g := range s.Groups {
		if g.Role == "repair" && g.Model == "model" && *g.Tokens.InputTokens != 50 {
			t.Fatalf("known later checkpoint lost %+v", g)
		}
	}
}
func TestStartedRecordPrivateReplacementAndSymlink(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	finished := a
	a.Outcome = "running"
	a.EndedAt = time.Time{}
	a.Usage = Usage{}
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSummary(d, "run-one", 1)
	if e != nil || s.Pending != 1 || s.Incomplete != 1 {
		t.Fatalf("start coverage %+v %v", s, e)
	}
	if e = SaveAttempt(d, finished); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "metrics", recordName(a))
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private mode %v", info.Mode())
	}
	info, _ = os.Stat(filepath.Join(d, "metrics"))
	if info.Mode().Perm() != 0700 {
		t.Fatal("metrics directory not private")
	}
	_ = os.Remove(p)
	target := filepath.Join(d, "other")
	_ = os.WriteFile(target, []byte("untouched"), 0600)
	_ = os.Symlink(target, p)
	if SaveAttempt(d, finished) == nil {
		t.Fatal("followed symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "untouched" {
		t.Fatal("symlink target modified")
	}
}
func TestIncompleteAttemptAndIdentityBounds(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	a.Outcome = "failed"
	a.Usage.Completeness = "incomplete"
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSummary(d, "run-one", 1)
	if e != nil || s.Failed != 1 || s.Incomplete != 1 || s.Groups[0].Tokens.InputTokens != nil {
		t.Fatalf("crash advertised savings %+v %v", s, e)
	}
	a.Role = "../other"
	if SaveAttempt(d, a) == nil {
		t.Fatal("unsafe filename accepted")
	}
	a.Role = "implementation"
	a.Attempt = 4097
	if SaveAttempt(d, a) == nil {
		t.Fatal("unbounded attempts accepted")
	}
}

// A partial cumulative checkpoint cannot turn the next resume into a measured
// interval; omitted output may conceal usage between the two checkpoints.
func TestPartialResumeDoesNotEstablishBaseline(t *testing.T) {
	d := dir(t)
	for i := 1; i <= 3; i++ {
		a := attempt(i, "implementation", "session", int64(i*100))
		a.Resumed = i > 1
		if i == 2 {
			a.Usage.Aggregate.OutputTokens = nil
		}
		if e := SaveAttempt(d, a); e != nil {
			t.Fatal(e)
		}
	}
	s, e := LoadSummary(d, "run-one", 3)
	if e != nil {
		t.Fatal(e)
	}
	g := s.Groups[0]
	if g.MeasuredAttempts != 1 || g.UnknownAttempts != 2 || s.UnknownBaseline != 1 || *g.Tokens.InputTokens != 100 {
		t.Fatalf("partial checkpoints counted: %+v %+v", s, g)
	}
}

func TestOptionalCategoriesRequireEveryMeasuredContribution(t *testing.T) {
	for _, missingFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-later", true: "missing-first"}[missingFirst], func(t *testing.T) {
			d := dir(t)
			for i := 1; i <= 2; i++ {
				a := attempt(i, "implementation", "", 100)
				if (i == 1) != missingFirst {
					a.Usage.Aggregate.ReasoningOutputTokens = n(3)
					a.Usage.Aggregate.CacheWriteInputTokens = n(4)
				}
				if e := SaveAttempt(d, a); e != nil {
					t.Fatal(e)
				}
			}
			s, e := LoadSummary(d, "run-one", 2)
			if e != nil {
				t.Fatal(e)
			}
			g := s.Groups[0]
			if g.MeasuredAttempts != 2 || *g.Tokens.InputTokens != 200 || g.Tokens.ReasoningOutputTokens != nil || g.Tokens.CacheWriteInputTokens != nil {
				t.Fatalf("partial categories advertised: %+v", g)
			}
		})
	}
}

func TestOptionalResumedCategoryWithUnknownBaseline(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	a = attempt(2, "repair", "session", 200)
	a.Resumed = true
	a.Usage.Aggregate.ReasoningOutputTokens = n(3)
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSummary(d, "run-one", 2)
	if e != nil {
		t.Fatal(e)
	}
	for _, g := range s.Groups {
		if g.Role == "repair" && (g.MeasuredAttempts != 1 || *g.Tokens.InputTokens != 100 || g.Tokens.ReasoningOutputTokens != nil) {
			t.Fatalf("invented optional baseline: %+v", g)
		}
	}
}

func TestClaudeResetDoesNotPartlyApplyModels(t *testing.T) {
	d := dir(t)
	for i := 1; i <= 2; i++ {
		a := attempt(i, "implementation", "session", 100)
		a.Provider = "claude"
		a.Resumed = i > 1
		a.Usage.Aggregate = nil
		a.Usage.Models = map[string]TokenUsage{
			"main":  {InputTokens: n(int64(i * 100)), CachedInputTokens: n(0), CacheCreationInputTokens: n(0), OutputTokens: n(int64(i * 10))},
			"child": {InputTokens: n(100), CachedInputTokens: n(0), CacheCreationInputTokens: n(0), OutputTokens: n(10)},
		}
		if i == 2 {
			v := a.Usage.Models["child"]
			v.OutputTokens = n(1)
			a.Usage.Models["child"] = v
		}
		if e := SaveAttempt(d, a); e != nil {
			t.Fatal(e)
		}
	}
	s, e := LoadSummary(d, "run-one", 2)
	if e != nil {
		t.Fatal(e)
	}
	g := s.Groups[0]
	if g.MeasuredAttempts != 1 || g.UnknownAttempts != 1 || *g.Tokens.InputTokens != 200 || *g.Models["main"].InputTokens != 100 || *g.Models["child"].OutputTokens != 10 {
		t.Fatalf("partial model delta applied: %+v", g)
	}
}

func TestPrivateMetadataRejectsFreeText(t *testing.T) {
	for name, mutate := range map[string]func(*Attempt){
		"provider":       func(a *Attempt) { a.Provider = "secret" },
		"effort":         func(a *Attempt) { a.Effort = "private-token" },
		"optimizer":      func(a *Attempt) { a.OptimizerMode = "private-token" },
		"version":        func(a *Attempt) { a.ClientVersion = "private-token" },
		"usage-version":  func(a *Attempt) { a.Usage.ClientVersion = "private-token" },
		"token-source":   func(a *Attempt) { a.Usage.Aggregate.Source = "private-token" },
		"token-scope":    func(a *Attempt) { a.Usage.Aggregate.Scope = "private-token" },
		"context-source": func(a *Attempt) { a.Usage.Context = &ContextUsage{Source: "private-token", Scope: "request_context"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := attempt(1, "implementation", "session", 100)
			mutate(&a)
			if SaveAttempt(dir(t), a) == nil {
				t.Fatal("accepted private free-text metadata")
			}
		})
	}
}

func TestProxyMetadataAndUnknownCategories(t *testing.T) {
	valid := headroom.Stats{Version: headroom.Version, Mode: "optimize", Source: "headroom_proxy_aggregate", Requests: n(1), InputTokens: n(100), OutputTokens: n(5), SavedTokens: n(20), Complete: true}
	for name, mutate := range map[string]func(*headroom.Stats){
		"source":                  func(s *headroom.Stats) { s.Source = "private-token" },
		"version":                 func(s *headroom.Stats) { s.Version = "private-token" },
		"mode":                    func(s *headroom.Stats) { s.Mode = "passthrough" },
		"error":                   func(s *headroom.Stats) { s.Error = "private-token" },
		"complete":                func(s *headroom.Stats) { s.SavedTokens = nil },
		"partial":                 func(s *headroom.Stats) { s.Complete = false },
		"bounded":                 func(s *headroom.Stats) { s.Requests = n(maxTokenCount) },
		"unavailable-with-values": func(s *headroom.Stats) { s.Error = "proxy metrics unavailable" },
	} {
		t.Run(name, func(t *testing.T) {
			a := attempt(1, "implementation", "", 100)
			a.OptimizerMode = "optimize"
			st := valid
			mutate(&st)
			a.HeadroomStats = &st
			if SaveAttempt(dir(t), a) == nil {
				t.Fatal("accepted invalid proxy metadata")
			}
		})
	}
	d := dir(t)
	for i := 1; i <= 3; i++ {
		a := attempt(i, "implementation", "", 100)
		a.OptimizerMode = "optimize"
		st := valid
		if i == 2 {
			st.OutputTokens = nil
			st.Complete = false
		}
		a.HeadroomStats = &st
		if e := SaveAttempt(d, a); e != nil {
			t.Fatal(e)
		}
	}
	s, e := LoadSummary(d, "run-one", 3)
	if e != nil {
		t.Fatal(e)
	}
	p := s.Groups[0].ProxyStats
	if p.Source != "headroom_proxy_aggregate" || p.ObservedAttempts != 3 || *p.InputTokens != 300 || p.OutputTokens != nil {
		t.Fatalf("partial proxy categories summed: %+v", p)
	}
	a := attempt(4, "implementation", "", 100)
	a.OptimizerMode = "optimize"
	a.HeadroomStats = &headroom.Stats{Version: headroom.Version, Mode: "optimize", Source: "headroom_proxy_aggregate", Error: "proxy metrics unavailable"}
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e = LoadSummary(d, "run-one", 4)
	if e != nil {
		t.Fatal(e)
	}
	p = s.Groups[0].ProxyStats
	if p.MissingAttempts != 1 || p.InputTokens != nil || p.Requests != nil || p.OutputTokens != nil || p.SavedTokens != nil {
		t.Fatalf("missing proxy stats became zero: %+v", p)
	}
}

func TestMetricsDirectorySymlinkRejected(t *testing.T) {
	d := dir(t)
	target := dir(t)
	if e := os.Symlink(target, filepath.Join(d, "metrics")); e != nil {
		t.Fatal(e)
	}
	if SaveAttempt(d, attempt(1, "implementation", "", 100)) == nil {
		t.Fatal("wrote through metrics directory symlink")
	}
	if _, e := LoadSummary(d, "run-one", 1); e == nil {
		t.Fatal("read through metrics directory symlink")
	}
	entries, e := os.ReadDir(target)
	if e != nil || len(entries) != 0 {
		t.Fatalf("symlink target modified: %v %v", entries, e)
	}
}

func TestClaudeOptionalCategoriesRequireEveryModel(t *testing.T) {
	d := dir(t)
	a := attempt(1, "implementation", "session", 100)
	a.Provider = "claude"
	a.Usage.Aggregate = nil
	a.Usage.Models = map[string]TokenUsage{
		"main":  {InputTokens: n(100), CachedInputTokens: n(0), CacheCreationInputTokens: n(0), OutputTokens: n(10), ReasoningOutputTokens: n(3), CacheWriteInputTokens: n(5)},
		"child": {InputTokens: n(50), CachedInputTokens: n(0), CacheCreationInputTokens: n(0), OutputTokens: n(5)},
	}
	if e := SaveAttempt(d, a); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSummary(d, "run-one", 1)
	if e != nil {
		t.Fatal(e)
	}
	g := s.Groups[0]
	if g.MeasuredAttempts != 1 || *g.Tokens.InputTokens != 150 || g.Tokens.ReasoningOutputTokens != nil || g.Tokens.CacheWriteInputTokens != nil || *g.Models["main"].ReasoningOutputTokens != 3 {
		t.Fatalf("unknown child categories became whole tree counts: %+v", g)
	}
}
