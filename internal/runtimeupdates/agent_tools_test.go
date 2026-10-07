package runtimeupdates

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func agentToolsState(t *testing.T) (statePlan Plan) {
	t.Helper()
	statePlan, err := (Checker{planMetadata()}).Plan(context.Background(), planState(), planRecipe(), packages)
	if err != nil {
		t.Fatal(err)
	}
	return statePlan
}

func TestAgentToolsPreservesOtherPinsWithoutUnrelatedMetadata(t *testing.T) {
	state := planState()
	baseline := agentToolsState(t)
	state.DependencyPins = &baseline.Pins
	beforeArguments := map[string]string{}
	for key, value := range baseline.Pins.Arguments {
		beforeArguments[key] = value
	}
	fixture := newMetadataFixture()
	state.Inventory.Dependencies[0].Version = "0.999.0"
	plan, err := (Checker{fixture}).PlanAgentTools(context.Background(), state, planRecipe())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pins.Arguments["CODEX_VERSION"] != "0.999.0" || plan.Pins.Arguments["CLAUDE_VERSION"] != "2.1.288" || plan.Pins.Arguments["SKILLS_REVISION"] != strings.Repeat("b", 40) {
		t.Fatal(plan.Pins)
	}
	for identity, argument := range managedArguments {
		if agentToolArguments[identity] == "" && plan.Pins.Arguments[argument] != beforeArguments[argument] {
			t.Fatalf("changed %s", argument)
		}
	}
	if !reflect.DeepEqual(baseline.Pins.Arguments, beforeArguments) || !reflect.DeepEqual(plan.Pins.Images, baseline.Pins.Images) || plan.Pins.DaemonImage != baseline.Pins.DaemonImage || plan.Pins.SigningImage != baseline.Pins.SigningImage {
		t.Fatal("changed unrelated pins or mutated input")
	}
	for path := range fixture.calls {
		if !strings.Contains(path, "@openai/codex/") && !strings.Contains(path, "@anthropic-ai/claude-code/") && !strings.Contains(path, "/tjpeel/skills/") && !strings.Contains(path, "/tjpeel/agents/") {
			t.Fatalf("unrelated metadata fetched: %s", path)
		}
	}
}

func TestAgentToolsRefusesDivergedCatalogue(t *testing.T) {
	state := planState()
	baseline := agentToolsState(t)
	state.DependencyPins = &baseline.Pins
	fixture := newMetadataFixture()
	fixture.bodies["api.github.com/repos/tjpeel/skills/compare/"+strings.Repeat("a", 40)+"..."+strings.Repeat("b", 40)] = `{"status":"diverged"}`
	if plan, err := (Checker{fixture}).PlanAgentTools(context.Background(), state, planRecipe()); err == nil || len(plan.Pins.Arguments) != 0 {
		t.Fatal("diverged catalogue accepted", err)
	}

}

func TestAgentToolsCompletesRecordedSourceBaselineWithoutVersionUpdates(t *testing.T) {
	state := planState()
	state.BuildRecipe = string(planRecipe())
	fixture := planMetadata()
	fixture.bodies["registry-1.docker.io/v2/library/docker/manifests/29.8.2-dind"] = `{"schemaVersion":2,"manifests":[]}`
	plan, err := (Checker{fixture}).PlanAgentTools(context.Background(), state, planRecipe())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Pins.DaemonImage, "docker:29.8.2-dind@sha256:") || !strings.HasPrefix(plan.Pins.Images["node"], "node:24.1.0-bookworm@sha256:") || plan.Pins.Arguments["NPM_VERSION"] != "11.1.0" || plan.Pins.Arguments["COMPOSE_VERSION"] != "5.5.1" {
		t.Fatal(plan.Pins)
	}
	for path := range fixture.calls {
		if !strings.Contains(path, "@openai/codex/") && !strings.Contains(path, "@anthropic-ai/claude-code/") && !strings.Contains(path, "/tjpeel/skills/") && !strings.Contains(path, "/tjpeel/agents/") && path != "auth.docker.io/token" && path != "registry-1.docker.io/v2/library/docker/manifests/29.8.2-dind" {
			t.Fatalf("unrelated metadata fetched: %s", path)
		}
	}
	if state.DependencyPins != nil {
		t.Fatal("mutated legacy state")
	}
}

func TestAgentToolsRefusesInvalidBaselineBeforeConnecting(t *testing.T) {
	state := planState()
	baseline := agentToolsState(t)
	state.DependencyPins = &baseline.Pins
	state.DependencyPins.DaemonImage = "docker:latest"
	fixture := newMetadataFixture()
	if _, err := (Checker{fixture}).PlanAgentTools(context.Background(), state, planRecipe()); err == nil || len(fixture.calls) != 0 {
		t.Fatal("invalid daemon pin accepted", err, fixture.calls)
	}
}

func TestAgentToolsRefusesUnrecordedLegacyBaselineWhenSourceChanged(t *testing.T) {
	state := planState()
	before := append([]runtimeimage.Dependency(nil), state.Inventory.Dependencies...)
	recipe := strings.ReplaceAll(string(planRecipe()), "GH_VERSION=2.1.0", "GH_VERSION=2.2.0")
	recipe = strings.ReplaceAll(recipe, "DOTNET_VERSION=10.0.100", "DOTNET_VERSION=10.0.401")
	recipe = strings.ReplaceAll(recipe, "NPM_VERSION=", "NPM_VERSION=11.2.0")
	recipe = strings.ReplaceAll(recipe, "YARN_VERSION=", "YARN_VERSION=1.22.22")
	recipe = strings.ReplaceAll(recipe, "COMPOSE_VERSION=", "COMPOSE_VERSION=5.6.0")
	recipe = strings.ReplaceAll(recipe, "BUILDX_VERSION=", "BUILDX_VERSION=0.31.0")
	recipe = strings.ReplaceAll(recipe, "node:24-bookworm", "node:24.2.0-bookworm")
	recipe = strings.ReplaceAll(recipe, "docker:29.8.2-cli", "docker:29.8.3-cli")
	fixture := planMetadata()
	plan, err := (Checker{fixture}).PlanAgentTools(context.Background(), state, []byte(recipe))
	if err == nil || !strings.Contains(err.Error(), "image provenance") || !strings.Contains(err.Error(), "--dependencies") || len(plan.Pins.Arguments) != 0 || len(fixture.calls) != 0 {
		t.Fatal("unrecorded source substituted for installed baseline", err, plan.Pins, fixture.calls)
	}
	if !reflect.DeepEqual(before, state.Inventory.Dependencies) {
		t.Fatal("mutated installed inventory")
	}
	// A recorded original recipe takes precedence over the deliberately newer source.
	state.BuildRecipe = string(planRecipe())
	fixture = planMetadata()
	fixture.bodies["registry-1.docker.io/v2/library/docker/manifests/29.8.2-dind"] = `{"schemaVersion":2,"manifests":[]}`
	plan, err = (Checker{fixture}).PlanAgentTools(context.Background(), state, []byte(recipe))
	if err != nil {
		t.Fatal(err)
	}
	for key, wanted := range map[string]string{"GH_VERSION": "2.1.0", "DOTNET_VERSION": "10.0.100", "NPM_VERSION": "11.1.0", "YARN_VERSION": "1.22.21", "COMPOSE_VERSION": "5.5.1", "BUILDX_VERSION": "0.30.0"} {
		if plan.Pins.Arguments[key] != wanted {
			t.Fatalf("%s changed to %s", key, plan.Pins.Arguments[key])
		}
	}
	if !strings.HasPrefix(plan.Pins.Images["node"], "node:24.1.0-bookworm@") || !strings.HasPrefix(plan.Pins.Images["docker"], "docker:29.8.2-cli@") {
		t.Fatal("changed recorded base images", plan.Pins.Images)
	}
}
