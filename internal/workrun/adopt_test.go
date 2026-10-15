package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePRURLRejectsAmbiguousOrUnsafeTargets(t *testing.T) {
	withUser := url.URL{Scheme: "https", Host: "github.com", User: url.User("example"), Path: "/example/project/pull/1"}
	if _, _, err := ParsePRURL(withUser.String()); err == nil {
		t.Fatal("accepted URL user information")
	}
	repo, n, err := ParsePRURL("https://github.com/example/project/pull/17")
	if err != nil || repo != "example/project" || n != 17 {
		t.Fatal(repo, n, err)
	}
	for _, raw := range []string{"https://github.com/example/project/pull/0", "https://github.com/example/project/pull/017", "http://github.com/example/project/pull/1", "https://other.invalid/example/project/pull/1", "https://github.com/example/project/pull/1?x=y", "https://github.com/example/project/pull/1/files", "https://github.com/example/project/pull/1#comment", "https://github.com/example%2Fproject/pull/1"} {
		if _, _, err := ParsePRURL(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func adoptionBoundary() PRAdoption {
	return PRAdoption{URL: "https://github.com/example/project/pull/1", Number: 1, Branch: "TASK-1", Base: "main", BaseSHA: testBase, HeadSHA: testHead, Title: "Existing title", Body: "Existing description"}
}

func TestAdoptionRejectsClosedForkAndMovedPRs(t *testing.T) {
	for _, mode := range []string{"open", "closed", "fork", "head", "base", "branch", "number"} {
		t.Run(mode, func(t *testing.T) {
			a := adoptionBoundary()
			plan := Plan{Adoption: &a, Repository: "example/project", Branch: a.Branch, Base: a.Base, BaseSHA: a.BaseSHA, SourceSHA: a.HeadSHA, StartingSHA: a.HeadSHA}
			calls := 0
			publisher := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "git" && args[0] == "check-ref-format" {
					return nil, nil
				}
				if name != "gh" {
					t.Fatal(name, args)
				}
				if args[0] == "api" {
					if args[1] == "user" {
						return []byte(`{"id":123,"login":"example"}`), nil
					}
					return []byte(`{"id":99,"full_name":"example/project"}`), nil
				}
				calls++
				record := map[string]any{"number": 1, "url": a.URL, "state": "OPEN", "isCrossRepository": false, "headRefName": a.Branch, "baseRefName": a.Base, "headRefOid": a.HeadSHA, "baseRefOid": a.BaseSHA, "title": a.Title, "body": a.Body}
				switch mode {
				case "closed":
					record["state"] = "CLOSED"
				case "fork":
					record["isCrossRepository"] = true
				case "head":
					record["headRefOid"] = testSigned
				case "base":
					record["baseRefOid"] = testTree
				case "branch":
					record["headRefName"] = "other"
				case "number":
					record["number"] = 2
				}
				return json.Marshal(record)
			}}
			previous := Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
			err := publisher.verifyAdoption(context.Background(), plan, previous, testHead)
			if (err == nil) != (mode == "open") || calls != 1 {
				t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
			}
		})
	}
}

func TestCaptureAdoptionUsesRemoteHeadAndSeparateAdvancedBase(t *testing.T) {
	ctx := context.Background()
	root, launch := sourceFixture(t)
	git := func(args ...string) string {
		t.Helper()
		out, err := SafeGit(ctx, root, args...)
		if err != nil {
			t.Fatal(args, err)
		}
		return strings.TrimSpace(out)
	}
	git("checkout", "-b", "feedback-fix")
	sourceWrite(t, root, "README.md", "remote PR implementation\n")
	git("add", "README.md")
	git("commit", "-m", "Existing PR implementation")
	head := git("rev-parse", "HEAD")
	git("checkout", "main")
	sourceWrite(t, root, "base-only.txt", "base advanced\n")
	git("add", "base-only.txt")
	git("commit", "-m", "Advance base independently")
	base := git("rev-parse", "HEAD")
	git("update-ref", "refs/sdlc/snapshot", head)
	git("update-ref", "refs/sdlc/base", base)
	bundle := filepath.Join(realTemp(t), "adopt.bundle")
	git("bundle", "create", bundle, "refs/sdlc/snapshot", "refs/sdlc/base")
	sourceWrite(t, root, "README.md", "unrelated local edits\n")
	a := PRAdoption{URL: "https://github.com/example/project/pull/1", Number: 1, Branch: "feedback-fix", Base: "main", HeadSHA: head, BaseSHA: base, Title: "Keep title", Body: "Keep description"}
	destination := filepath.Join(realTemp(t), "workspace")
	plan, err := CaptureAdoption(ctx, launch, BranchSnapshot{Branch: a.Branch, SHA: head, Bundle: bundle}, a, destination, "example/project", DefaultModels().Codex)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "README.md"))
	if err != nil || string(data) != "remote PR implementation\n" {
		t.Fatalf("captured local source: %q %v", data, err)
	}
	if plan.StartingSHA != head || plan.SourceSHA != head || plan.BaseSHA != base || plan.Root != root || plan.PRTitle != a.Title || plan.PRBody != a.Body || plan.PRMetadataPolicy != nil {
		t.Fatalf("wrong boundary: %+v", plan)
	}
	if _, err := SafeGit(ctx, destination, "cat-file", "-e", base+"^{commit}"); err != nil {
		t.Fatal("lost advanced base", err)
	}
	if err := validateAdoptionPlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "base-only.txt")); !os.IsNotExist(err) {
		t.Fatal("merged unrelated base changes into PR")
	}
}

func TestAdoptionStartsFreshFeedbackSessionAndRetainsChecks(t *testing.T) {
	for _, withComment := range []bool{true, false} {
		t.Run(map[bool]string{true: "comment", false: "no comments"}[withComment], func(t *testing.T) {
			dir, j := testRun(t)
			a := adoptionBoundary()
			j.Plan.Adoption = &a
			j.Plan.SourceSHA = testHead
			j.Plan.StartingSHA = testHead
			j.Plan.PRTitle = a.Title
			j.Plan.PRBody = a.Body
			j.State = "adopting"
			j.Publication = Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
			c := &fakeChecker{}
			pub := &feedbackPublisherFake{fakePublisher: &fakePublisher{}}
			outcomes := []Outcome{testOutcome("implemented"), testOutcome("implemented"), testOutcome("reviewed")}
			comment := generalFeedback("comment:1", "Explain the current behaviour")
			if withComment {
				pub.comments = []PRFeedbackComment{comment}
				outcomes[0] = assessedOutcome(comment, "no_change")
			}
			provider := &feedbackScriptProvider{outcomes: outcomes}
			runner := fakeRunner(&fakeProvider{}, c, pub.fakePublisher, &fakeRepository{})
			runner.Provider = provider
			runner.Publisher = pub
			if err := runner.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.State != "ready" || len(provider.sessions) < 2 || provider.sessions[0].ResumeID != "" || c.calls != 1 || j.Plan.PRTitle != a.Title || j.Plan.PRBody != a.Body {
				t.Fatalf("lost fresh adoption/check/metadata contract: state=%s checks=%d sessions=%+v", j.State, c.calls, provider.sessions)
			}
			if withComment && (pub.replies != 1 || !strings.Contains(provider.sessions[0].Prompt, comment.Body)) {
				t.Fatal("feedback was not assessed and replied")
			}
			if j.PRFeedback.Active || j.PRFeedback.CompletedHead != testHead {
				t.Fatal("feedback completion not recorded")
			}
		})
	}
}

func TestAdoptionFetchFailureRetainsResumableCheckpoint(t *testing.T) {
	dir, j := testRun(t)
	a := adoptionBoundary()
	j.Plan.Adoption = &a
	j.Plan.SourceSHA = testHead
	j.Plan.StartingSHA = testHead
	j.State = "adopting"
	j.Publication = Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
	pub := &feedbackPublisherFake{fakePublisher: &fakePublisher{}, readErr: errors.New("feedback unavailable")}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, pub.fakePublisher, &fakeRepository{})
	r.Publisher = pub
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.State != "blocked" || j.ResumeState != "adopting" || j.SessionID != "" || j.PRFeedback != nil {
		t.Fatalf("unrecoverable adoption: %+v", j)
	}
}

func TestAdoptionExportsBaseSignsOnlyCorrectionsAndPreservesPR(t *testing.T) {
	ctx := context.Background()
	root, launch := sourceFixture(t)
	git := func(root string, args ...string) string {
		t.Helper()
		out, err := (GitHubPublisher{Command: localCommand}).git(ctx, root, args...)
		if err != nil {
			t.Fatal(args, err)
		}
		return strings.TrimSpace(out)
	}
	git(root, "checkout", "-b", "TASK-1")
	sourceWrite(t, root, "README.md", "existing PR\n")
	git(root, "add", "README.md")
	git(root, "commit", "-m", "Original PR implementation")
	head := git(root, "rev-parse", "HEAD")
	git(root, "checkout", "main")
	sourceWrite(t, root, "base-only.txt", "new base commit\n")
	git(root, "add", "base-only.txt")
	git(root, "commit", "-m", "Advance base")
	base := git(root, "rev-parse", "HEAD")
	remote := filepath.Join(realTemp(t), "remote.git")
	if _, err := localCommand(ctx, "git", "clone", "--bare", root, remote); err != nil {
		t.Fatal(err)
	}
	directory := realTemp(t)
	key := filepath.Join(realTemp(t), "disposable-key")
	if data, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatal(string(data), err)
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(directory, "allowed-signers")
	if err := os.WriteFile(allowed, []byte("* "+strings.TrimSpace(string(public))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := frozenTestIdentity()
	identity.SSHPublicKey = strings.Join(strings.Fields(string(public))[:2], " ")
	signingCalls, remoteWrites := 0, 0
	replacement := false
	p := GitHubPublisher{Frozen: identity, SigningKeyPath: key, AllowedSignersPath: allowed}
	p.Command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" {
			rewritten := append([]string(nil), args...)
			for i, arg := range rewritten {
				if arg == "https://github.com/example/project.git" {
					rewritten[i] = remote
				}
				if arg == "commit-tree" {
					signingCalls++
				}
				if arg == "push" {
					remoteWrites++
				}
			}
			return localCommand(ctx, name, rewritten...)
		}
		if name != "gh" {
			t.Fatal("unexpected external command", name, args)
		}
		if args[0] == "auth" {
			return nil, nil
		}
		if args[0] == "api" {
			switch args[1] {
			case "user":
				return []byte(`{"id":123,"login":"example"}`), nil
			case "repos/example/project":
				return []byte(`{"id":99,"full_name":"example/project"}`), nil
			case "repos/example/project/branches/main":
				return json.Marshal(map[string]any{"commit": map[string]string{"sha": base}})
			case "repos/example/project/branches/TASK-1":
				return json.Marshal(map[string]any{"commit": map[string]string{"sha": git(remote, "rev-parse", "refs/heads/TASK-1")}})
			}
		}
		current := git(remote, "rev-parse", "refs/heads/TASK-1")
		record := map[string]any{"number": 1, "url": "https://github.com/example/project/pull/1", "state": "OPEN", "isCrossRepository": false, "headRefName": "TASK-1", "baseRefName": "main", "headRefOid": current, "baseRefOid": base, "title": "Preserve this title", "body": "Preserve this description"}
		if args[0] == "pr" && args[1] == "list" {
			return json.Marshal([]any{record})
		}
		if args[0] == "pr" && args[1] == "view" {
			if strings.HasPrefix(args[len(args)-1], "number,url,baseRefOid,headRefOid,state") {
				if args[2] != "1" {
					t.Fatal("confirmed adopted PR by branch rather than frozen number")
				}
				if replacement {
					record["number"] = 2
					record["url"] = "https://github.com/example/project/pull/2"
				}
			}
			return json.Marshal(record)
		}
		t.Fatalf("unexpected external command: %s %v", name, args)
		return nil, errors.New("unexpected command")
	}
	plan := Plan{Repository: "example/project"}
	a, snapshot, err := p.Adopt(ctx, plan, 1, directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Bundle = filepath.Join(directory, "snapshot.bundle")
	workspace := filepath.Join(directory, "workspace")
	plan, err = CaptureAdoption(ctx, launch, snapshot, a, workspace, plan.Repository, DefaultModels().Codex)
	if err != nil {
		t.Fatal(err)
	}
	export := func() {
		t.Helper()
		helper := strings.ReplaceAll(repositoryHelper, "/workspace", workspace)
		helper = strings.ReplaceAll(helper, "/export/source.bundle", filepath.Join(directory, "source.bundle"))
		if data, err := exec.Command("python3", "-I", "-c", helper, "bundle").CombinedOutput(); err != nil {
			t.Fatal(string(data), err)
		}
		p.BundlePath = filepath.Join(directory, "source.bundle")
		p.ExpectedHead = git(workspace, "rev-parse", "HEAD")
		p.ExpectedTree = git(workspace, "rev-parse", "HEAD^{tree}")
	}
	previous := Publication{a.URL, a.Number, a.BaseSHA, a.HeadSHA}
	export()
	unchanged, err := p.Publish(ctx, plan, workspace, directory, previous, nil)
	if err != nil || unchanged != previous || signingCalls != 0 || remoteWrites != 0 {
		t.Fatalf("unchanged PR was modified: %+v %d %d %v", unchanged, signingCalls, remoteWrites, err)
	}
	review, err := PrepareReview(ctx, Journal{Plan: plan, Workspace: workspace, Publication: unchanged}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if git(review, "rev-parse", base+"^{commit}") != base {
		t.Fatal("review lost non-ancestor base")
	}
	sourceWrite(t, workspace, "README.md", "existing PR with supported correction\n")
	git(workspace, "add", "README.md")
	git(workspace, "commit", "-m", "Address PR feedback")
	originalCorrection := git(workspace, "rev-parse", "HEAD")
	export()
	replacement = true
	if _, err := p.Publish(ctx, plan, workspace, directory, previous, nil); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal("accepted replacement PR", err)
	}
	replacement = false
	published, err := p.Publish(ctx, plan, workspace, directory, previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	if published.Number != 1 || published.BaseSHA != base || published.HeadSHA == head || published.HeadSHA == originalCorrection || signingCalls != 1 || remoteWrites != 1 {
		t.Fatalf("wrong correction publication: %+v signing=%d pushes=%d", published, signingCalls, remoteWrites)
	}
	if git(filepath.Join(directory, "publication.git"), "show", "-s", "--format=%P", published.HeadSHA) != head {
		t.Fatal("rewrote original PR history")
	}
	if git(remote, "rev-parse", "refs/heads/TASK-1") != published.HeadSHA {
		t.Fatal("remote correction not confirmed")
	}
	review, err = PrepareReview(ctx, Journal{Plan: plan, Workspace: workspace, Publication: published}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if git(review, "rev-parse", base+"^{commit}") != base || git(review, "rev-parse", "HEAD") != published.HeadSHA {
		t.Fatal("review did not retain frozen base and signed head")
	}
}
