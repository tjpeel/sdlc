package workrun

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

const structuredPRBody = "Ticket: TASK-1\n\n## What\n\nExpose a stored response value.\n\n## Why\n\nConsumers need the current value.\n\n## Implementation\n\nMap the stored field into the response.\n\n## Testing approach\n\nAssert a deliberately non-default stored value.\n\n## Verification\n\nThe candidate passes; the original mapper fails the assertion.\n\n## Data and rollout\n\nNot applicable.\n\n## Scope and follow-up\n\nNot applicable.\n"

func structuredPRPlan() Plan {
	return Plan{Reference: "TASK-1", PRTitle: "TASK-1: Expose stored response value", PRBody: structuredPRBody, PRMetadataPolicy: DefaultPRMetadataPolicy()}
}

func TestPRMetadataRetainsRequiredStructureAndExactReference(t *testing.T) {
	if err := ValidatePRMetadata(structuredPRPlan()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, title, body string
	}{
		{"reference only in prose", "TASK-1: Expose value", strings.Replace(structuredPRBody, "Ticket: TASK-1", "TASK-1 exposes a value", 1)},
		{"wrong reference", "TASK-1: Expose value", strings.Replace(structuredPRBody, "Ticket: TASK-1", "Ticket: TASK-10", 1)},
		{"reference only in code", "TASK-1: Expose value", strings.Replace(structuredPRBody, "Ticket: TASK-1", "```text\nTicket: TASK-1\n```", 1)},
		{"reference only in comment", "TASK-1: Expose value", strings.Replace(structuredPRBody, "Ticket: TASK-1", "<!-- Ticket: TASK-1 -->", 1)},
		{"reference not title prefix", "Expose value for TASK-1", structuredPRBody},
		{"empty title outcome", "TASK-1: ", structuredPRBody},
		{"missing heading", "TASK-1: Expose value", strings.Replace(structuredPRBody, "## Why", "Why", 1)},
		{"renamed heading", "TASK-1: Expose value", strings.Replace(structuredPRBody, "## Why", "## Motivation", 1)},
		{"duplicate heading", "TASK-1: Expose value", structuredPRBody + "\n## What\n\nDuplicate.\n"},
		{"empty section", "TASK-1: Expose value", strings.Replace(structuredPRBody, "Consumers need the current value.", "<!-- hidden explanation -->", 1)},
		{"headings only in fence", "TASK-1: Expose value", "Ticket: TASK-1\n\n```markdown\n" + strings.TrimPrefix(structuredPRBody, "Ticket: TASK-1\n\n") + "```\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := structuredPRPlan()
			plan.PRTitle, plan.PRBody = tc.title, tc.body
			if err := ValidatePRMetadata(plan); err == nil {
				t.Fatal("malformed PR metadata was accepted")
			}
		})
	}
}

func TestPRMetadataIgnoresExamplesInsideFencesAndComments(t *testing.T) {
	plan := structuredPRPlan()
	plan.PRBody = strings.Replace(plan.PRBody, "Map the stored field into the response.", "Map the stored field into the response.\n\n````markdown\n## Example heading\n```\n````\n<!--\n## Hidden heading\n-->", 1)
	if err := ValidatePRMetadata(plan); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFreezesRepositoryPRTemplate(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".github/PULL_REQUEST_TEMPLATE.md", "<!-- Guidance -->\n## Change\n\nDescribe the change.\n\n## Evidence\n\nDescribe observed evidence.\n")
	destination := filepath.Join(realTemp(t), "workspace")
	plan, err := Capture(context.Background(), launch, destination, "selected-work", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PRMetadataPolicy == nil || plan.PRMetadataPolicy.TemplatePath != ".github/PULL_REQUEST_TEMPLATE.md" {
		t.Fatal("repository template was not frozen")
	}
	plan.PRTitle = "JOB:1: Expose stored response value"
	plan.PRBody = "Work reference: JOB:1\n\n## Change\n\nExpose stored response value.\n\n## Evidence\n\nCandidate succeeds and original mapper fails.\n"
	if err = ValidatePRMetadata(plan); err != nil {
		t.Fatal(err)
	}
	plan.PRBody = strings.Replace(plan.PRBody, "## Evidence", "## Verification", 1)
	if err = ValidatePRMetadata(plan); err == nil {
		t.Fatal("default heading replaced the repository template heading")
	}
}

func TestRepositoryTemplateRetainsMixedHeadingLevels(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".github/pull_request_template.md", "# Summary\n\nDescribe the outcome.\n\n### Test plan\n\nDescribe the proof.\n")
	plan, err := Capture(context.Background(), launch, filepath.Join(realTemp(t), "workspace"), "selected-work", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	plan.PRTitle = "JOB:1: Expose stored response value"
	plan.PRBody = "Work reference: JOB:1\n\n# Summary\n\nExpose stored response value.\n\n### Test plan\n\nCandidate succeeds and original mapper fails.\n"
	if err = ValidatePRMetadata(plan); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"Work reference: JOB:1", "Work reference: JOB:1\n\n# Summary\n\nExpose stored response value."} {
		plan.PRBody = body
		if err = ValidatePRMetadata(plan); err == nil {
			t.Fatal("missing template content was accepted")
		}
	}
}

func TestPRMetadataCorrectionReusesPassingChecks(t *testing.T) {
	dir, journal := testRun(t)
	journal.Plan.PRMetadataPolicy = DefaultPRMetadataPolicy()
	valid := testOutcome("implemented")
	valid.PRTitle, valid.PRBody = structuredPRPlan().PRTitle, structuredPRBody
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), valid, testOutcome("reviewed")}}
	checks, publisher := &fakeChecker{}, &fakePublisher{}
	if err := fakeRunner(provider, checks, publisher, &fakeRepository{}).Run(context.Background(), dir, journal, ""); err != nil {
		t.Fatal(err)
	}
	if journal.State != "ready" || checks.calls != 1 || publisher.calls != 1 || journal.PRMetadataCorrections != 1 {
		t.Fatalf("metadata correction repeated checks or publication: state=%s checks=%d publication=%d corrections=%d", journal.State, checks.calls, publisher.calls, journal.PRMetadataCorrections)
	}
	if len(provider.calls) != 4 || !strings.Contains(provider.calls[2].Prompt, "Correct only the PR metadata") || provider.calls[2].ResumeID != testNative {
		t.Fatal("metadata correction lost its original native session or specific feedback")
	}
}

func TestHeadinglessTemplateRequiresVisibleDescription(t *testing.T) {
	root, launch := sourceFixture(t)
	sourceWrite(t, root, ".github/pull_request_template.md", "**Summary:**\n\n- [ ] Regression verified\n")
	plan, err := Capture(context.Background(), launch, filepath.Join(realTemp(t), "workspace"), "selected-work", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	plan.PRTitle = "JOB:1: Expose stored response value"
	for _, body := range []string{"Work reference: JOB:1", "Work reference: JOB:1\n\n<!-- No actual description -->"} {
		plan.PRBody = body
		if err = ValidatePRMetadata(plan); err == nil {
			t.Fatal("reference-only template response was accepted")
		}
	}
	plan.PRBody = "Work reference: JOB:1\n\n**Summary:** Expose the stored response value.\n\n- [x] Regression verified\n"
	if err = ValidatePRMetadata(plan); err != nil {
		t.Fatal(err)
	}
}

func TestPRMetadataCannotReachPublicationCommands(t *testing.T) {
	plan := structuredPRPlan()
	plan.PRBody = "TASK-1 exposes a stored value."
	called := false
	publisher := GitHubPublisher{Command: func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, errors.New("publication command must not run")
	}}
	if _, err := publisher.Publish(context.Background(), plan, "", t.TempDir(), Publication{}, io.Discard); err == nil || called {
		t.Fatal("invalid metadata reached publication commands")
	}
	if _, err := (DockerPublisher{}).Publish(context.Background(), plan, "", t.TempDir(), Publication{}, io.Discard); err == nil || !strings.Contains(err.Error(), "PR body") {
		t.Fatal("isolated publisher did not reject metadata before reading or starting the runtime")
	}
}

func TestLegacyRunRetainsOriginalPRMetadataContract(t *testing.T) {
	plan := Plan{Reference: "TASK-1", PRTitle: "TASK-1 selected work", PRBody: "Selected work complete."}
	if err := ValidatePRMetadata(plan); err != nil {
		t.Fatal("older saved run was assigned a new publication contract", err)
	}
}
