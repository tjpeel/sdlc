package workrun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type modelSelectionProvider struct {
	calls []Session
	fail  bool
}

func (p *modelSelectionProvider) Status(context.Context, string) (string, error) {
	return "stored", nil
}
func (p *modelSelectionProvider) Execute(_ context.Context, s Session, _, _ io.Writer) (SessionResult, error) {
	p.calls = append(p.calls, s)
	if p.fail {
		return SessionResult{SessionID: s.ResumeID, ReportedModel: s.Model.Name}, errors.New("interrupted")
	}
	return SessionResult{SessionID: s.ResumeID, ReportedModel: s.Model.Name, Outcome: testOutcome("checks_requested")}, nil
}

func TestResumeModelKeepsNativeSessionAndPersistsAcrossFailure(t *testing.T) {
	dir, old := takeoverFixture(t)
	j := grantTakeover(t, dir, old)
	first := &takeoverTestProvider{fail: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = first
	_ = r.Run(context.Background(), dir, &j, "")
	if j.SessionID != takeoverSession {
		t.Fatal("missing takeover session")
	}
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", "context canceled"
	j.ReportedModel = "gpt-6.1-sol"
	if err := Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	priorPlan, reviewer := j.Plan.Roles.Implementation, j.Plan.Roles.Review
	selected := Model{Provider: "codex", Name: "gpt-6.1-terra", Effort: "high"}
	if err := RecordResumeModel(dir, j.UpdatedAt, selected); err != nil {
		t.Fatal(err)
	}
	j, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.Plan.Roles.Implementation != priorPlan || j.Plan.Roles.Review != reviewer || j.SessionID != takeoverSession || j.ReportedModel != "" || j.ResumeModelSelections[0].PreviousReportedModel != "gpt-6.1-sol" {
		t.Fatal("selection lost prior identity or changed review")
	}
	p := &modelSelectionProvider{fail: true}
	r.Provider = p
	_ = r.Run(context.Background(), dir, &j, "")
	if len(p.calls) != 1 || p.calls[0].ResumeID != takeoverSession || p.calls[0].Model != selected || j.ReportedModel != selected.Name || j.ResumeModelSelections[0].FirstAttempt != j.Attempt {
		t.Fatal("selected model was not requested in the same session")
	}
	j.State, j.ResumeState, j.StopReason = "failed", "implementing", "context canceled"
	if err := Save(dir, &j); err != nil {
		t.Fatal(err)
	}
	j, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.calls = nil
	_ = r.Run(context.Background(), dir, &j, "")
	if len(p.calls) != 1 || p.calls[0].Model != selected || p.calls[0].ResumeID != takeoverSession || j.ResumeModelSelections[0].FirstAttempt == j.Attempt {
		t.Fatal("interrupted retry lost model or session")
	}
}

func TestResumeModelEligibilityAndValidation(t *testing.T) {
	_, j := takeoverFixture(t)
	selected := Model{Provider: "claude", Name: "claude-opus-5-5", Effort: "high"}
	if err := ValidateResumeModel(*j, selected); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Journal)
		model  Model
	}{
		{"provider change", func(*Journal) {}, Model{Provider: "codex", Name: "gpt-6.1-sol", Effort: "low"}},
		{"invalid effort", func(*Journal) {}, Model{Provider: "claude", Name: "claude-opus-5-5", Effort: "invalid"}},
		{"invalid model", func(*Journal) {}, Model{Provider: "claude", Name: "bad name", Effort: "high"}},
		{"usage hold", func(j *Journal) { j.StopReason = "context canceled\nusage limit" }, selected},
		{"retained policy hold", func(j *Journal) { j.Outcome.Status = "blocked" }, selected},
		{"missing session", func(j *Journal) { j.SessionID = "" }, selected},
		{"review question", func(j *Journal) {
			j.State = "waiting_for_human"
			j.PendingRole = "review"
			j.Outcome.Questions = []string{"question"}
		}, selected},
		{"other operational hold", func(j *Journal) { j.State = "blocked"; j.StopReason = "access denied" }, selected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := *j
			tt.mutate(&copy)
			if err := ValidateResumeModel(copy, tt.model); err == nil {
				t.Fatal("unsafe selection accepted")
			}
		})
	}
	question := *j
	question.State = "waiting_for_human"
	question.PendingRole = "implementation"
	question.Outcome.Questions = []string{"question"}
	if err := ValidateResumeModel(question, selected); err != nil {
		t.Fatal(err)
	}
	cancelled := *j
	cancelled.State = "blocked"
	cancelled.StopReason = "run was cancelled or timed out; checkpoint retained"
	if err := ValidateResumeModel(cancelled, selected); err != nil {
		t.Fatal(err)
	}
	selected.Name = strings.Repeat("a", 257)
	if ValidateResumeModel(*j, selected) == nil {
		t.Fatal("oversized model accepted")
	}
}

func TestResumeModelClaudeUsesOriginalSession(t *testing.T) {
	dir, j := takeoverFixture(t)
	selected := Model{Provider: "claude", Name: "claude-opus-5-5", Effort: "high"}
	if err := RecordResumeModel(dir, j.UpdatedAt, selected); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &modelSelectionProvider{fail: true}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.Provider = p
	_ = r.Run(context.Background(), dir, &saved, "")
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || p.calls[0].Model != selected || saved.Plan.Roles.Review.Provider != "codex" {
		t.Fatal("Claude selection replaced session or reviewer")
	}
}
