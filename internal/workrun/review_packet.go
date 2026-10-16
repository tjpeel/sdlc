package workrun

import "encoding/json"

// Implementation self-review must not populate the independent review report.
// Restrict the native output schema as well as validating the returned role.
var implementationOutcomeSchema = func() string {
	var schema map[string]any
	if err := json.Unmarshal([]byte(outcomeSchema), &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	properties["recommendations"].(map[string]any)["maxItems"] = 0
	properties["recommendations"].(map[string]any)["description"] = "Implementation role: return []. Put self-review recommendations in summary."
	properties["review_coverage"] = map[string]any{"type": "null", "description": "Implementation role: return null. Put self-review inspection evidence in summary."}
	properties["status"].(map[string]any)["enum"] = []string{"implemented", "checks_requested", "diagnostics_requested", "waiting_for_human", "blocked", "failed"}
	data, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(data)
}()

type reviewCIEvidence struct {
	Status string `json:"status"`
}

// Reuse the shared field definitions while omitting implementation-only work
// requests and PR metadata from the fresh review's structured output contract.
var reviewOutcomeSchema = func() string {
	var schema map[string]any
	if err := json.Unmarshal([]byte(outcomeSchema), &schema); err != nil {
		panic(err)
	}
	all := schema["properties"].(map[string]any)
	properties := map[string]any{}
	keys := []string{"status", "summary", "questions", "findings", "limitations", "repository_requests", "recommendations", "review_coverage"}
	for _, key := range keys {
		properties[key] = all[key]
	}
	status := properties["status"].(map[string]any)
	status["enum"] = []string{"reviewed", "review_incomplete", "checks_requested", "waiting_for_human", "blocked", "failed"}
	for _, variant := range properties["review_coverage"].(map[string]any)["anyOf"].([]any) {
		object := variant.(map[string]any)
		if object["type"] != "object" {
			continue
		}
		fields := object["properties"].(map[string]any)
		fields["inspection"] = map[string]any{
			"type": "array", "description": "One entry per changed file, plus material unchanged supporting paths. Complete means inspected substantive bodies, assertions and fixtures; inventories or selected snippets are incomplete. Evidence states what was inspected or exactly what remains. Keep prose, gaps and these entries consistent.",
			"items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "complete", "evidence"}, "properties": map[string]any{"path": map[string]any{"type": "string"}, "complete": map[string]any{"type": "boolean"}, "evidence": map[string]any{"type": "string"}}},
		}
		object["required"] = append(object["required"].([]any), "inspection")
	}
	properties["repository_requests"].(map[string]any)["description"] = "Request missing read-only companion source with checks_requested. Return [] when unused."
	schema["properties"], schema["required"] = properties, keys
	data, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(data)
}()

// reviewRequirements retains requirement identity and relationships, without
// repeating implementation launch, authentication and publication settings.
type reviewRequirements struct {
	Reference         string             `json:"reference"`
	Ticket            string             `json:"ticket"`
	Repository        string             `json:"repository"`
	StartingSHA       string             `json:"starting_sha"`
	SourceSHA         string             `json:"source_sha"`
	Inputs            []Input            `json:"inputs"`
	CheckInputs       []Input            `json:"check_inputs"`
	Derived           *DerivedBoundary   `json:"derived,omitempty"`
	DependencyContext *DependencyContext `json:"dependency_context,omitempty"`
}

func selectedReviewRequirements(p Plan) reviewRequirements {
	return reviewRequirements{p.Reference, p.Ticket, p.Repository, p.StartingSHA, p.SourceSHA, p.Inputs, p.CheckInputs, p.Derived, p.DependencyContext}
}

// The reviewer receives current comment revisions and acknowledgements, rather
// than replaying superseded revisions and provisional assessment history.
func currentReviewFeedback(history *PRFeedbackHistory) *PRFeedbackHistory {
	if history == nil {
		return nil
	}
	latest := map[string]int{}
	for i, entry := range history.Entries {
		latest[entry.Comment.ID] = i
	}
	packet := *history
	packet.Entries = nil
	for i, entry := range history.Entries {
		if latest[entry.Comment.ID] != i {
			continue
		}
		entry.AssessmentHistory = nil
		entry.StartedAttempt = 0
		entry.ReplyBody = ""
		entry.Marker = ""
		entry.Reply = nil
		packet.Entries = append(packet.Entries, entry)
	}
	return &packet
}

// Omit ignored, resolved and deleted comment bodies from provider context while retaining
// their full evidence and prior revisions in the private journal.
func feedbackForPrompt(history *PRFeedbackHistory) *PRFeedbackHistory {
	if history == nil {
		return nil
	}
	latest := map[string]PRFeedbackComment{}
	for _, e := range history.Entries {
		latest[e.Comment.ID] = e.Comment
	}
	packet := *history
	packet.Entries = nil
	for _, e := range history.Entries {
		current := latest[e.Comment.ID]
		if current.Ignored == nil && !current.Resolved && !current.Deleted {
			packet.Entries = append(packet.Entries, e)
		}
	}
	return &packet
}
