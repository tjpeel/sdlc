package runtimeupdates

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

func planRecipe() []byte {
	return []byte("FROM golang:1.27.1-bookworm@sha256:" + strings.Repeat("a", 64) + " AS publisher-build\n" +
		"FROM docker:29.8.2-cli@sha256:" + strings.Repeat("b", 64) + " AS docker-tools\n" +
		"FROM node:24-bookworm@sha256:" + strings.Repeat("a", 64) + "\n" +
		"ARG CODEX_VERSION=0.159.3\nARG CLAUDE_VERSION=2.1.287\nARG GH_VERSION=2.1.0\nARG DOTNET_VERSION=10.0.100\n" +
		"ARG SKILLS_REVISION=" + strings.Repeat("a", 40) + "\nARG AGENTS_REVISION=" + strings.Repeat("c", 40) + "\n" +
		"ARG NPM_VERSION=\nARG YARN_VERSION=\nARG COMPOSE_VERSION=\nARG BUILDX_VERSION=\nRUN echo preserved\n")
}

func planState() runtimeimage.State {
	inventory := fixtureInventory()
	inventory.Dependencies = append(inventory.Dependencies, runtimeimage.Dependency{Name: "yarn", Kind: "npm", Source: "yarn", Version: "1.22.21"})
	return runtimeimage.State{Inventory: &inventory}
}

func planMetadata() *metadataFixture {
	fixture := newMetadataFixture()
	fixture.bodies["registry.npmjs.org/yarn/latest"] = `{"name":"yarn","version":"1.22.22"}`
	fixture.bodies["go.dev/dl/"] = `[{"version":"go1.28.5","stable":true},{"version":"go1.27.2","stable":true},{"version":"go1.27.3rc1","stable":false}]`
	fixture.bodies["hub.docker.com/v2/repositories/1password/op/tags"] = `{"next":null,"results":[{"name":"3.9.0"},{"name":"2.40.0"},{"name":"2.41.0-beta"},{"name":"latest"}]}`
	fixture.bodies["api.github.com/repos/docker/compose/releases/latest"] = `{"tag_name":"v5.6.0","draft":false,"prerelease":false}`
	fixture.bodies["api.github.com/repos/docker/buildx/releases/latest"] = `{"tag_name":"v0.31.0","draft":false,"prerelease":false}`
	for _, path := range []string{"library/node/manifests/24.2.0-bookworm", "library/golang/manifests/1.27.2-bookworm", "library/docker/manifests/29.8.3-cli", "library/docker/manifests/29.8.3-dind", "1password/op/manifests/2.40.0"} {
		fixture.bodies["registry-1.docker.io/v2/"+path] = `{"schemaVersion":2,"manifests":[]}`
	}
	fixture.overrides["auth.docker.io/token"] = func(request *http.Request) (*http.Response, error) {
		source := strings.TrimSuffix(strings.TrimPrefix(request.URL.Query().Get("scope"), "repository:"), ":pull")
		if source != "library/node" && source != "library/golang" && source != "library/docker" && source != "1password/op" {
			return nil, fmt.Errorf("unapproved fixture registry scope")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"token":"fake-public-registry-token"}`))}, nil
	}
	return fixture
}

func TestPlanResolvesEveryPinWithinReleaseTracks(t *testing.T) {
	fixture := planMetadata()
	state := planState()
	// Display names cannot change managed actions.
	for i := range state.Inventory.Dependencies {
		state.Inventory.Dependencies[i].Name = fmt.Sprintf("Display %d", i)
	}
	state.Inventory.Dependencies = append(state.Inventory.Dependencies, runtimeimage.Dependency{Name: "Child", Kind: "npm", Source: "public-child", Version: "1.0.0"})
	fixture.bodies["registry.npmjs.org/public-child/latest"] = `{"name":"public-child","version":"2.0.0"}`
	original := planRecipe()
	before := append([]byte{}, original...)
	plan, err := (Checker{fixture}).Plan(context.Background(), state, original, packages)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Pins.Validate(); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"CODEX_VERSION": "0.159.4", "CLAUDE_VERSION": "2.1.288", "GH_VERSION": "2.2.0", "DOTNET_VERSION": "10.0.401", "NPM_VERSION": "11.2.0", "YARN_VERSION": "1.22.22", "COMPOSE_VERSION": "5.6.0", "BUILDX_VERSION": "0.31.0"} {
		if plan.Pins.Arguments[key] != value {
			t.Fatal("incorrect pin", key, plan.Pins.Arguments[key])
		}
	}
	if !strings.HasPrefix(plan.Pins.Images["node"], "node:24.2.0-bookworm@sha256:") || !strings.HasPrefix(plan.Pins.Images["golang"], "golang:1.27.2-bookworm@sha256:") || !strings.HasPrefix(plan.Pins.SigningImage, "1password/op:2.40.0@sha256:") || !strings.HasPrefix(plan.Pins.DaemonImage, "docker:29.8.3-dind@sha256:") {
		t.Fatal(plan.Pins)
	}
	if len(plan.Remaining) != 1 || !strings.Contains(plan.Remaining[0].Detail, "published parent") {
		t.Fatal("transitive update lost parent governance", plan.Remaining)
	}
	if plan.ExpectedRuntimes["Microsoft.NETCore.App"] != "10.0.2" || plan.ExpectedRuntimes["Microsoft.AspNetCore.App"] != "10.0.2" {
		t.Fatal("SDK runtime verification metadata missing", plan.ExpectedRuntimes)
	}
	if !bytes.Equal(original, before) || state.DependencyPins != nil || state.Inventory.Dependencies[0].Version != "0.159.3" {
		t.Fatal("planning mutated source state")
	}
	if fixture.calls["nodejs.org/dist/index.json"] != 1 || fixture.calls["api.github.com/repos/docker/cli/tags"] != 1 {
		t.Fatal("metadata cache bypassed", fixture.calls)
	}
	var output bytes.Buffer
	if err := plan.Print(&output); err != nil || !strings.Contains(output.String(), "Remaining parent-managed npm differences: 1") || !strings.Contains(output.String(), "Planned build actions") {
		t.Fatal(err, output.String())
	}
}

func TestPlanNeverDowngradesInstalledToolsOrAuxiliaryImages(t *testing.T) {
	fixture := planMetadata()
	state := planState()
	state.Inventory.Dependencies[0].Version = "0.999.0"
	pins, err := (Checker{fixture}).Plan(context.Background(), state, planRecipe(), packages)
	if err != nil {
		t.Fatal(err)
	}
	state.DependencyPins = &pins.Pins
	for key, ref := range map[string]string{"node": "node:24.9.0-bookworm", "golang": "golang:1.27.9-bookworm", "docker": "docker:30.0.0-cli"} {
		state.DependencyPins.Images[key] = ref + "@sha256:" + strings.Repeat("d", 64)
	}
	state.DependencyPins.DaemonImage = "docker:30.0.0-dind@sha256:" + strings.Repeat("d", 64)
	state.DependencyPins.SigningImage = "1password/op:2.50.0@sha256:" + strings.Repeat("d", 64)
	for _, path := range []string{"library/node/manifests/24.9.0-bookworm", "library/golang/manifests/1.27.9-bookworm", "library/docker/manifests/30.0.0-cli", "library/docker/manifests/30.0.0-dind", "1password/op/manifests/2.50.0"} {
		fixture.bodies["registry-1.docker.io/v2/"+path] = `{"schemaVersion":2,"manifests":[]}`
	}
	plan, err := (Checker{fixture}).Plan(context.Background(), state, planRecipe(), packages)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pins.Arguments["CODEX_VERSION"] != "0.999.0" || !strings.Contains(plan.Pins.Images["node"], ":24.9.0-") || !strings.Contains(plan.Pins.Images["golang"], ":1.27.9-") || !strings.Contains(plan.Pins.Images["docker"], ":30.0.0-") || !strings.Contains(plan.Pins.SigningImage, ":2.50.0@") || !strings.Contains(plan.Pins.DaemonImage, ":30.0.0-") {
		t.Fatal("installed dependency downgraded", plan.Pins)
	}
}

func TestRecordedRecipeAndInventoryPreventChangedSourceDowngrades(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded=%t", recorded), func(t *testing.T) {
			fixture := planMetadata()
			state := planState()
			state.Inventory.Dependencies[4].Version = "30.0.0"
			if recorded {
				state.BuildRecipe = strings.Replace(string(planRecipe()), "docker:29.8.2-cli", "docker:30.0.0-cli", 1)
				state.BuildRecipe = strings.Replace(state.BuildRecipe, "golang:1.27.1-bookworm", "golang:1.27.5-bookworm", 1)
			}
			for _, path := range []string{"library/docker/manifests/30.0.0-cli", "library/docker/manifests/30.0.0-dind", "library/golang/manifests/1.27.5-bookworm"} {
				fixture.bodies["registry-1.docker.io/v2/"+path] = `{"schemaVersion":2,"manifests":[]}`
			}
			plan, err := (Checker{fixture}).Plan(context.Background(), state, planRecipe(), packages)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(plan.Pins.Images["docker"], "docker:30.0.0-cli@") || !strings.HasPrefix(plan.Pins.DaemonImage, "docker:30.0.0-dind@") {
				t.Fatal("actual installed Docker downgraded by source recipe", plan.Pins)
			}
			if recorded && !strings.HasPrefix(plan.Pins.Images["golang"], "golang:1.27.5-bookworm@") {
				t.Fatal("recorded builder baseline ignored", plan.Pins.Images["golang"])
			}
			if !recorded {
				found := false
				for _, row := range plan.Report.Results {
					if row.Name == "Go publisher builder" && strings.Contains(row.Detail, "source recipe assumed") {
						found = true
					}
				}
				if !found {
					t.Fatal("legacy provenance assumption not reported")
				}
			}
		})
	}
}

func TestSDKAndSharedRuntimesNeverDowngrade(t *testing.T) {
	state := planState()
	for i := range state.Inventory.Dependencies {
		if state.Inventory.Dependencies[i].Kind == "dotnet-sdk" || state.Inventory.Dependencies[i].Kind == "dotnet-runtime" {
			state.Inventory.Dependencies[i].Version = "10.0.999"
		}
	}
	plan, err := (Checker{planMetadata()}).Plan(context.Background(), state, planRecipe(), packages)
	if err != nil || plan.Pins.Arguments["DOTNET_VERSION"] != "10.0.999" || plan.ExpectedRuntimes["Microsoft.NETCore.App"] != "10.0.999" || plan.ExpectedRuntimes["Microsoft.AspNetCore.App"] != "10.0.999" {
		t.Fatal("SDK or shared runtime downgraded", err, plan.Pins.Arguments, plan.ExpectedRuntimes)
	}
}

func TestResolveDefaultDaemonUsesExactLegacyVersionWithoutAccountCredentials(t *testing.T) {
	fixture := planMetadata()
	fixture.bodies["registry-1.docker.io/v2/library/docker/manifests/29.8.2-dind"] = `{"schemaVersion":2,"manifests":[]}`
	reference, err := (Checker{fixture}).ResolveDefaultDaemonImage(context.Background())
	if err != nil || !strings.HasPrefix(reference, "docker:29.8.2-dind@sha256:") || fixture.calls["api.github.com/repos/docker/cli/tags"] != 0 {
		t.Fatal("default daemon resolver changed version or failed", err, reference)
	}
}

func TestPlanAllOrNothingOnManagedOrDebianFailure(t *testing.T) {
	comparison := "api.github.com/repos/tjpeel/skills/compare/" + strings.Repeat("a", 40) + "..." + strings.Repeat("b", 40)
	for _, scenario := range []struct{ endpoint, body string }{
		{"registry.npmjs.org/npm/latest", `{"name":"npm","version":"12.0.0-preview"}`},
		{comparison, `{"status":"behind"}`},
		{comparison, `{"status":"diverged"}`},
		{"go.dev/dl/", `[{"version":"go1.28.1","stable":true}]`},
		{"go.dev/dl/", `[{"version":"go1.27.2"}]`},
		{"hub.docker.com/v2/repositories/1password/op/tags", `{"next":null,"results":[{"name":"3.0.0"}]}`},
		{"registry-1.docker.io/v2/library/docker/manifests/29.8.3-dind", `{"schemaVersion":1}`},
		{"builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json", `{"channel-version":"11.0","releases":[]}`},
	} {
		t.Run(scenario.endpoint+scenario.body, func(t *testing.T) {
			fixture := planMetadata()
			fixture.bodies[scenario.endpoint] = scenario.body
			plan, err := (Checker{fixture}).Plan(context.Background(), planState(), planRecipe(), packages)
			if err == nil || plan.Pins.Arguments != nil || plan.Pins.Images != nil || len(plan.Changes) != 0 {
				t.Fatal("partial plan actionable", err, plan)
			}
		})
	}
	plan, err := (Checker{planMetadata()}).Plan(context.Background(), planState(), planRecipe(), func(context.Context) ([]runtimeimage.PackageUpdate, error) {
		return nil, fmt.Errorf("disposable package failure")
	})
	if err == nil || plan.Pins.Arguments != nil || len(plan.Changes) != 0 {
		t.Fatal("Debian failure produced plan", err)
	}
}

func TestTransitiveMetadataFailureRemainsReportedWithoutOverridingParents(t *testing.T) {
	state := planState()
	state.Inventory.Dependencies = append(state.Inventory.Dependencies, runtimeimage.Dependency{Name: "Child", Kind: "npm", Source: "missing-public-child", Version: "1.0.0"})
	plan, err := (Checker{planMetadata()}).Plan(context.Background(), state, planRecipe(), packages)
	if err != nil || len(plan.Remaining) != 1 || plan.Remaining[0].Status != Unavailable || !plan.Report.Incomplete() {
		t.Fatal(err, plan.Remaining)
	}
	if len(plan.Pins.Arguments) != 10 {
		t.Fatal("transitive version injected into pin map")
	}
}

func TestCheckStateOfflineIncludesAuxiliaryImagesWithoutRequests(t *testing.T) {
	client := transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("offline HTTP"); return nil, nil })
	report, err := (Checker{client}).CheckState(context.Background(), planState(), planRecipe(), true, func(context.Context) ([]runtimeimage.PackageUpdate, error) {
		t.Fatal("offline Debian refresh")
		return nil, nil
	})
	if err != nil || len(report.Results) != len(planState().Inventory.Dependencies)+5 {
		t.Fatal(err, report)
	}
	for _, row := range report.Results {
		if row.Status != Offline {
			t.Fatal(row)
		}
	}
}

func TestCheckStateMissingRecipeReportsSourceProvenanceUnavailable(t *testing.T) {
	report, err := (Checker{planMetadata()}).CheckState(context.Background(), planState(), nil, false, packages)
	if err != nil || !report.Incomplete() {
		t.Fatal("missing build provenance accepted", err, report)
	}
	for _, row := range report.Results[len(planState().Inventory.Dependencies):] {
		if row.Status != Unavailable || !strings.Contains(row.Detail, "build recipe") {
			t.Fatal(row)
		}
	}
}

func TestOnePasswordPaginationSelectsLatestAcrossCompletePages(t *testing.T) {
	fixture := planMetadata()
	fixture.overrides["hub.docker.com/v2/repositories/1password/op/tags"] = func(request *http.Request) (*http.Response, error) {
		body := `{"next":"https://hub.docker.com/v2/repositories/1password/op/tags/?page=2&page_size=100","results":[{"name":"2.40.0"}]}`
		if request.URL.Query().Get("page") == "2" {
			body = `{"next":null,"results":[{"name":"2.50.0"},{"name":"3.0.0"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	fixture.bodies["registry-1.docker.io/v2/1password/op/manifests/2.50.0"] = `{"schemaVersion":2,"manifests":[]}`
	plan, err := (Checker{fixture}).Plan(context.Background(), planState(), planRecipe(), packages)
	if err != nil || !strings.HasPrefix(plan.Pins.SigningImage, "1password/op:2.50.0@sha256:") || fixture.calls["hub.docker.com/v2/repositories/1password/op/tags"] != 2 {
		t.Fatal("later image tag ignored", err, plan.Pins.SigningImage)
	}
}

func TestOnePasswordPaginationRejectsUntrustedOrIncompletePages(t *testing.T) {
	for _, next := range []string{"https://example.invalid/tags?page=2&page_size=100", "https://hub.docker.com/v2/repositories/untrusted/op/tags?page=2&page_size=100", "https://hub.docker.com/v2/repositories/1password/op/tags?page=2&page_size=100"} {
		fixture := planMetadata()
		fixture.overrides["hub.docker.com/v2/repositories/1password/op/tags"] = func(request *http.Request) (*http.Response, error) {
			if request.URL.Query().Get("page") == "2" {
				return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("fake rate limit"))}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"next":"` + next + `","results":[{"name":"2.40.0"}]}`))}, nil
		}
		plan, err := (Checker{fixture}).Plan(context.Background(), planState(), planRecipe(), packages)
		if err == nil || plan.Pins.SigningImage != "" {
			t.Fatal("partial public tag data trusted", next, err)
		}
	}
}

func TestInvalidPinsAndRecipeFailBeforeMetadata(t *testing.T) {
	state := planState()
	state.DependencyPins = &runtimepins.Pins{}
	client := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid pins requested metadata")
		return nil, nil
	})
	if _, err := (Checker{client}).Plan(context.Background(), state, planRecipe(), packages); err == nil {
		t.Fatal("invalid stored pins accepted")
	}
	state.DependencyPins = nil
	if _, err := (Checker{client}).Plan(context.Background(), state, append(planRecipe(), []byte("ARG NPM_VERSION=1.2.3\n")...), packages); err == nil {
		t.Fatal("ambiguous recipe accepted")
	}
}
