package runstatus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestTakeoverDiagnosticShowsStateWithoutPrivateSession(t *testing.T) {
	id := strings.Repeat("a", 24)
	j := workrun.Journal{ID: id, State: "failed", ResumeState: "implementing", ProviderTakeover: &workrun.ProviderTakeover{PreviousModel: workrun.Model{Provider: "claude"}, NewModel: workrun.Model{Provider: "codex"}, Reviewer: workrun.Model{Provider: "codex"}, PreviousSessionID: "private-session", Reason: "private-reason"}}
	d, err := DiagnosticSnapshot(View{Available: true, ID: id, Directory: t.TempDir(), Journal: &j})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-session") || strings.Contains(string(data), "private-reason") || d.ProviderTakeover == nil || d.NextAction.Category != "ordinary_resume" {
		t.Fatal("unsafe or incomplete takeover diagnostic")
	}
	j.ProviderTakeover.StartedAttempt = 2
	if NextRunAction(j, false).Category != "inspect" {
		t.Fatal("unknown interrupted session was offered automatic resume")
	}
	if NextRunAction(j, true).Category != "observe" {
		t.Fatal("live controller was not prioritised")
	}
}
