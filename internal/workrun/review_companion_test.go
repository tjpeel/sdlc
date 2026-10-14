package workrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewCompanionRequest() CompanionRepositoryRequest {
	return CompanionRepositoryRequest{ID: "read-contract", Repository: "example/contract", Purpose: "inspect linked read API"}
}

func companionReviewFixture(t *testing.T) (string, *Journal) {
	t.Helper()
	directory, j := testRun(t)
	git := func(root string, args ...string) string {
		t.Helper()
		value, err := isolatedGit(context.Background(), root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(value)
	}
	publication := filepath.Join(directory, "publication.git")
	git(directory, "init", "--bare", "--template=", publication)
	tree := git(publication, "mktree")
	head := git(publication, "-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit-tree", tree, "-m", "Create review fixture")
	git(publication, "update-ref", "refs/heads/publication", head)
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, BaseSHA: head, HeadSHA: head}
	j.State = "awaiting_reviewer"
	j.SessionID = testNative
	j.Attempt, j.Rounds, j.CheckAttempt = 5, 3, 4
	j.Evidence = CheckEvidence{Head: head, Tree: tree, Passed: true}
	j.CIRepairBudget = &CIRepairBudget{Used: 1, StartRound: 1, LastGrantedHead: head}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 12, Used: 1, StartRound: 1, LastGrantedHead: head}
	return directory, j
}

func createFrozenReviewCompanion(t *testing.T, workspace string, request CompanionRepositoryRequest) CompanionRepository {
	t.Helper()
	relative, _ := companionRepositoryPath(request.Repository)
	root := filepath.Join(workspace, filepath.FromSlash(relative))
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		result, err := isolatedGit(context.Background(), root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(result)
	}
	git("init", "--template=", "-q")
	file := filepath.Join(root, "contract.txt")
	if err := os.WriteFile(file, []byte("Read returns the current contract.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "contract.txt")
	git("-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-qm", "Create read contract")
	if err := os.WriteFile(file, []byte("Read returns the current contract and its identifier.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "contract.txt")
	git("-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-qm", "Record contract identifier")
	git("remote", "add", "origin", "https://github.com/"+request.Repository+".git")
	return CompanionRepository{Request: request, Path: relative, Revision: git("rev-parse", "HEAD")}
}

type reviewSourceCloner struct {
	t        *testing.T
	requests []string
	err      error
	after    func()
}

func (cloner *reviewSourceCloner) CloneCompanionRepository(_ context.Context, _ Plan, workspace, repository string) (string, error) {
	cloner.requests = append(cloner.requests, repository)
	if cloner.err != nil {
		return "", cloner.err
	}
	request := reviewCompanionRequest()
	request.Repository = repository
	source := createFrozenReviewCompanion(cloner.t, workspace, request)
	if cloner.after != nil {
		cloner.after()
	}
	return source.Revision, nil
}

func TestReviewerSourceOnlyHandoffAdmitsNoExecutableOrIncompleteWork(t *testing.T) {
	requested := testOutcome("checks_requested")
	requested.RepositoryRequests = []CompanionRepositoryRequest{reviewCompanionRequest()}
	if err := validateOutcome(requested, "review"); err != nil {
		t.Fatal("source-only review handoff rejected", err)
	}
	for _, kind := range []string{"empty", "verification", "diagnostic", "findings", "questions", "limitations", "diagnostics status"} {
		t.Run(kind, func(t *testing.T) {
			copy := requested
			switch kind {
			case "empty":
				copy.RepositoryRequests = nil
			case "verification":
				copy.VerificationRequests = []VerificationRequest{regressionRequest()}
			case "diagnostic":
				copy.DiagnosticRequests = []VerificationRequest{diagnosticRequest("probe")}
			case "findings":
				copy.Findings = reviewedFinding("P1").Findings
			case "questions":
				copy.Questions = []string{"Which contract applies?"}
			case "limitations":
				copy.Limitations = []string{"Contract unavailable"}
			case "diagnostics status":
				copy.Status = "diagnostics_requested"
			}
			if validateOutcome(copy, "review") == nil {
				t.Fatal("non-source-only review handoff accepted")
			}
		})
	}
}

func TestPrepareReviewIncludesFrozenCompanionSourceAndIndependentHistory(t *testing.T) {
	directory, j := companionReviewFixture(t)
	source := createFrozenReviewCompanion(t, j.Workspace, reviewCompanionRequest())
	j.CompanionRepositories = []CompanionRepository{source}
	original := filepath.Join(j.Workspace, filepath.FromSlash(source.Path))
	if err := os.WriteFile(filepath.Join(original, ".git", "ignored-implementation-transcript"), []byte("private implementation context"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := PrepareReview(context.Background(), *j, directory)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(workspace, filepath.FromSlash(source.Path))
	content, err := os.ReadFile(filepath.Join(copied, "contract.txt"))
	if err != nil || !strings.Contains(string(content), "identifier") {
		t.Fatal("review snapshot omitted source", err)
	}
	if _, err := os.Stat(filepath.Join(copied, ".git", "ignored-implementation-transcript")); !os.IsNotExist(err) {
		t.Fatal("implementation context entered review snapshot")
	}
	before, _ := os.Stat(filepath.Join(original, "contract.txt"))
	after, _ := os.Stat(filepath.Join(copied, "contract.txt"))
	if os.SameFile(before, after) {
		t.Fatal("review source shares mutable files")
	}
	if err := os.Rename(original, original+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	history, err := isolatedGit(context.Background(), copied, "log", "--format=%s")
	if err != nil || !strings.Contains(history, "Create read contract") || !strings.Contains(history, "Record contract identifier") {
		t.Fatal("review history depends on implementation source", err, history)
	}
	status, err := isolatedGit(context.Background(), workspace, "status", "--porcelain")
	if err != nil || status != "" {
		t.Fatal("companion source polluted published candidate", err, status)
	}
	prompt := (Runner{}).prompt(*j, "review")
	if !strings.Contains(prompt, "companion_sources") || !strings.Contains(prompt, source.Revision) || !strings.Contains(prompt, source.Path) {
		t.Fatal("review source inventory missing")
	}
}

func TestReviewSourceHandoffStagesThenStartsFreshReviewWithoutRepair(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "new source", true: "known source"}[known], func(t *testing.T) {
			directory, j := companionReviewFixture(t)
			if known {
				j.CompanionRepositories = []CompanionRepository{createFrozenReviewCompanion(t, j.Workspace, reviewCompanionRequest())}
			}
			requested := testOutcome("checks_requested")
			requested.RepositoryRequests = []CompanionRepositoryRequest{reviewCompanionRequest()}
			provider := &fakeProvider{outcomes: []Outcome{requested, testOutcome("reviewed")}}
			cloner := &reviewSourceCloner{t: t}
			checker, publisher := &fakeChecker{}, &fakePublisher{}
			runner := fakeRunner(provider, checker, publisher, &fakeRepository{})
			runner.CompanionCloner = cloner
			var snapshots []string
			runner.ReviewWorkspace = func(ctx context.Context, journal Journal, root string) (string, error) {
				snapshot, err := PrepareReview(ctx, journal, root)
				snapshots = append(snapshots, snapshot)
				return snapshot, err
			}
			if err := runner.Run(context.Background(), directory, j, ""); err != nil {
				t.Fatal(err)
			}
			wantClones := 1
			if known {
				wantClones = 0
			}
			if len(cloner.requests) != wantClones || len(provider.calls) != 2 || checker.calls != 0 || publisher.calls != 0 || j.State != "ready" {
				t.Fatal("source handoff dispatched implementation or publication")
			}
			for _, call := range provider.calls {
				if call.Role != "review" || call.ResumeID != "" {
					t.Fatal("review did not start fresh", call.Role, call.ResumeID)
				}
			}
			if j.SessionID != testNative || j.Attempt != 7 || j.Rounds != 3 || j.CheckAttempt != 4 || j.CIRepairBudget.Used != 1 || j.ReviewRepairBudget.Used != 1 {
				t.Fatal("source handoff spent repair allowance or reset history")
			}
			if len(snapshots) != 2 || snapshots[0] == snapshots[1] {
				t.Fatal("source handoff reused review snapshot")
			}
			path := filepath.Join(snapshots[1], filepath.FromSlash(j.CompanionRepositories[0].Path), "contract.txt")
			if _, err := os.ReadFile(path); err != nil {
				t.Fatal("fresh reviewer still lacked requested source", err)
			}
			loaded, err := Load(directory)
			if err != nil || len(loaded.CompanionRepositories) != 1 || loaded.CompanionRepositories[0].Revision != j.CompanionRepositories[0].Revision {
				t.Fatal("frozen source revision not persisted", err)
			}
		})
	}
}

func TestPrepareReviewRejectsChangedOrUnsafeCompanion(t *testing.T) {
	for _, kind := range []string{"missing", "revision", "content", "configuration", "source link", "metadata link", "object redirect", "copy bound", "parent link", "owner"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := companionReviewFixture(t)
			source := createFrozenReviewCompanion(t, j.Workspace, reviewCompanionRequest())
			j.CompanionRepositories = []CompanionRepository{source}
			root := filepath.Join(j.Workspace, filepath.FromSlash(source.Path))
			switch kind {
			case "missing":
				os.Rename(root, root+"-missing")
			case "revision":
				j.CompanionRepositories[0].Revision = testHead
			case "content":
				os.WriteFile(filepath.Join(root, "contract.txt"), []byte("changed"), 0644)
			case "configuration":
				isolatedGit(context.Background(), root, "config", "core.hooksPath", "/unsupported-hooks")
			case "source link":
				os.Symlink(j.Workspace, filepath.Join(root, "escaping-link"))
			case "metadata link":
				os.Symlink(j.Workspace, filepath.Join(root, ".git", "refs", "heads", "escaping-link"))
			case "object redirect":
				if err := os.WriteFile(filepath.Join(root, ".git", "objects", "info", "alternates"), []byte(j.Workspace), 0644); err != nil {
					t.Fatal(err)
				}
			case "copy bound":
				file, err := os.Create(filepath.Join(root, ".git", "oversized-source"))
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maximumReviewCompanionBytes + 1); err != nil {
					t.Fatal(err)
				}
				file.Close()
			case "parent link":
				os.Rename(root, root+"-outside")
				os.Symlink(root+"-outside", root)
			case "owner":
				j.CompanionRepositories[0].Request.Repository = "other-owner/contract"
			}
			if _, err := PrepareReview(context.Background(), *j, directory); err == nil {
				t.Fatal("changed or unsafe frozen source accepted")
			}
		})
	}
}

func TestReviewSourceAccessQuestionIsOwnedByReview(t *testing.T) {
	directory, j := companionReviewFixture(t)
	requested := testOutcome("checks_requested")
	requested.RepositoryRequests = []CompanionRepositoryRequest{reviewCompanionRequest()}
	provider := &fakeProvider{outcomes: []Outcome{requested}}
	cloner := &reviewSourceCloner{t: t, err: &companionSourceAccessError{repository: reviewCompanionRequest().Repository}}
	runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	runner.CompanionCloner = cloner
	runner.ReviewWorkspace = PrepareReview
	if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, ErrStopped) || j.State != "waiting_for_human" || j.PendingRole != "review" || len(j.Outcome.Questions) != 1 {
		t.Fatal("source access failure lost review-owned question", err)
	}
	cloner.err = nil
	provider.outcomes = []Outcome{requested, testOutcome("reviewed")}
	if err := runner.Run(context.Background(), directory, j, "Read access is available."); err != nil {
		t.Fatal(err)
	}
	if j.ReviewHumanInput == nil || j.ReviewHumanInput.Answer != "Read access is available." || j.SessionID != testNative || j.Rounds != 3 {
		t.Fatal("answer escaped review ownership or reset original session")
	}
	for _, call := range provider.calls {
		if call.Role != "review" || call.ResumeID != "" {
			t.Fatal("access recovery dispatched implementation")
		}
	}
}

func TestReviewSourceHandoffStopsOnCIBoundaryDrift(t *testing.T) {
	for _, phase := range []string{"before staging", "after staging"} {
		t.Run(phase, func(t *testing.T) {
			directory, j := companionReviewFixture(t)
			requested := testOutcome("checks_requested")
			requested.RepositoryRequests = []CompanionRepositoryRequest{reviewCompanionRequest()}
			provider := &fakeProvider{outcomes: []Outcome{requested}}
			publisher := &fakePublisher{statuses: []string{"passed", "failed"}}
			cloner := &reviewSourceCloner{t: t}
			if phase == "after staging" {
				publisher.statuses = nil
				cloner.after = func() { publisher.ciErr = errors.New("published head changed") }
			}
			runner := fakeRunner(provider, &fakeChecker{}, publisher, &fakeRepository{})
			runner.CompanionCloner = cloner
			runner.ReviewWorkspace = PrepareReview
			if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" || !strings.Contains(j.StopReason, "PR or CI changed") {
				t.Fatal("CI drift allowed a new reviewer", err)
			}
			if len(provider.calls) != 1 || j.SessionID != testNative || j.Rounds != 3 || j.CIRepairBudget.Used != 1 || j.ReviewRepairBudget.Used != 1 {
				t.Fatal("CI drift consumed a source repair grant")
			}
			if phase == "before staging" && len(cloner.requests) != 0 {
				t.Fatal("source cloned after boundary failed")
			}
		})
	}
}

func TestReviewSourceInvalidOrStoppedHandoffNeverStagesOrSpendsRepair(t *testing.T) {
	for _, kind := range []string{"cross owner", "incomplete", "blocked", "failed"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := companionReviewFixture(t)
			requested := testOutcome("checks_requested")
			requested.RepositoryRequests = []CompanionRepositoryRequest{reviewCompanionRequest()}
			switch kind {
			case "cross owner":
				requested.RepositoryRequests[0].Repository = "other-owner/contract"
			case "incomplete":
				requested.Limitations = []string{"Read contract still unresolved"}
			default:
				requested.Status = kind
			}
			provider := &fakeProvider{outcomes: []Outcome{requested}}
			cloner := &reviewSourceCloner{t: t}
			runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			runner.CompanionCloner = cloner
			runner.ReviewWorkspace = PrepareReview
			if err := runner.Run(context.Background(), directory, j, ""); !errors.Is(err, ErrStopped) || j.State != "blocked" {
				t.Fatal("invalid review source handoff proceeded", err)
			}
			if len(cloner.requests) != 0 || len(provider.calls) != 1 || j.Rounds != 3 || j.ReviewRepairBudget.Used != 1 || j.CIRepairBudget.Used != 1 || j.SessionID != testNative {
				t.Fatal("stopped handoff staged source or spent repair grants")
			}
		})
	}
}
