package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCompanionCloner struct {
	calls      []string
	revision   string
	err        error
	workspaces []string
}

func (c *fakeCompanionCloner) CloneCompanionRepository(_ context.Context, _ Plan, workspace, repository string) (string, error) {
	c.calls = append(c.calls, repository)
	c.workspaces = append(c.workspaces, workspace)
	return c.revision, c.err
}

func TestCompanionRepositoryRequestValidation(t *testing.T) {
	plan := Plan{Repository: "example-org/notifications"}
	valid := CompanionRepositoryRequest{ID: "source", Repository: "example-org/companion", Purpose: "inspect the linked contract"}
	if err := validateCompanionRepositoryRequests([]CompanionRepositoryRequest{valid}, &plan); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{
		"OTHER/private-repo",
		"example-org/../outside",
		"example-org/repo/subdir",
		"/example-org/repo",
		"example-org/.",
	} {
		request := valid
		request.Repository = repository
		if err := validateCompanionRepositoryRequests([]CompanionRepositoryRequest{request}, &plan); err == nil {
			t.Errorf("accepted companion repository %q", repository)
		}
	}
	outcome := testOutcome("waiting_for_human")
	outcome.Questions = []string{"The source is missing."}
	outcome.RepositoryRequests = []CompanionRepositoryRequest{valid}
	if err := validateOutcome(outcome, "implementation"); err != nil {
		t.Fatal(err)
	}
	if err := validateOutcome(outcome, "review"); err == nil {
		t.Fatal("accepted companion repository request from review role")
	}
}

func TestOutcomeCompanionRequestsAreArrays(t *testing.T) {
	for _, body := range []string{`{"repository_requests":null}`, `{"repository_requests":{}}`} {
		var outcome Outcome
		if json.Unmarshal([]byte(body), &outcome) == nil {
			t.Errorf("accepted invalid companion requests: %s", body)
		}
	}
	var outcome Outcome
	if err := json.Unmarshal([]byte(`{"repository_requests":[]}`), &outcome); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionRepositoryRequestStagesSourceAndResumesSameSession(t *testing.T) {
	directory, journal := testRun(t)
	journal.Plan.Repository = "example-org/notifications"
	requested := testOutcome("waiting_for_human")
	requested.Questions = []string{"The companion source is missing."}
	requested.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example-org/companion", Purpose: "inspect private source"}}
	waiting := testOutcome("waiting_for_human")
	waiting.Questions = []string{"Which API contract applies?"}
	provider := &fakeProvider{outcomes: []Outcome{requested, waiting}}
	cloner := &fakeCompanionCloner{revision: strings.Repeat("a", 40)}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	runner.CompanionCloner = cloner
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("unexpected run result: %v", err)
	}
	if len(cloner.calls) != 1 || cloner.calls[0] != "example-org/companion" || cloner.workspaces[0] != journal.Workspace {
		t.Fatalf("unexpected clone requests: %+v", cloner)
	}
	if len(provider.calls) != 2 || provider.calls[1].ResumeID != testNative {
		t.Fatalf("provider session did not resume after clone: %+v", provider.calls)
	}
	for _, evidence := range []string{"example-org/companion", ".sdlc/repositories/example-org/companion", strings.Repeat("a", 40), "configured GitHub profile"} {
		if !strings.Contains(provider.calls[1].Prompt, evidence) {
			t.Errorf("resumed prompt is missing %q", evidence)
		}
	}
	if strings.Contains(provider.calls[1].Prompt, "inspect private source") {
		t.Fatal("request rationale was repeated into controller feedback")
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.CompanionRepositories) != 1 || loaded.CompanionRepositories[0].Path != ".sdlc/repositories/example-org/companion" || loaded.CompanionRepositories[0].Revision != strings.Repeat("a", 40) {
		t.Fatalf("staged repository was not checkpointed: %+v", loaded.CompanionRepositories)
	}
}

func TestCompanionRepositoryCloneFailureRequestsHumanAccessRepair(t *testing.T) {
	directory, journal := testRun(t)
	journal.Plan.Repository = "example-org/notifications"
	requested := testOutcome("waiting_for_human")
	requested.Questions = []string{"The companion source is missing."}
	requested.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example-org/companion", Purpose: "inspect linked source"}}
	provider := &fakeProvider{outcomes: []Outcome{requested}}
	cloner := &fakeCompanionCloner{err: &companionSourceAccessError{repository: "example-org/companion"}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	runner.CompanionCloner = cloner
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("unexpected run result: %v", err)
	}
	if journal.State != "waiting_for_human" || journal.PendingRole != "implementation" || len(journal.Outcome.Questions) != 1 {
		t.Fatalf("clone failure did not retain an actionable checkpoint: %+v", journal)
	}
}

func TestCompanionControllerFailureBlocksWithoutAskingForAccountAccess(t *testing.T) {
	directory, journal := testRun(t)
	journal.Plan.Repository = "example-org/notifications"
	requested := testOutcome("waiting_for_human")
	requested.Questions = []string{"The companion source is missing."}
	requested.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example-org/companion", Purpose: "inspect linked source"}}
	provider := &fakeProvider{outcomes: []Outcome{requested}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	runner.CompanionCloner = &fakeCompanionCloner{err: errors.New("private copy failure")}
	if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("unexpected run result: %v", err)
	}
	if journal.State != "blocked" || strings.Contains(journal.StopReason, "read access") {
		t.Fatalf("controller failure was misreported as an access question: %+v", journal)
	}
}

func TestCompanionRequestCannotResumeBlockedOrFailedProvider(t *testing.T) {
	for _, status := range []string{"blocked", "failed"} {
		t.Run(status, func(t *testing.T) {
			directory, journal := testRun(t)
			journal.Plan.Repository = "example-org/notifications"
			outcome := testOutcome(status)
			outcome.Summary = "Provider access or usage requires attention."
			outcome.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source", Repository: "example-org/companion", Purpose: "inspect linked source"}}
			provider := &fakeProvider{outcomes: []Outcome{outcome}}
			cloner := &fakeCompanionCloner{revision: strings.Repeat("a", 40)}
			runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			runner.CompanionCloner = cloner
			if err := runner.Run(context.Background(), directory, journal, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			if journal.State != status || journal.StopReason != outcome.Summary || len(provider.calls) != 1 || len(cloner.calls) != 0 {
				t.Fatal("repository request bypassed the provider stop", journal.State, provider.calls, cloner.calls)
			}
		})
	}
}

func TestCompanionCloneFailureOnlyRequestsHumanForAccessErrors(t *testing.T) {
	for _, test := range []struct {
		message string
		access  bool
	}{
		{message: "GraphQL: Could not resolve to a Repository with the name", access: true},
		{message: "HTTP 403: Resource not accessible", access: true},
		{message: "unknown option `env`", access: false},
		{message: "connection timed out", access: false},
	} {
		if got := companionCloneRequiresHumanAccess(errors.New(test.message)); got != test.access {
			t.Errorf("access classification for %q = %t, want %t", test.message, got, test.access)
		}
	}
}

func TestCompanionCloneUsesConfiguredReadOnlyGitHubVolume(t *testing.T) {
	args := companionCloneArguments("sha256:runtime", "sdlc-companion-example", "sdlc-github-auth-profile-github", "example-org/companion")
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--network bridge",
		"--detach",
		"--read-only",
		"--cap-drop ALL",
		"--user 1000:1000",
		"type=volume,src=sdlc-github-auth-profile-github,dst=/github-auth,readonly,volume-nocopy",
		"--tmpfs /tmp:rw,nosuid,nodev,noexec,size=128m,mode=1777",
		"GH_CONFIG_DIR=/github-auth",
		"GH_PROMPT_DISABLED=1",
		"--entrypoint /bin/sleep",
		"sha256:runtime infinity",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("clone command is missing %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"/provider-auth", "signing-key", "1password", "GH_TOKEN=disposable", "GITHUB_TOKEN=disposable"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("clone command includes %q", forbidden)
		}
	}
	if strings.Contains(joined, string([]byte{0})) || strings.Contains(joined, "--entrypoint /bin/sh") || strings.Contains(joined, "type=bind") {
		t.Fatal("clone command contains unsafe execution details")
	}
	imagePosition := strings.Index(joined, "sha256:runtime infinity")
	for _, variable := range []string{"HTTP_PROXY=", "GH_TOKEN=", "GITHUB_TOKEN="} {
		position := strings.Index(joined, "--env "+variable)
		if position < 0 || imagePosition < 0 || position > imagePosition {
			t.Errorf("environment override %q follows the image command: %s", variable, joined)
		}
	}
	clone := strings.Join(companionCloneCommand("sdlc-companion-example", "example-org/companion"), " ")
	if clone != "exec sdlc-companion-example /usr/local/bin/gh repo clone https://github.com/example-org/companion /tmp/companion --no-upstream" {
		t.Fatalf("unexpected official clone command: %s", clone)
	}
	copyArgs := companionCopyArguments("sdlc-companion-example")
	if strings.Join(copyArgs, " ") != "exec sdlc-companion-example /bin/tar -C /tmp/companion -czf - ." {
		t.Fatalf("unexpected companion copy command: %v", copyArgs)
	}
}

func TestCompanionCloneValidatesRemoteAndIgnoresCheckout(t *testing.T) {
	sshRemote := "git" + "@" + "github.com:example-org/companion"
	sshURL := "ssh://" + "git" + "@" + "github.com/example-org/companion"
	for _, remote := range []string{
		"https://github.com/example-org/companion.git",
		"https://github.com/example-org/companion",
		sshRemote + ".git",
		sshURL + ".git",
	} {
		if !companionRemoteMatches(remote, "example-org/companion") {
			t.Errorf("rejected official GitHub remote %q", remote)
		}
	}
	for _, remote := range []string{
		"https://github.com.attacker.invalid/example-org/companion",
		"https://example.invalid/example-org/companion",
		"git" + "@" + "attacker.invalid:example-org/companion.git",
	} {
		if companionRemoteMatches(remote, "example-org/companion") {
			t.Errorf("accepted unexpected remote %q", remote)
		}
	}

	workspace, _ := sourceFixture(t)
	if err := excludeCompanionSource(workspace); err != nil {
		t.Fatal(err)
	}
	if err := excludeCompanionSource(workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "/.sdlc/repositories/") != 1 {
		t.Fatalf("companion checkout exclusion missing or duplicated: %s", data)
	}
}

func TestCompanionSourceIsReadOnlyToProviderWorker(t *testing.T) {
	root := t.TempDir()
	restoreCompanionTestPermissions(t, root)
	directory := filepath.Join(root, "src")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "source.cs")
	if err := os.WriteFile(file, []byte("class Example {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := makeCompanionReadOnly(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, directory, file} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0222 != 0 {
			t.Errorf("companion path remains writable %q: %v", path, info.Mode())
		}
	}
}
