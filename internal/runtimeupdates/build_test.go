package runtimeupdates

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPlanBuildResolvesFreshDependenciesWithoutAnInstalledRuntime(t *testing.T) {
	fixture := planMetadata()
	local := map[string]string{"SKILLS_REVISION": strings.Repeat("d", 40), "AGENTS_REVISION": strings.Repeat("e", 40)}
	plan, err := (Checker{fixture}).PlanBuild(context.Background(), planRecipe(), local)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"SKILLS_REVISION": local["SKILLS_REVISION"], "AGENTS_REVISION": local["AGENTS_REVISION"], "CODEX_VERSION": "0.159.4", "CLAUDE_VERSION": "2.1.288", "NPM_VERSION": "11.2.0", "YARN_VERSION": "1.22.22", "DOTNET_VERSION": "10.0.401", "COMPOSE_VERSION": "5.6.0", "BUILDX_VERSION": "0.31.0"} {
		if plan.Pins.Arguments[key] != want {
			t.Fatalf("%s=%s want %s", key, plan.Pins.Arguments[key], want)
		}
	}
	if err := plan.Pins.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Pins.Images["node"], "node:24.2.0-bookworm@sha256:") || !strings.HasPrefix(plan.Pins.Images["golang"], "golang:1.27.2-bookworm@sha256:") {
		t.Fatal("lost release tracks", plan.Pins.Images)
	}
	for path := range fixture.calls {
		if strings.Contains(path, "/tjpeel/skills/") || strings.Contains(path, "/tjpeel/agents/") {
			t.Fatal("queried fixed local catalogue", path)
		}
	}
	if plan.ExpectedRuntimes["Microsoft.NETCore.App"] != "10.0.2" {
		t.Fatal("missing runtime validation", plan.ExpectedRuntimes)
	}
	var output bytes.Buffer
	if err := plan.PrintBuild(&output); err != nil || !strings.Contains(output.String(), local["SKILLS_REVISION"]) || strings.Contains(output.String(), "installed") {
		t.Fatal(output.String(), err)
	}
}

func TestPlanBuildRemoteCatalogueAndMetadataFailureAreExplicit(t *testing.T) {
	fixture := planMetadata()
	plan, err := (Checker{fixture}).PlanBuild(context.Background(), planRecipe(), map[string]string{"SKILLS_REVISION": strings.Repeat("d", 40)})
	if err != nil || plan.Pins.Arguments["AGENTS_REVISION"] != strings.Repeat("c", 40) {
		t.Fatal(plan, err)
	}
	delete(fixture.bodies, "registry.npmjs.org/@anthropic-ai/claude-code/latest")
	plan, err = (Checker{fixture}).PlanBuild(context.Background(), planRecipe(), nil)
	if err == nil || plan.Pins.Arguments != nil || plan.ExpectedRuntimes != nil {
		t.Fatal("returned partial build plan", plan, err)
	}
	if _, err := (Checker{fixture}).PlanBuild(context.Background(), planRecipe(), map[string]string{"CODEX_VERSION": "unexpected"}); err == nil {
		t.Fatal("accepted an unrelated fixed selection")
	}
}

func TestPlanBuildRejectsMissingSharedRuntimeMetadataBeforeBuild(t *testing.T) {
	fixture := planMetadata()
	fixture.bodies["builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json"] = `{"channel-version":"10.0","releases":[{"sdk":{"version":"10.0.401"}}]}`
	plan, err := (Checker{fixture}).PlanBuild(context.Background(), planRecipe(), nil)
	if err == nil || !strings.Contains(err.Error(), "runtime") || plan.Pins.Arguments != nil || plan.ExpectedRuntimes != nil {
		t.Fatal("accepted incomplete shared-runtime evidence", plan, err)
	}
}
