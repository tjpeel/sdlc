package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenPublisherImportsRewrittenCandidateIntoExistingPrivateCache(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	var err error
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	git := func(root string, args ...string) string {
		t.Helper()
		out, err := (GitHubPublisher{Command: localCommand}).git(ctx, root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git(source, "init")
	git(source, "config", "user.name", "Offline Implementation")
	git(source, "config", "user.email", "offline@example.invalid")
	git(source, "config", "commit.gpgsign", "false")
	commit := func(content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "example.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		git(source, "add", ".")
		git(source, "commit", "-m", message)
		return git(source, "rev-parse", "HEAD")
	}
	base := commit("base\n", "Captured source")
	original := commit("original implementation\n", "Original implementation")
	bundle := filepath.Join(directory, "source.bundle")
	git(source, "bundle", "create", bundle, "HEAD")
	key := filepath.Join(t.TempDir(), "disposable-signing-key")
	if out, err := exec.CommandContext(ctx, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("disposable signing key: %s: %v", out, err)
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	identity := *frozenTestIdentity()
	identity.SSHPublicKey = strings.Join(strings.Fields(string(public))[:2], " ")
	allowed := filepath.Join(t.TempDir(), "allowed-signers")
	if err := os.WriteFile(allowed, []byte("* "+identity.SSHPublicKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Reference: "TASK-1", Repository: "example/project", Branch: "TASK-1", Base: "main", SourceSHA: base, BaseSHA: base, PublicationIdentity: &identity, PRTitle: "TASK-1 selected implementation", PRBody: "Implements TASK-1."}
	signed := 0
	publisher := GitHubPublisher{Frozen: &identity, BundlePath: bundle, ExpectedHead: original, ExpectedTree: git(source, "rev-parse", "HEAD^{tree}"), SigningKeyPath: key, AllowedSignersPath: allowed, AfterSigning: func() error { signed++; return errors.New("offline stop before remote publication") }}
	publisher.Command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return localCommand(ctx, name, args...)
		}
		if name != "gh" {
			t.Fatalf("unexpected executable %s", name)
		}
		if len(args) > 1 && args[0] == "auth" && args[1] == "status" {
			return nil, nil
		}
		if len(args) == 2 && args[0] == "api" {
			switch args[1] {
			case "user":
				return json.Marshal(map[string]any{"id": identity.GitHubID, "login": identity.GitHubLogin})
			case "repos/example/project":
				return json.Marshal(map[string]any{"id": identity.RepositoryID, "full_name": identity.RepositoryName})
			case "repos/example/project/branches/main":
				return json.Marshal(map[string]any{"commit": map[string]string{"sha": base}})
			}
		}
		t.Fatalf("unexpected connected publication command: %v", args)
		return nil, nil
	}
	stopAfterSigning := func(want int) {
		t.Helper()
		_, err := publisher.Publish(ctx, plan, "", directory, Publication{}, nil)
		if err == nil || !strings.Contains(err.Error(), "signing credential cleanup failed before publication") || signed != want {
			t.Fatalf("candidate failed before real signing/tree proof: signed=%d error=%v", signed, err)
		}
	}
	stopAfterSigning(1)
	cache := filepath.Join(directory, "publication.git")
	if git(cache, "rev-parse", "refs/sdlc/candidate") != original {
		t.Fatal("original candidate not retained")
	}
	git(source, "reset", "--hard", base)
	rewritten := commit("corrected implementation\n", "Correct unpublished history")
	if _, err := (GitHubPublisher{Command: localCommand}).git(ctx, source, "merge-base", "--is-ancestor", original, rewritten); err == nil {
		t.Fatal("fixture was not a non-fast-forward history correction")
	}
	if err := os.Remove(bundle); err != nil {
		t.Fatal(err)
	}
	git(source, "bundle", "create", bundle, "HEAD")
	publisher.ExpectedHead, publisher.ExpectedTree = rewritten, git(source, "rev-parse", "HEAD^{tree}")
	stopAfterSigning(2)
	if git(cache, "rev-parse", "refs/sdlc/candidate") != rewritten {
		t.Fatal("rewritten candidate not imported into retained cache")
	}
	publisher.ExpectedHead = original
	if _, err := publisher.Publish(ctx, plan, "", directory, Publication{}, nil); err == nil || !strings.Contains(err.Error(), "exported candidate differs from tested revision") || signed != 2 {
		t.Fatal("local forced import bypassed tested revision proof", err)
	}
}
