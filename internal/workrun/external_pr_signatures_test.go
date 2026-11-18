package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnverifiedPRCommitsChecksEveryComparisonPage(t *testing.T) {
	first, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	missing := false
	p := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"api", "user"}) {
			return []byte(`{"id":123,"login":"example"}`), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project"}) {
			return []byte(`{"id":99,"full_name":"example/project"}`), nil
		}
		if len(args) == 4 && strings.Contains(args[1], "&page=1") {
			return json.Marshal(map[string]any{"total_commits": 2, "commits": []any{map[string]any{"sha": first, "verified": false}}})
		}
		if len(args) == 4 && strings.Contains(args[1], "&page=2") {
			if missing {
				return []byte(`{"total_commits":2,"commits":[]}`), nil
			}
			return json.Marshal(map[string]any{"total_commits": 2, "commits": []any{map[string]any{"sha": head, "verified": true}}})
		}
		return nil, fmt.Errorf("unexpected request")
	}}
	plan := Plan{Repository: "example/project"}
	pub := Publication{BaseSHA: testBase, HeadSHA: head}
	got, err := p.UnverifiedPRCommits(context.Background(), plan, pub)
	if err != nil || len(got) != 1 || got[0] != first {
		t.Fatalf("unverified commits: %v %v", got, err)
	}
	missing = true
	if _, err := p.UnverifiedPRCommits(context.Background(), plan, pub); err == nil {
		t.Fatal("incomplete comparison accepted")
	}
}

func TestExternalPRUnverifiedHistoryProducesFreshSignedPublicationWork(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	r := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := r.mergeExternalPR(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	p.unverified = []string{strings.Repeat("c", 40)}
	before := repo.revision
	if err := r.repairExternalPRSignatures(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if j.ExternalPR.SignatureRepair == nil || !j.ExternalPR.SignatureRepair.Completed || j.Plan.SourceSHA != j.ExternalPR.Boundary.ParentHeadSHA || repo.revision.Tree != before.Tree || j.State != "implementing" || j.Evidence.Passed || !j.RestackNeedsChecks {
		t.Fatal("unverified history bypassed signed publication")
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

func TestExternalPRSignatureRepairDoesNotAcceptUnverifiedPublication(t *testing.T) {
	dir, j, p, repo := externalMergeFixture(t)
	r := Runner{Publisher: p, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL}
	if err := r.mergeExternalPR(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	p.unverified = []string{strings.Repeat("c", 40)}
	if err := r.repairExternalPRSignatures(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	j.Publication.HeadSHA = strings.Repeat("9", 40)
	j.State = "ci"
	r.ExternalPRURL = ""
	r.Output = io.Discard
	r.Provider = &fakeProvider{}
	r.Checker = &fakeChecker{}
	err := r.Run(context.Background(), dir, j, "")
	if err == nil || j.State != "blocked" || !strings.Contains(j.StopReason, "unverified commits") {
		t.Fatalf("unverified signed publication was accepted: %v", err)
	}
}

func TestNormalizeExternalPRHistoryPreservesTreeAndRecovers(t *testing.T) {
	root := t.TempDir()
	aggregateGit(t, root, "init", "-b", "main")
	aggregateGit(t, root, "config", "user.name", "Test")
	aggregateGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "add", "file")
	aggregateGit(t, root, "commit", "-m", "base")
	parent := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	aggregateGit(t, root, "checkout", "-b", "incoming")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("incoming\n"), 0600); err != nil {
		t.Fatal(err)
	}
	aggregateGit(t, root, "commit", "-am", "incoming")
	aggregateGit(t, root, "checkout", "main")
	aggregateGit(t, root, "merge", "--no-ff", "-m", "merge", "incoming")
	original := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD^{tree}"))
	helper := strings.ReplaceAll(normalizeExternalPRHelper, "pathlib.Path('/workspace')", "pathlib.Path("+fmt.Sprintf("%q", root)+")")
	helper = strings.ReplaceAll(helper, "safe.directory=/workspace", "safe.directory="+root)
	run := func() error { return exec.Command("python3", "-c", helper, original, tree, parent, "main").Run() }
	if err := run(); err != nil {
		t.Fatal(err)
	}
	candidate := strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD"))
	if candidate == original || strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD^{tree}")) != tree || strings.TrimSpace(aggregateGit(t, root, "show", "-s", "--format=%P", "HEAD")) != parent {
		t.Fatal("normalization did not preserve tree and parent")
	}
	// Simulate interruption after the durable candidate marker was written,
	// but before the branch update was recorded.
	aggregateGit(t, root, "update-ref", "refs/heads/main", original, candidate)
	if err := run(); err != nil {
		t.Fatal("normalization did not recover:", err)
	}
	if strings.TrimSpace(aggregateGit(t, root, "rev-parse", "HEAD")) != candidate {
		t.Fatal("retry changed candidate")
	}
}
