package githubprofile

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/signing"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func testPair(t *testing.T, name, login string, id int64) Pair {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), public...)
	profile, err := signing.NewProfile("key-"+name, "YOUR_VAULT", "YOUR_KEY", "ssh-ed25519 "+base64.StdEncoding.EncodeToString(wire), "", filepath.Join(privateDirectory(t), "unused-bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	return Pair{1, name, id, login, "key-" + name, profile.ID, profile.PublicKey, profile.Fingerprint}
}

func TestRoutingSelectsUniqueOwnerAndExplicitOrganisationWithoutWritingCheckout(t *testing.T) {
	directory, root := privateDirectory(t), privateDirectory(t)
	personal, work := testPair(t, "personal", "example-user", 1), testPair(t, "work", "work-user", 2)
	for _, pair := range []Pair{personal, work} {
		if err := Store(directory, pair, false); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := Select(directory, root, "EXAMPLE-USER/Project", "")
	if err != nil || pair != personal {
		t.Fatal("unique personal owner not selected", pair, err)
	}
	if _, err := Select(directory, root, "example-org/project", ""); !errors.Is(err, ErrSelectionRequired) {
		t.Fatal("organisation guessed an account", err)
	}
	if err := SaveSelection(directory, root, "example-org/project", work); err != nil {
		t.Fatal(err)
	}
	pair, err = Select(directory, root, "Example-Org/Project", "")
	if err != nil || pair != work {
		t.Fatal("saved organisation account not selected", err)
	}
	if _, err := Select(directory, root, "example-org/project", "personal"); err == nil {
		t.Fatal("explicit profile bypassed saved selection")
	}
	if _, err := Select(directory, root, "other-org/project", ""); err == nil {
		t.Fatal("changed remote reused account selection")
	}
	if err := SaveSelection(directory, root, "other-org/project", personal); err != nil {
		t.Fatal(err)
	}
	pair, err = Select(directory, root, "other-org/project", "")
	if err != nil || pair != personal {
		t.Fatal("deliberate rebind failed", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("private routing wrote into checkout", err)
	}
}

func TestAmbiguousOwnerRequiresDeliberateChoiceAndWorktreesRemainSeparate(t *testing.T) {
	directory, root, worktree := privateDirectory(t), privateDirectory(t), privateDirectory(t)
	first, second := testPair(t, "personal", "example", 1), testPair(t, "alternate", "example", 1)
	for _, pair := range []Pair{first, second} {
		if err := Store(directory, pair, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Select(directory, root, "example/project", ""); err == nil || errors.Is(err, ErrSelectionRequired) {
		t.Fatal("ambiguous owner silently selected", err)
	}
	if pair, err := Select(directory, root, "example/project", "alternate"); err != nil || pair != second {
		t.Fatal(err)
	}
	if err := SaveSelection(directory, root, "example/project", first); err != nil {
		t.Fatal(err)
	}
	if _, err := Select(directory, worktree, "example/project", ""); err == nil {
		t.Fatal("another worktree inherited private checkout selection")
	}
	if err := SaveSelection(directory, worktree, "example/project", second); err != nil {
		t.Fatal(err)
	}
	if pair, err := Select(directory, worktree, "example/project", ""); err != nil || pair != second {
		t.Fatal(err)
	}
}

func TestPairReplacementInvalidatesSavedSelectionUntilRebound(t *testing.T) {
	directory, root := privateDirectory(t), privateDirectory(t)
	first, changed := testPair(t, "personal", "example", 1), testPair(t, "personal", "other-user", 2)
	if err := Store(directory, first, false); err != nil {
		t.Fatal(err)
	}
	if err := Store(directory, first, false); err != nil {
		t.Fatal("idempotent pairing failed", err)
	}
	if err := SaveSelection(directory, root, "example-org/project", first); err != nil {
		t.Fatal(err)
	}
	if err := Store(directory, changed, false); err == nil {
		t.Fatal("account/key replaced without explicit flag")
	}
	if err := Store(directory, changed, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Select(directory, root, "example-org/project", ""); err == nil {
		t.Fatal("saved repo followed repointed profile")
	}
	if err := SaveSelection(directory, root, "example-org/project", first); err == nil {
		t.Fatal("stale pairing saved during rebind")
	}
	if err := SaveSelection(directory, root, "example-org/project", changed); err != nil {
		t.Fatal(err)
	}
}

func TestReadsNeverCreateMissingStateAndRefuseInvalidMetadata(t *testing.T) {
	directory := filepath.Join(privateDirectory(t), "missing")
	root := privateDirectory(t)
	if pairs, err := List(directory); err != nil || len(pairs) != 0 {
		t.Fatal(err)
	}
	if _, err := Select(directory, root, "example/project", ""); !errors.Is(err, ErrSelectionRequired) {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("read created state")
	}
	directory = privateDirectory(t)
	pair := testPair(t, "personal", "example", 1)
	if err := Store(directory, pair, false); err != nil {
		t.Fatal(err)
	}
	file, _ := pairFile(pair.GitHubProfile)
	path := filepath.Join(directory, file)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(string(valid), `"version": 1`, `"version": 2`, 1), strings.Replace(string(valid), `"github_profile": "personal"`, `"github_profile": "other"`, 1), string(valid) + ` {}`, strings.Replace(string(valid), `"version": 1`, `"token": "fake", "version": 1`, 1)} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := List(directory); err == nil {
			t.Fatal("invalid profile metadata ignored")
		}
	}
}

func TestMissingBoundPairIsAnErrorRatherThanUnconfiguredSelection(t *testing.T) {
	directory, root := privateDirectory(t), privateDirectory(t)
	pair := testPair(t, "personal", "example", 1)
	if err := Store(directory, pair, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveSelection(directory, root, "example/project", pair); err != nil {
		t.Fatal(err)
	}
	file, _ := pairFile("personal")
	if err := os.Remove(filepath.Join(directory, file)); err != nil {
		t.Fatal(err)
	}
	_, err := Select(directory, root, "example/project", "")
	if err == nil || os.IsNotExist(err) || errors.Is(err, ErrSelectionRequired) {
		t.Fatal("missing bound pair downgraded to unconfigured", err)
	}
}

func TestPrivateStorageRefusesUnsafeFilesAndLocations(t *testing.T) {
	for _, kind := range []string{"public file", "symlink", "hardlink", "public directory", "repo directory", "lock symlink", "selection symlink"} {
		t.Run(kind, func(t *testing.T) {
			directory, root := privateDirectory(t), privateDirectory(t)
			pair := testPair(t, "personal", "example", 1)
			if err := Store(directory, pair, false); err != nil {
				t.Fatal(err)
			}
			file, _ := pairFile(pair.GitHubProfile)
			path := filepath.Join(directory, file)
			switch kind {
			case "public file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "repo directory":
				if err := os.Mkdir(filepath.Join(directory, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			case "lock symlink":
				lock := filepath.Join(directory, "github-pairing.lock")
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, lock); err != nil {
					t.Fatal(err)
				}
			case "selection symlink":
				file, err := selectionFile(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(directory, file)); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "selection symlink" {
				if err := SaveSelection(directory, root, "example/project", pair); err == nil {
					t.Fatal("unsafe selection replaced")
				}
				return
			}
			if err := Store(directory, pair, true); err == nil {
				t.Fatal("unsafe storage replaced")
			}
			if kind != "lock symlink" {
				if _, err := Load(directory, "personal"); err == nil {
					t.Fatal("unsafe storage read")
				}
			}
		})
	}
}

func TestPairCannotContainInvalidAccountKeyOrProfile(t *testing.T) {
	valid := testPair(t, "personal", "example", 1)
	for _, change := range []func(*Pair){func(p *Pair) { p.AccountID = 0 }, func(p *Pair) { p.GitHubProfile = "../other" }, func(p *Pair) { p.SigningProfile = "" }, func(p *Pair) { p.Login = "other\n" }, func(p *Pair) { p.PublicKey = "ssh-ed25519 AAAA" }, func(p *Pair) { p.Fingerprint = "SHA256:invalid" }, func(p *Pair) { p.SigningID = "x\n" }} {
		pair := valid
		change(&pair)
		if err := pair.Validate(); err == nil {
			t.Fatal("invalid account/key admitted")
		}
	}
}
