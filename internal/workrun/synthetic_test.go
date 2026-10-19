package workrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSyntheticRunnerKeepsCodexReviewAndRepairSession(t *testing.T) {
	directory, journal := testRun(t)
	journal.Plan.Roles = DefaultModels().Synthetic
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	if journal.State != "ready" || len(provider.calls) != 3 {
		t.Fatalf("Synthetic lifecycle stopped: %+v", journal)
	}
	if provider.calls[0].Model.Provider != "synthetic" || provider.calls[1].ResumeID == "" || provider.calls[1].ResumeID != journal.SessionID || provider.calls[2].Model.Provider != "codex" || provider.calls[2].ResumeID != "" {
		t.Fatal("implementation/resume/review selection lost")
	}
	if _, err := Load(directory); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticDefaultsAndLegacyModelSettings(t *testing.T) {
	dir := t.TempDir()
	// Older settings retain their overrides while inheriting the new provider.
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"version":1,"codex":{"implementation":{"model":"custom-codex"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	defaults, err := LoadModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Codex.Implementation.Name != "custom-codex" {
		t.Fatal("legacy model override lost")
	}
	roles, err := ResolveModels(defaults, "synthetic", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if roles.Implementation != (Model{"synthetic", "hf:moonshotai/Kimi-K3", "default"}) || roles.Review.Provider != "codex" {
		t.Fatalf("unexpected Synthetic roles: %+v", roles)
	}
	if _, err := ResolveModels(defaults, "synthetic", "", "high", "", ""); err == nil {
		t.Fatal("invented reasoning effort accepted")
	}
	if _, err := ResolveSpeeds(roles, "fast", "normal"); err == nil {
		t.Fatal("unsupported fast selection accepted")
	}
}

func TestSyntheticNativeHandoffAndResumeIdentity(t *testing.T) {
	w := &eventWriter{provider: "synthetic"}
	outcome := testOutcome("implemented")
	encoded, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": "ses_example123", "model": "hf:moonshotai/Kimi-K3", "structured_output": outcome})
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("implementation"); err != nil {
		t.Fatal(err)
	}
	if w.result.SessionID != "ses_example123" || w.result.ReportedModel != "hf:moonshotai/Kimi-K3" {
		t.Fatalf("native identity lost: %+v", w.result)
	}
	if _, err := w.Write([]byte(`{"type":"result","subtype":"success","session_id":"ses_example123","model":"other-model"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("implementation"); err == nil {
		t.Fatal("model change accepted")
	}
}
