package workrun

import (
	"encoding/json"
	"github.com/tjpeel/sdlc/internal/project"
	"testing"
)

func TestIndependentReviewRetainsScopeRolesAndProvenance(t *testing.T) {
	p := Plan{Reference: "EXAMPLE", Ticket: "tickets/01-current.md", Inputs: []Input{{Path: "tickets/01-current.md"}, {Path: "tickets/02-later.md"}}, InputContext: []project.RequirementContext{{Path: "tickets/01-current.md", Role: "selected_ticket"}, {Path: "tickets/02-later.md", Role: "boundary_context", ReferencedBy: []string{"tickets/01-current.md"}}}}
	data, err := json.Marshal(selectedReviewRequirements(p))
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Ticket       string                       `json:"ticket"`
		InputContext []project.RequirementContext `json:"input_context"`
	}
	if err = json.Unmarshal(data, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Ticket != p.Ticket || len(packet.InputContext) != 2 || packet.InputContext[1].Role != "boundary_context" || len(packet.InputContext[1].ReferencedBy) != 1 {
		t.Fatal(string(data))
	}
	// Old captured runs stay readable and do not gain inferred authority.
	p.InputContext = nil
	data, err = json.Marshal(selectedReviewRequirements(p))
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	json.Unmarshal(data, &legacy)
	if _, ok := legacy["input_context"]; ok {
		t.Fatal("legacy scope labels were fabricated")
	}
}
