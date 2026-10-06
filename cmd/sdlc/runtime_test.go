package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

type statusFixture struct {
	state        runtimeimage.State
	err          error
	packageCalls int
}

func (fixture *statusFixture) Status(context.Context) (runtimeimage.State, error) {
	return fixture.state, fixture.err
}

func (fixture *statusFixture) PackageUpdates(context.Context, runtimeimage.State) ([]runtimeimage.PackageUpdate, error) {
	fixture.packageCalls++
	return []runtimeimage.PackageUpdate{{Name: "git", Installed: "1:2.39.5-1", Candidate: "1:2.39.5-2", Architecture: "arm64", Update: true}}, nil
}

type statusHTTP func(*http.Request) (*http.Response, error)

func (client statusHTTP) Do(request *http.Request) (*http.Response, error) { return client(request) }

func statusInventory() *runtimeimage.Inventory {
	return &runtimeimage.Inventory{
		Version: 1, Platform: "linux/arm64", Distribution: "debian:bookworm",
		Dependencies: []runtimeimage.Dependency{
			{Name: "Codex", Kind: "npm", Source: "@openai/codex", Version: "0.159.3"},
			{Name: "Claude", Kind: "npm", Source: "@anthropic-ai/claude-code", Version: "2.1.287"},
			{Name: "npm", Kind: "npm", Source: "npm", Version: "11.1.0"},
			{Name: "GitHub CLI", Kind: "github-release", Source: "cli/cli", Version: "2.2.0"},
			{Name: "Docker CLI", Kind: "github-release", Source: "docker/cli", Version: "29.8.2"},
			{Name: "Compose", Kind: "github-release", Source: "docker/compose", Version: "5.5.1"},
			{Name: "Buildx", Kind: "github-release", Source: "docker/buildx", Version: "0.29.0"},
			{Name: "Skills", Kind: "git", Source: "tjpeel/skills", Track: "main", Version: strings.Repeat("a", 40)},
			{Name: "Agents", Kind: "git", Source: "tjpeel/agents", Track: "main", Version: strings.Repeat("a", 40)},
			{Name: "Node", Kind: "node", Source: "nodejs", Track: "24", Version: "24.1.0"},
			{Name: "Node base", Kind: "image", Source: "library/node", Track: "24-bookworm", Version: "sha256:" + strings.Repeat("a", 64)},
			{Name: ".NET SDK", Kind: "dotnet-sdk", Source: "dotnet", Track: "10.0", Version: "10.0.401"},
			{Name: ".NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.NETCore.App", Track: "10.0", Version: "10.0.1"},
			{Name: "ASP.NET runtime", Kind: "dotnet-runtime", Source: "Microsoft.AspNetCore.App", Track: "10.0", Version: "10.0.1"},
		},
		Packages: []runtimeimage.Package{{Name: "git", Version: "1:2.39.5-1", Architecture: "arm64"}},
	}
}

func TestRuntimeArgumentsAndHelpNeverCreateState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	t.Setenv("SDLC_STATE_DIR", directory)
	for _, args := range [][]string{{}, {"unknown"}, {"status", "--source", "somewhere"}, {"build", "--offline"}, {"build", "--github-profile", "personal"}, {"status", "--github-profile", "../unsafe"}, {"status", "extra"}, {"status", "--offline=invalid"}, {"update", "--offline"}, {"update", "--github-profile", "personal"}, {"update", "--dry-run=invalid"}, {"update", "extra"}} {
		if err := runtimeCommand(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid options: %v", args)
		}
	}
	for _, command := range []string{"status", "build", "update"} {
		var diagnostics bytes.Buffer
		if err := runtimeCommand(context.Background(), []string{command, "--help"}, &bytes.Buffer{}, &diagnostics); err != nil || !strings.Contains(diagnostics.String(), "Usage of runtime "+command) {
			t.Fatal("help tried to inspect a runtime", err, diagnostics.String())
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid arguments or help created state", err)
	}
}

func TestRuntimeStatusChecksSelectedProjectPair(t *testing.T) {
	root := runGitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, root)
	directory, profile := pairingFixture(t)
	t.Setenv("SDLC_STATE_DIR", directory)
	pair := githubprofile.Pair{Version: 1, GitHubProfile: "personal", AccountID: 123, Login: "example-user", SigningProfile: "personal-key", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
	if err := githubprofile.Store(directory, pair, false); err != nil {
		t.Fatal(err)
	}
	if err := githubprofile.SaveSelection(directory, root, "example-org/project", pair); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile.BootstrapFile, []byte("fake-service-bootstrap\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	var output bytes.Buffer
	err = runtimeCommand(context.Background(), []string{"status", "--offline"}, &output, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(output.String(), "GitHub profile: personal\n") || !strings.Contains(output.String(), "Signing profile: personal-key\n") || strings.Contains(output.String(), "needs attention") || strings.Contains(output.String(), "--profile default") {
		t.Fatalf("runtime status lost selected pairing or masked missing runtime: %s; %v", output.String(), err)
	}
	output.Reset()
	err = runtimeCommand(context.Background(), []string{"status", "--offline", "--github-profile", "other"}, &output, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(output.String(), "GitHub profile other is unpaired") || strings.Contains(output.String(), "GitHub profile: personal") {
		t.Fatalf("explicit status profile did not override summary: %s; %v", output.String(), err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("pairing readiness started a connected command")
	}
}

func TestRuntimeStatusDoesNotInventDefaultPairOutsideCheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("SDLC_STATE_DIR", filepath.Join(t.TempDir(), "absent-state"))
	var output bytes.Buffer
	err := runtimeCommand(context.Background(), []string{"status", "--offline"}, &output, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(output.String(), "GitHub pairing not checked") || !strings.Contains(output.String(), "--github-profile NAME") || strings.Contains(output.String(), "--profile default") || strings.Contains(output.String(), "needs attention") {
		t.Fatalf("outside-checkout readiness invented a profile or masked runtime error: %s; %v", output.String(), err)
	}
	if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatal("readiness created installation state")
	}
}

func TestRuntimeStatusLegacyInventoryIsExplicit(t *testing.T) {
	manager := &statusFixture{state: runtimeimage.State{ImageID: "fake-image", Tools: "Codex version fixture"}}
	checker := runtimeupdates.Checker{Client: statusHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("older image contacted upstream metadata")
		return nil, nil
	})}
	for _, offline := range []bool{true, false} {
		var output bytes.Buffer
		err := runtimeStatus(context.Background(), manager, checker, offline, false, &output)
		if offline && err != nil || !offline && (err == nil || !strings.Contains(err.Error(), "needs a build inventory")) {
			t.Fatal("incorrect legacy status result", offline, err)
		}
		if !strings.Contains(output.String(), "Shared image: sdlc:local") || !strings.Contains(output.String(), "rebuild with sdlc runtime build") || strings.Contains(output.String(), "No updates") {
			t.Fatal(output.String())
		}
	}
	if manager.packageCalls != 0 {
		t.Fatal("legacy image requested package candidates")
	}
}

func TestRuntimeStatusOfflineListsInventoryWithoutConnections(t *testing.T) {
	manager := &statusFixture{state: runtimeimage.State{Inventory: statusInventory()}}
	checker := runtimeupdates.Checker{Client: statusHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("offline status contacted metadata")
		return nil, nil
	})}
	var output bytes.Buffer
	if err := runtimeStatus(context.Background(), manager, checker, true, false, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Codex", "0.159.3", "not checked (offline)", "1 installed; upstream checks skipped"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("missing", want, output.String())
		}
	}
	if manager.packageCalls != 0 {
		t.Fatal("offline status requested package candidates")
	}
}

func TestRuntimeStatusOnlineFailureReturnsIncompleteAndKeepsSuccessfulPackages(t *testing.T) {
	manager := &statusFixture{state: runtimeimage.State{Inventory: statusInventory()}}
	checker := runtimeupdates.Checker{Client: statusHTTP(func(request *http.Request) (*http.Response, error) {
		if _, bounded := request.Context().Deadline(); !bounded {
			t.Error("unbounded metadata request")
		}
		return nil, errors.New("fake upstream unavailable")
	})}
	var output bytes.Buffer
	err := runtimeStatus(context.Background(), manager, checker, false, true, &output)
	if err == nil || !strings.Contains(err.Error(), "dependency check incomplete") {
		t.Fatal(err)
	}
	for _, want := range []string{"Shared image: sdlc:local", "Checking public upstream metadata", "Debian git:arm64", "1 updates", "Dependency check incomplete"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("missing", want, output.String())
		}
	}
	if manager.packageCalls != 1 || strings.Contains(output.String(), "No updates") {
		t.Fatal(manager.packageCalls, output.String())
	}
}

func TestRuntimeStatusLocalFailureStopsBeforeUpstream(t *testing.T) {
	manager := &statusFixture{err: errors.New("fake image mismatch")}
	var output bytes.Buffer
	if err := runtimeStatus(context.Background(), manager, runtimeupdates.Checker{}, false, false, &output); !errors.Is(err, manager.err) || output.Len() != 0 || manager.packageCalls != 0 {
		t.Fatal("local failure did not stop status", err, output.String())
	}
}

func TestRuntimeStatusSummaryAndAllKeepMajorToolsVisible(t *testing.T) {
	inventory := statusInventory()
	inventory.Dependencies = append(inventory.Dependencies, runtimeimage.Dependency{Name: "bundled-example-package", Kind: "npm", Source: "bundled-example-package", Version: "1.0.0"})
	manager := &statusFixture{state: runtimeimage.State{Inventory: inventory}}
	checker := runtimeupdates.Checker{Client: statusHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("offline summary contacted metadata")
		return nil, nil
	})}
	for _, all := range []bool{false, true} {
		var output bytes.Buffer
		if err := runtimeStatus(context.Background(), manager, checker, true, all, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "bundled-example-package") != all {
			t.Fatal("bundled package visibility differs from --all", all, output.String())
		}
		for _, want := range []string{"Codex", "Claude", "GitHub CLI", "Skills", "Agents", "Node base", ".NET SDK"} {
			if !strings.Contains(output.String(), want) {
				t.Fatal("major tool hidden", want, output.String())
			}
		}
	}
	if manager.packageCalls != 0 {
		t.Fatal("offline summary refreshed packages")
	}
}
