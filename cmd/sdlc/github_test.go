package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
	"github.com/tjpeel/sdlc/internal/workrun"
)

type fakePairSession struct {
	account                                        githubauth.Identity
	keys                                           []string
	identityErr, keysErr, repositoryErr, closeErr  error
	identityCalls, keyCalls, repoCalls, closeCalls int
	onKeys                                         func()
}

func (s *fakePairSession) Identity(context.Context) (githubauth.Identity, error) {
	s.identityCalls++
	return s.account, s.identityErr
}
func (s *fakePairSession) Repository(_ context.Context, repo string) (githubauth.RepositoryIdentity, error) {
	s.repoCalls++
	return githubauth.RepositoryIdentity{ID: 99, Name: repo, Push: true}, s.repositoryErr
}
func (s *fakePairSession) SigningKeys(_ context.Context, login string) ([]string, error) {
	s.keyCalls++
	if login != s.account.Login {
		return nil, errors.New("wrong user selected")
	}
	if s.onKeys != nil {
		s.onKeys()
	}
	return s.keys, s.keysErr
}
func (s *fakePairSession) Close() error { s.closeCalls++; return s.closeErr }

func pairingFixture(t *testing.T) (string, signing.Profile) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), public...)
	profile, err := signing.NewProfile("personal-key", "YOUR_VAULT", "YOUR_KEY", "ssh-ed25519 "+base64.StdEncoding.EncodeToString(wire), "", filepath.Join(directory, "absent-bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := signing.Store(directory, profile, "personal-key"); err != nil {
		t.Fatal(err)
	}
	return directory, profile
}

func TestGitHubOnboardingFlagsAreScopedAndRejectAmbiguousInputs(t *testing.T) {
	for _, args := range [][]string{{"pair"}, {"pair", "--profile", "personal", "--signing-profile", "key", "--replace"}, {"use", "--profile", "work", "--repo", "example-org/project"}, {"status"}, {"status", "--profile", "work", "--verify"}, {"status", "--repo", "example/project", "--verify"}} {
		if _, err := parseGitHubOptions(args, io.Discard); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"use"}, {"pair", "--profile", ""}, {"pair", "--signing-profile", ""}, {"pair", "--repo", "example/project"}, {"pair", "--verify"}, {"use", "--replace"}, {"status", "--signing-profile", "key"}, {"status", "--profile", "../key"}, {"status", "--profile", "key", "--repo", "example/project"}, {"use", "--profile", "personal", "extra"}} {
		if _, err := parseGitHubOptions(args, io.Discard); err == nil {
			t.Fatal("ambiguous flags accepted", args)
		}
	}
	options, err := parseGitHubOptions([]string{"pair", "--profile", "personal"}, io.Discard)
	if err != nil || options.signingProfile != "personal" {
		t.Fatal("same-name convenience lost", err)
	}
}

func TestPairingPinsActualAccountAndDifferentSignerWithoutReadingSecrets(t *testing.T) {
	directory, profile := pairingFixture(t)
	session := &fakePairSession{account: githubauth.Identity{ID: 123, Login: "example-user"}, keys: []string{profile.PublicKey + " key comment"}}
	pair, err := pairGitHub(context.Background(), directory, githubOptions{profile: "personal", signingProfile: "personal-key"}, func(context.Context) (githubIdentitySession, error) { return session, nil })
	if err != nil {
		t.Fatal(err)
	}
	stored, err := githubprofile.Load(directory, "personal")
	if err != nil || stored != pair || pair.SigningProfile != "personal-key" || pair.AccountID != 123 || pair.Login != "example-user" || session.keyCalls != 1 || session.closeCalls != 1 || session.repoCalls != 0 {
		t.Fatal("wrong identity or signing metadata saved", err)
	}
	if _, err := os.Stat(profile.BootstrapFile); !os.IsNotExist(err) {
		t.Fatal("pairing touched bootstrap")
	}
	data, err := os.ReadFile(filepath.Join(directory, "github-pair.personal.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{profile.Reference, profile.BootstrapFile, "bootstrap_file", "reference", "token"} {
		if strings.Contains(string(data), private) {
			t.Fatal("pairing contains private locator or secret field")
		}
	}
}

func TestPairingRefusesUnregisteredKeyFailedIdentityAndChangedSigningMetadata(t *testing.T) {
	for _, kind := range []string{"missing key", "API failure", "invalid account", "key replaced during lookup"} {
		t.Run(kind, func(t *testing.T) {
			directory, profile := pairingFixture(t)
			session := &fakePairSession{account: githubauth.Identity{ID: 123, Login: "example-user"}, keys: []string{profile.PublicKey}}
			switch kind {
			case "missing key":
				session.keys = nil
			case "API failure":
				session.identityErr = errors.New("selected account unavailable")
			case "invalid account":
				session.account.ID = 0
			case "key replaced during lookup":
				session.onKeys = func() {
					changed := profile
					changed.ID = "changed-key"
					if err := signing.Store(directory, changed, "personal-key"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := pairGitHub(context.Background(), directory, githubOptions{profile: "personal", signingProfile: "personal-key"}, func(context.Context) (githubIdentitySession, error) { return session, nil }); err == nil {
				t.Fatal("unverified pairing saved")
			}
			if _, err := githubprofile.Load(directory, "personal"); !os.IsNotExist(err) {
				t.Fatal("failed onboarding persisted pairing", err)
			}
			if session.closeCalls != 1 {
				t.Fatal("native cache lease not closed")
			}
		})
	}
}

func TestConnectedPairCheckStopsChangedAccountBeforeKeyOrRepositoryRequests(t *testing.T) {
	for _, account := range []githubauth.Identity{{ID: 124, Login: "example-user"}, {ID: 123, Login: "other-user"}} {
		session := &fakePairSession{account: account}
		if err := checkPair(context.Background(), githubprofile.Pair{AccountID: 123, Login: "example-user"}, session, "example/project"); err == nil || session.keyCalls != 0 || session.repoCalls != 0 {
			t.Fatal("changed account reached repository lookup", err)
		}
	}
	session := &fakePairSession{account: githubauth.Identity{ID: 123, Login: "example-user"}, keys: []string{"ssh-ed25519 ABCD"}, repositoryErr: errors.New("push denied")}
	if err := checkPair(context.Background(), githubprofile.Pair{AccountID: 123, Login: "example-user", PublicKey: "ssh-ed25519 ABCD"}, session, "example/project"); err == nil || session.repoCalls != 1 {
		t.Fatal("repository access denial lost", err)
	}
}

func TestNewRunKeepsIndependentSignerAndRejectsPairChangesWhileLegacyResumeWorks(t *testing.T) {
	directory, profile := pairingFixture(t)
	pair := githubprofile.Pair{Version: 1, GitHubProfile: "personal", AccountID: 123, Login: "example-user", SigningProfile: "personal-key", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
	if err := githubprofile.Store(directory, pair, false); err != nil {
		t.Fatal(err)
	}
	plan := workrun.Plan{GitHubProfile: "personal", SigningProfile: "personal-key", PublicationIdentity: &workrun.PublicationIdentity{GitHubProfile: "personal", GitHubID: 123, GitHubLogin: "example-user", ProfileID: profile.ID, SSHPublicKey: profile.PublicKey, SSHFingerprint: profile.Fingerprint}}
	runtime := runtimeimage.Manager{Directory: directory}
	if got, err := runSigningProfile(runtime, plan); err != nil || got.ID != profile.ID {
		t.Fatal("independent signer lost", err)
	}
	changed := pair
	changed.AccountID = 124
	if err := githubprofile.Store(directory, changed, true); err != nil {
		t.Fatal(err)
	}
	if _, err := runSigningProfile(runtime, plan); err == nil {
		t.Fatal("frozen run followed changed pairing")
	}
	legacy := plan
	legacy.SigningProfile = ""
	legacy.GitHubProfile = "personal-key"
	legacy.PublicationIdentity.GitHubProfile = "personal-key"
	if _, err := runSigningProfile(runtime, legacy); err != nil {
		t.Fatal("legacy frozen same-name signer changed", err)
	}
}

func TestMissingPairFailsBeforeCaptureRuntimeOrProviderRequests(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	if err := runCommand(context.Background(), runArgs("--repo", "example/project"), io.Discard); err == nil || !strings.Contains(err.Error(), "github use") {
		t.Fatal("unconfigured launch admitted", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unconfigured selection contacted Docker/provider")
	}
	if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatal("unconfigured selection created runtime state")
	}
	entries, err := os.ReadDir(filepath.Join(root, ".sdlc", "work", "TASK-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "runs" {
			t.Fatal("unconfigured launch captured workspace")
		}
	}
}

func TestRepositoryUseStatusAndRunResolvePairedSignerOffline(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	directory, profile := pairingFixture(t)
	t.Setenv("SDLC_STATE_DIR", directory)
	pair := githubprofile.Pair{Version: 1, GitHubProfile: "personal", AccountID: 123, Login: "example-user", SigningProfile: "personal-key", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
	if err := githubprofile.Store(directory, pair, false); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", root, "remote", "add", "origin", "git"+"@"+"github.com:example-org/project.git")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if err := githubCommand(context.Background(), []string{"status"}, io.Discard); err == nil {
		t.Fatal("organisation guessed an account")
	}
	if err := githubCommand(context.Background(), []string{"use", "--profile", "personal"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := githubCommand(context.Background(), []string{"status"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Account: example-user") || !strings.Contains(output.String(), "Signing profile: personal-key") || !strings.Contains(output.String(), "unverified") {
		t.Fatal("status omitted selected pair or overstated verification", output.String())
	}
	output.Reset()
	if err := runCommand(context.Background(), runArgs("--dry-run"), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"github_profile":"personal"`) || !strings.Contains(output.String(), `"signing_profile":"personal-key"`) {
		t.Fatal("run did not use repository's pair", output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("local selection contacted Docker/provider")
	}
	for _, path := range []string{filepath.Join(root, ".sdlc", "profiles.local.json"), filepath.Join(root, "profiles.local.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("local selection copied private metadata into checkout")
		}
	}
	command = exec.Command("git", "-C", root, "remote", "set-url", "origin", "git"+"@"+"github.com:other-org/project.git")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(data))
	}
	if err := githubCommand(context.Background(), []string{"status"}, io.Discard); err == nil {
		t.Fatal("status reused selection after remote changed")
	}
	if err := runCommand(context.Background(), runArgs("--dry-run"), io.Discard); err == nil {
		t.Fatal("dry run reused selection after remote changed")
	}
}

func TestResumeStopsChangedAccountBeforeRepositoryAccess(t *testing.T) {
	frozen := &workrun.PublicationIdentity{GitHubID: 123, GitHubLogin: "example-user", SSHPublicKey: "ssh-ed25519 ABCD", RepositoryID: 99, RepositoryName: "example/project"}
	plan := workrun.Plan{Repository: "example/project", PublicationIdentity: frozen}
	session := &fakePairSession{account: githubauth.Identity{ID: 124, Login: "example-user"}, keys: []string{frozen.SSHPublicKey}}
	if err := checkFrozenGitHub(context.Background(), plan, session); err == nil || session.keyCalls != 0 || session.repoCalls != 0 {
		t.Fatal("resume followed another account", err)
	}
	session = &fakePairSession{account: githubauth.Identity{ID: 123, Login: "example-user"}, keys: []string{frozen.SSHPublicKey}}
	if err := checkFrozenGitHub(context.Background(), plan, session); err != nil {
		t.Fatal("valid frozen identity rejected", err)
	}
	frozen.RepositoryID = 100
	if err := checkFrozenGitHub(context.Background(), plan, session); err == nil {
		t.Fatal("resume followed replaced repository")
	}
}

func TestResumeRechecksPairAfterAcquisitionAndPreservesLegacyBlankDefault(t *testing.T) {
	state, marker, _ := queuedRunFixture(t)
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(".")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := githubprofile.Load(state, "default")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := freezePublicationIdentity(context.Background(), runtime, root, "example/project", pair)
	if err != nil {
		t.Fatal(err)
	}
	journal := workrun.Journal{ImageID: "sha256:" + strings.Repeat("a", 64), Plan: workrun.Plan{GitHubProfile: "default", SigningProfile: "default", Repository: "example/project", PublicationIdentity: frozen}}
	profile, err := signingProfile(runtime, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := signing.Store(state, profile, "replacement"); err != nil {
		t.Fatal(err)
	}
	changed := pair
	changed.SigningProfile = "replacement"
	data, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(state, "replacement-pair.json")
	if err := os.WriteFile(replacement, data, 0600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(state, "fake-bin", "docker")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	modified := strings.Replace(string(script), "status) printf", "status) cp "+quote(replacement)+" "+quote(filepath.Join(state, "github-pair.default.local.json"))+"; printf", 1)
	if modified == string(script) {
		t.Fatal("fixture status hook not installed")
	}
	if err := os.WriteFile(scriptPath, []byte(modified), 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(state, "fake-bin", "metadata-calls")
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if err := resumeGitHubPreflight(context.Background(), runtime, journal); err == nil || !strings.Contains(err.Error(), "paired account differs") {
		t.Fatal("resume followed changed signer pairing", err)
	}
	data, err = os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "github-identity") {
		t.Fatal("changed pair reached account lookup")
	}
	legacy := journal
	legacy.Plan.SigningProfile = ""
	legacy.Plan.GitHubProfile = ""
	legacy.Plan.PublicationIdentity.GitHubProfile = ""
	if err := resumeGitHubPreflight(context.Background(), runtime, legacy); err != nil {
		t.Fatal("legacy blank default profile rejected", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("resume used forbidden provider/signing operation")
	}
}
