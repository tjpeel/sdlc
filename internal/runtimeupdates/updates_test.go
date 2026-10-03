package runtimeupdates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (function transportFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func fixtureInventory() runtimeimage.Inventory {
	return runtimeimage.Inventory{Version: 1, Platform: "linux/arm64", Distribution: "debian:bookworm",
		Dependencies: []runtimeimage.Dependency{
			{Name: "Codex CLI", Kind: "npm", Source: "@openai/codex", Version: "0.159.3"},
			{Name: "Claude Code", Kind: "npm", Source: "@anthropic-ai/claude-code", Version: "2.1.287"},
			{Name: "npm", Kind: "npm", Source: "npm", Version: "11.1.0"},
			{Name: "GitHub CLI", Kind: "github-release", Source: "cli/cli", Version: "2.1.0"},
			{Name: "Docker CLI", Kind: "github-release", Source: "docker/cli", Version: "29.8.2"},
			{Name: "Docker Compose", Kind: "github-release", Source: "docker/compose", Version: "5.5.1"},
			{Name: "Docker Buildx", Kind: "github-release", Source: "docker/buildx", Version: "0.30.0"},
			{Name: "Skills", Kind: "git", Source: "tjpeel/skills", Track: "main", Version: strings.Repeat("a", 40)},
			{Name: "Agents", Kind: "git", Source: "tjpeel/agents", Track: "main", Version: strings.Repeat("c", 40)},
			{Name: "Node base", Kind: "image", Source: "library/node", Track: "24-bookworm", Version: "sha256:" + strings.Repeat("a", 64)},
			{Name: "Node.js", Kind: "node", Source: "nodejs", Track: "24", Version: "24.1.0"},
			{Name: ".NET SDK", Kind: "dotnet-sdk", Source: "dotnet", Track: "10.0", Version: "10.0.100"},
			{Name: ".NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.NETCore.App", Track: "10.0", Version: "10.0.1"},
			{Name: "ASP.NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.AspNetCore.App", Track: "10.0", Version: "10.0.1"},
		}, Packages: []runtimeimage.Package{{Name: "git", Version: "1:2.39.5-0+deb12u1", Architecture: "arm64"}},
	}
}

func packages(context.Context) ([]runtimeimage.PackageUpdate, error) {
	return []runtimeimage.PackageUpdate{{Name: "git", Installed: "1:2.39.5-0+deb12u1", Candidate: "1:2.39.5-0+deb12u2", Architecture: "arm64", Update: true}}, nil
}

type metadataFixture struct {
	mutex     sync.Mutex
	bodies    map[string]string
	calls     map[string]int
	overrides map[string]func(*http.Request) (*http.Response, error)
}

func newMetadataFixture() *metadataFixture {
	return &metadataFixture{calls: make(map[string]int), overrides: make(map[string]func(*http.Request) (*http.Response, error)), bodies: map[string]string{
		"registry.npmjs.org/@openai/codex/latest":                                                                 `{"name":"@openai/codex","version":"0.159.4"}`,
		"registry.npmjs.org/@anthropic-ai/claude-code/latest":                                                     `{"name":"@anthropic-ai/claude-code","version":"2.1.288"}`,
		"registry.npmjs.org/npm/latest":                                                                           `{"name":"npm","version":"11.2.0"}`,
		"api.github.com/repos/cli/cli/releases/latest":                                                            `{"tag_name":"v2.2.0","draft":false,"prerelease":false}`,
		"api.github.com/repos/docker/cli/tags":                                                                    `[{"name":"v29.8.3"},{"name":"v30.0.0-rc1"},{"name":"v29.8.2"}]`,
		"api.github.com/repos/docker/compose/releases/latest":                                                     `{"tag_name":"v5.5.1","draft":false,"prerelease":false}`,
		"api.github.com/repos/docker/buildx/releases/latest":                                                      `{"tag_name":"v0.30.0","draft":false,"prerelease":false}`,
		"api.github.com/repos/tjpeel/skills/commits/main":                                                         `{"sha":"` + strings.Repeat("b", 40) + `"}`,
		"api.github.com/repos/tjpeel/skills/compare/" + strings.Repeat("a", 40) + "..." + strings.Repeat("b", 40): `{"status":"ahead"}`,
		"api.github.com/repos/tjpeel/agents/commits/main":                                                         `{"sha":"` + strings.Repeat("c", 40) + `"}`,
		"nodejs.org/dist/index.json":                                                                              `[{"version":"v26.10.0"},{"version":"v24.2.0"},{"version":"v24.3.0-rc1"}]`,
		"builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json":                                  `{"channel-version":"10.0","releases":[{"sdk":{"version":"10.0.401"},"sdks":[{"version":"10.0.402-preview"},{"version":"10.0.301"}],"runtime":{"version":"10.0.2"},"aspnetcore-runtime":{"version":"10.0.2"}}]}`,
		"auth.docker.io/token":                                                                                    `{"token":"fake-public-registry-token"}`,
		"registry-1.docker.io/v2/library/node/manifests/24-bookworm":                                              `{"schemaVersion":2,"manifests":[]}`,
	}}
}

func (fixture *metadataFixture) Do(request *http.Request) (*http.Response, error) {
	key := request.URL.Host + request.URL.Path
	fixture.mutex.Lock()
	fixture.calls[key]++
	override := fixture.overrides[key]
	body, found := fixture.bodies[key]
	fixture.mutex.Unlock()
	if override != nil {
		return override(request)
	}
	if !found {
		return nil, fmt.Errorf("unexpected fixture request")
	}
	if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "sdlc-runtime-status" {
		return nil, fmt.Errorf("invalid public metadata request")
	}
	headers := make(http.Header)
	if request.URL.Host == "registry-1.docker.io" {
		if request.Header.Get("Authorization") != "Bearer fake-public-registry-token" {
			return nil, fmt.Errorf("incorrect public registry scope")
		}
		sum := sha256.Sum256([]byte(body))
		headers.Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
	} else if request.Header.Get("Authorization") != "" {
		return nil, fmt.Errorf("credentials sent to public metadata endpoint")
	}
	if request.URL.Host == "auth.docker.io" && request.URL.Query().Get("scope") != "repository:library/node:pull" {
		return nil, fmt.Errorf("wrong anonymous registry scope")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: headers}, nil
}

func TestAllSourcesUseImmutableInventoryAndPublicMetadata(t *testing.T) {
	fixture := newMetadataFixture()
	inventory := fixtureInventory()
	report, err := (Checker{fixture}).Check(context.Background(), inventory, false, packages)
	if err != nil || report.Incomplete() {
		t.Fatal("complete fixtures failed", err, report)
	}
	statuses := map[string]string{}
	for _, result := range report.Results {
		statuses[result.Name] = result.Status
	}
	for _, name := range []string{"Codex CLI", "Claude Code", "npm", "GitHub CLI", "Docker CLI", "Skills", "Node.js", ".NET SDK", ".NET runtime", "ASP.NET runtime"} {
		if statuses[name] != Update {
			t.Errorf("%s: %s", name, statuses[name])
		}
	}
	if statuses["Agents"] != Current || statuses["Docker Compose"] != Current || statuses["Node base"] != "tag changed" {
		t.Fatal(statuses)
	}
	if fixture.calls["builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json"] != 1 {
		t.Fatal("shared release metadata fetched repeatedly")
	}
	var output bytes.Buffer
	if err := report.Print(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Debian git:arm64") || !strings.Contains(output.String(), "1 updates; 0 unavailable") || strings.Contains(output.String(), "No updates") {
		t.Fatal(output.String())
	}
	if inventory.Dependencies[0].Version != "0.159.3" {
		t.Fatal("checker mutated installed inventory")
	}
}

func TestOfflineNeverContactsMetadataOrPackageFeeds(t *testing.T) {
	checker := Checker{transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("HTTP request during offline check")
		return nil, fmt.Errorf("unexpected")
	})}
	report, err := checker.Check(context.Background(), fixtureInventory(), true, func(context.Context) ([]runtimeimage.PackageUpdate, error) {
		t.Error("online apt check during offline status")
		return nil, nil
	})
	if err != nil || report.Incomplete() {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if result.Status != Offline {
			t.Fatal(result)
		}
	}
	var output bytes.Buffer
	report.Print(&output)
	if strings.Contains(output.String(), "No updates") || !strings.Contains(output.String(), "upstream checks skipped") {
		t.Fatal(output.String())
	}
}

func TestPartialFailureKeepsSuccessfulChecksAndNeverReportsAllCurrent(t *testing.T) {
	fixture := newMetadataFixture()
	fixture.overrides["registry.npmjs.org/@anthropic-ai/claude-code/latest"] = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("private-looking diagnostics must stay hidden"))}, nil
	}
	report, err := (Checker{fixture}).Check(context.Background(), fixtureInventory(), false, func(context.Context) ([]runtimeimage.PackageUpdate, error) {
		return nil, fmt.Errorf("private-looking engine diagnostics")
	})
	if err != nil || !report.Incomplete() {
		t.Fatal("missing incomplete state", err)
	}
	if report.Results[0].Status != Update || report.Results[1].Status != Unavailable || report.Results[1].Detail != "metadata returned HTTP 429" {
		t.Fatal(report.Results[:2])
	}
	var output bytes.Buffer
	report.Print(&output)
	if strings.Contains(output.String(), "private-looking") || strings.Contains(output.String(), "No updates") || !strings.Contains(output.String(), "Dependency check incomplete") {
		t.Fatal(output.String())
	}
}

func TestVersionOrderingAndCatalogueAncestry(t *testing.T) {
	fixture := newMetadataFixture()
	fixture.bodies["registry.npmjs.org/@openai/codex/latest"] = `{"name":"@openai/codex","version":"0.159.2"}`
	comparison := "api.github.com/repos/tjpeel/skills/compare/" + strings.Repeat("a", 40) + "..." + strings.Repeat("b", 40)
	for _, scenario := range []struct{ comparison, status string }{{"behind", Ahead}, {"diverged", Changed}} {
		fixture.bodies[comparison] = `{"status":"` + scenario.comparison + `"}`
		report, err := (Checker{fixture}).Check(context.Background(), fixtureInventory(), false, packages)
		if err != nil || report.Incomplete() || report.Results[0].Status != Ahead || report.Results[7].Status != scenario.status {
			t.Fatal(err, report.Results)
		}
	}
	if compare("1.10.0", "1.9.0") <= 0 {
		t.Fatal("lexical version comparison")
	}
	for _, value := range []string{"1.2.3-beta", "01.2.3", "1.2", "1.2.3\n", "18446744073709551616.1.0"} {
		if _, valid := version(value); valid {
			t.Fatal("accepted invalid stable version", value)
		}
	}
}

func TestCodexPlatformAliasUsesPublishedPackageAndTag(t *testing.T) {
	for _, scenario := range []struct{ candidate, status string }{
		{"0.159.4-linux-arm64", Update}, {"0.159.3-linux-arm64", Current},
		{"0.159.2-linux-arm64", Ahead}, {"0.159.4-linux-x64", Unavailable},
		{"0.159.4-alpha-linux-arm64", Unavailable},
	} {
		fixture := newMetadataFixture()
		fixture.bodies["registry.npmjs.org/@openai/codex/linux-arm64"] = `{"name":"@openai/codex","version":"` + scenario.candidate + `"}`
		inventory := fixtureInventory()
		inventory.Dependencies = append(inventory.Dependencies, runtimeimage.Dependency{
			Name: "@openai/codex-linux-arm64", Kind: "npm", Source: "@openai/codex", Track: "linux-arm64", Version: "0.159.3-linux-arm64",
		})
		report, err := (Checker{fixture}).Check(context.Background(), inventory, false, packages)
		if err != nil || report.Results[len(report.Results)-1].Status != scenario.status {
			t.Fatal(scenario, err, report.Results)
		}
		if fixture.calls["registry.npmjs.org/@openai/codex-linux-arm64/latest"] != 0 || fixture.calls["registry.npmjs.org/@openai/codex/linux-arm64"] != 1 {
			t.Fatal("alias queried instead of published package", fixture.calls)
		}
	}
}

func TestDockerTagsFollowAllPagesAndDoNotTrustPartialResults(t *testing.T) {
	for _, scenario := range []struct {
		link         string
		secondStatus int
		status       string
	}{
		{`<https://api.github.com/repos/docker/cli/tags?per_page=100&page=2>; rel="next"`, 200, Update},
		{`<https://api.github.com/repositories/123456/tags?per_page=100&page=2>; rel="next"`, 200, Update},
		{`<https://api.github.com/repos/docker/cli/tags?per_page=100&page=2>; rel="next"`, 429, Unavailable},
		{`<https://example.invalid/tags?per_page=100&page=2>; rel="next"`, 200, Unavailable},
	} {
		fixture := newMetadataFixture()
		fixture.overrides["api.github.com/repos/docker/cli/tags"] = func(request *http.Request) (*http.Response, error) {
			headers := make(http.Header)
			code, body := http.StatusOK, `[{"name":"v29.8.2"}]`
			if request.URL.Query().Get("page") == "2" {
				code, body = scenario.secondStatus, `[{"name":"v30.1.0"}]`
			} else {
				headers.Set("Link", scenario.link)
			}
			return &http.Response{StatusCode: code, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		report, err := (Checker{fixture}).Check(context.Background(), fixtureInventory(), false, packages)
		if err != nil || report.Results[4].Status != scenario.status {
			t.Fatal("Docker pagination result", scenario, err, report.Results[4])
		}
		if scenario.status == Update && report.Results[4].Candidate != "30.1.0" {
			t.Fatal(report.Results[4])
		}
	}
}

func TestPlatformPackageRetainsUpstreamFailure(t *testing.T) {
	fixture := newMetadataFixture()
	fixture.overrides["registry.npmjs.org/@openai/codex/linux-arm64"] = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("fake metadata limit"))}, nil
	}
	inventory := fixtureInventory()
	inventory.Dependencies = append(inventory.Dependencies, runtimeimage.Dependency{
		Name: "@openai/codex-linux-arm64", Kind: "npm", Source: "@openai/codex", Track: "linux-arm64", Version: "0.159.3-linux-arm64",
	})
	report, err := (Checker{fixture}).Check(context.Background(), inventory, false, packages)
	if err != nil {
		t.Fatal(err)
	}
	row := report.Results[len(report.Results)-1]
	if row.Status != Unavailable || row.Detail != "metadata returned HTTP 429" {
		t.Fatal(row)
	}
}

func TestMalformedMetadataAndCandidatesAreUnavailable(t *testing.T) {
	for _, scenario := range []struct {
		endpoint, body string
		row            int
	}{
		{"registry.npmjs.org/@openai/codex/latest", `{"name":"different-package","version":"0.160.0"}`, 0},
		{"registry.npmjs.org/@anthropic-ai/claude-code/latest", `{"name":"@anthropic-ai/claude-code","version":"3.0.0-beta"}`, 1},
		{"api.github.com/repos/cli/cli/releases/latest", `{"tag_name":"v2.3.0"}`, 3},
		{"api.github.com/repos/docker/compose/releases/latest", `{"tag_name":"v5.6.0","draft":false,"prerelease":true}`, 5},
		{"api.github.com/repos/tjpeel/skills/commits/main", `{"sha":"not-a-commit"}`, 7},
		{"builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json", `{"channel-version":"11.0","releases":[]}`, 11},
	} {
		t.Run(scenario.endpoint, func(t *testing.T) {
			fixture := newMetadataFixture()
			fixture.bodies[scenario.endpoint] = scenario.body
			report, err := (Checker{fixture}).Check(context.Background(), fixtureInventory(), false, packages)
			if err != nil || !report.Incomplete() || report.Results[scenario.row].Status != Unavailable {
				t.Fatal(err, report.Results[scenario.row])
			}
		})
	}
	for _, packageResult := range []runtimeimage.PackageUpdate{
		{Name: "git", Installed: "1:2.39.5-0+deb12u1", Architecture: "arm64"},
		{Name: "unexpected", Installed: "1.0", Candidate: "2.0", Architecture: "arm64"},
	} {
		report, err := (Checker{newMetadataFixture()}).Check(context.Background(), fixtureInventory(), false, func(context.Context) ([]runtimeimage.PackageUpdate, error) {
			return []runtimeimage.PackageUpdate{packageResult}, nil
		})
		if err != nil || !report.Incomplete() {
			t.Fatal("missing candidate treated as complete", err)
		}
	}
}

func TestCancellationBoundsRequestsAndDoesNotLeakErrors(t *testing.T) {
	client := transportFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, fmt.Errorf("sensitive proxy URL or authorization must not escape")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	report, err := (Checker{client}).Check(ctx, fixtureInventory(), false, packages)
	if err != nil || !report.Incomplete() {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if strings.Contains(result.Detail, "sensitive") || result.Status != Unavailable {
			t.Fatal(result)
		}
	}
}

func TestResponseBoundsAndManifestDigest(t *testing.T) {
	for _, scenario := range []struct {
		endpoint string
		respond  func(*http.Request) (*http.Response, error)
		row      int
	}{
		{"registry.npmjs.org/@openai/codex/latest", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maximumResponse+1)))}, nil
		}, 0},
		{"registry-1.docker.io/v2/library/node/manifests/24-bookworm", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Docker-Content-Digest": []string{"sha256:" + strings.Repeat("f", 64)}}, Body: io.NopCloser(strings.NewReader(`{"schemaVersion":2}`))}, nil
		}, 9},
	} {
		fixture := newMetadataFixture()
		fixture.overrides[scenario.endpoint] = scenario.respond
		report, err := (Checker{fixture}).Check(context.Background(), fixtureInventory(), false, packages)
		if err != nil || !report.Incomplete() || report.Results[scenario.row].Status != Unavailable {
			t.Fatal(err, report.Results[scenario.row])
		}
	}
}

func TestInvalidInventoryMakesNoRequests(t *testing.T) {
	inventory := fixtureInventory()
	inventory.Dependencies[0].Source = "https://untrusted.invalid"
	client := transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid inventory caused network request")
		return nil, nil
	})
	if _, err := (Checker{client}).Check(context.Background(), inventory, false, packages); err == nil {
		t.Fatal("invalid inventory accepted")
	}
}
