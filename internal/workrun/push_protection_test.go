package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakePushDiagnostic(branch, signed, path string, line int) []byte {
	return []byte(fmt.Sprintf("remote: error: GH013: Repository rule violations found for refs/heads/%s.\nremote: - GITHUB PUSH PROTECTION\nremote:     - Push cannot contain secrets\nremote:       —— Disposable test type —————————\nremote:        locations:\nremote:          - commit: %s\nremote:            path: %s:%d\nremote:        (?) To push, remove secret from commit(s) or follow this URL to allow the secret.\n", branch, signed, path, line))
}

func TestPushRepairCeilingAllowsOnlyExplicitNumericOverride(t *testing.T) {
	dir, j := testRun(t)
	os.Mkdir(filepath.Join(dir, "publisher"), 0700)
	os.WriteFile(filepath.Join(dir, "source.bundle"), []byte("offline disposable bundle"), 0600)
	identity := PublicationIdentity{}
	j.Plan.PublicationIdentity = &identity
	j.SessionID = testNative
	j.Attempt, j.Rounds, j.CheckAttempt = 4, 2, 3
	runner := Runner{}
	for n := 4; n <= 7; n++ {
		head := strings.Repeat(fmt.Sprint(n), 40)
		j.State = "publishing"
		j.Evidence = CheckEvidence{Passed: true, Head: head, Tree: testTree}
		r := &PushProtectionRejection{Identity: identity, Cutoff: testBase, Source: testBase, Head: head, Tree: testTree, SignedHead: testSigned, Repository: j.Plan.Repository, Branch: j.Plan.Branch, Locations: []PushProtectionLocation{{Commit: head, Path: "config.txt", Line: 2, Blob: testTree}}}
		err := runner.grantPushRepair(dir, j, r)
		if n < 7 && err != nil {
			t.Fatal(err)
		}
		if n == 7 {
			if !errors.Is(err, ErrStopped) || j.StopReason != pushProtectionRepairBound || len(j.PushRepair.Candidates) != 3 {
				t.Fatal("default repair ceiling not retained", err)
			}
			if kind, limit, err := admittedRunLimit(*j); err != nil || kind != "push_repairs" || limit != 3 {
				t.Fatal("numeric ceiling not admitted", kind, limit, err)
			}
			if j.SessionID != testNative || j.Attempt != 4 || j.Rounds != 2 || j.CheckAttempt != 3 {
				t.Fatal("numeric hold changed original accounting")
			}
			for _, reason := range []string{"this rejected candidate already received a push protection repair grant", "rejected audit bundle changed", "published source prefix changed during push repair", "check input changed", "repair history exceeds content bound"} {
				stopped := *j
				stopped.StopReason = reason
				if _, _, err := admittedRunLimit(stopped); err == nil {
					t.Fatal("operational hold admitted numeric override", reason)
				}
			}

			j.RunLimitOverride = &RunLimitOverride{GrantedAt: time.Now().UTC(), CheckpointUpdatedAt: j.UpdatedAt, GrantedAttempt: j.Attempt, GrantedRounds: j.Rounds, OriginalSessionID: testNative, LimitKind: "push_repairs", OriginalLimit: 3}
			j.State = "publishing"
			if err := runner.grantPushRepair(dir, j, r); err != nil || len(j.PushRepair.Candidates) != 4 {
				t.Fatal("explicit numeric override did not retain safety gates", err)
			}
		}
	}
}

func TestPushReceiptRequiresCleanFinalBoundaryAndSurvivesInterruptedGrant(t *testing.T) {
	dir, j := testRun(t)
	os.Mkdir(filepath.Join(dir, "publisher"), 0700)
	identity := *frozenTestIdentity()
	j.Plan.PublicationIdentity = &identity
	r := &PushProtectionRejection{Identity: identity, Cutoff: testBase, Source: testBase, Head: testHead, Tree: testTree, SignedHead: testSigned, Repository: j.Plan.Repository, Branch: j.Plan.Branch, Locations: []PushProtectionLocation{{Commit: testHead, Path: "config.txt", Line: 2, Blob: testTree}}}
	raw := fakePushDiagnostic(j.Plan.Branch, testSigned, "config.txt", 2)
	hash := sha256.Sum256(raw)
	if err := retainPublicationDiagnostic(dir, PublicationDiagnostic{Stage: "command", Code: "command_failed", ExitCode: 1, Path: "failure.stderr", SHA256: hex.EncodeToString(hash[:]), Bytes: len(raw)}, raw); err != nil {
		t.Fatal(err)
	}
	if err := retainProvisionalPushReceipt(dir, r, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPushReceipt(dir, j.Plan, Publication{}, testHead, testTree); err == nil {
		t.Fatal("unpromoted rejection resumed publication")
	}
	for name, err := range map[string]error{"cleanup": errors.Join(r, errors.New("container cleanup failed")), "transport": errors.Join(r, errors.New("output corrupted"))} {
		t.Run(name, func(t *testing.T) {
			_, got := finishPushAuthority(context.Background(), dir, PublisherResponse{}, err)
			var leaked *PushProtectionRejection
			if errors.As(got, &leaked) {
				t.Fatal("cleanup retained repair authority")
			}
			if _, err := os.Stat(filepath.Join(dir, "publisher", "push-rejection.json")); !os.IsNotExist(err) {
				t.Fatal("unsafe receipt persisted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := finishPushAuthority(ctx, dir, PublisherResponse{}, r); err == r {
		t.Fatal("cancelled operation retained authority")
	}
	if _, err := finishPushAuthority(context.Background(), dir, PublisherResponse{}, r); err != r {
		t.Fatal(err)
	}
	loaded, err := loadPushReceipt(dir, j.Plan, Publication{}, testHead, testTree)
	if err != nil || loaded == nil || loaded.Head != r.Head {
		t.Fatal("interrupted grant lost typed receipt", err)
	}
	newHead := strings.Repeat("4", 40)
	if loaded, err := loadPushReceipt(dir, j.Plan, Publication{}, newHead, testTree, r); err != nil || loaded.Head != r.Head {
		t.Fatal("already granted old receipt rejected corrected candidate", err)
	}
	newRejection := *r
	newRejection.Head = newHead
	if err := retainPushReceipt(dir, &newRejection); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadPushReceipt(dir, j.Plan, Publication{}, newHead, testTree, r); err != nil || loaded.Head != newHead {
		t.Fatal("second rejection receipt cannot resume before grant", err)
	}
	if err := retainPushReceipt(dir, r); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPushReceipt(dir, j.Plan, Publication{}, testBase, testTree); err == nil {
		t.Fatal("stale receipt accepted")
	}
	if _, err := loadPushReceipt(dir, j.Plan, Publication{Number: 1, HeadSHA: testSigned}, testHead, testTree); err == nil {
		t.Fatal("changed publication context accepted")
	}
	os.Chmod(filepath.Join(dir, "publisher", "push-rejection.json"), 0644)
	if _, err := loadPushReceipt(dir, j.Plan, Publication{}, testHead, testTree); err == nil {
		t.Fatal("public receipt accepted")
	}
}

type pushLifecycleRepository struct{ head string }

func (*pushLifecycleRepository) FormatterPreparation(context.Context, *Journal, Revision) ([]VerificationRequest, error) {
	return nil, nil
}

func (r *pushLifecycleRepository) Inspect(context.Context, string) (Revision, error) {
	return Revision{Head: r.head, Tree: testTree, Clean: true}, nil
}
func (r *pushLifecycleRepository) Bundle(_ context.Context, _ string, path string) error {
	return os.WriteFile(path, []byte("offline disposable bundle"), 0600)
}

type pushLifecycleProvider struct {
	fakeProvider
	repository    *pushLifecycleRepository
	repaired      bool
	historyRepair bool
}

func (p *pushLifecycleProvider) Execute(ctx context.Context, s Session, out, diag io.Writer) (SessionResult, error) {
	if s.Role == "implementation" && strings.Contains(s.Prompt, "GitHub push protection rejected the exact candidate") && !p.repaired {
		p.repository.head = strings.Repeat("4", 40)
		p.repaired = true
		p.lastImplementation = Outcome{}
	}
	if s.Role == "implementation" && strings.Contains(s.Prompt, "Historical cleanup is incomplete") && !p.historyRepair {
		p.repository.head = strings.Repeat("5", 40)
		p.historyRepair = true
		p.lastImplementation = Outcome{}
	}
	return p.fakeProvider.Execute(ctx, s, out, diag)
}

type pushLifecyclePublisher struct {
	fakePublisher
	rejection *PushProtectionRejection
	directory string
	semantic  bool
	pushes    int
}

func (p *pushLifecyclePublisher) Publish(ctx context.Context, plan Plan, workspace, dir string, previous Publication, out io.Writer) (Publication, error) {
	if p.calls == 0 {
		p.pushes++
		p.calls++
		os.Mkdir(filepath.Join(dir, "publisher"), 0700)
		return Publication{}, p.rejection
	}
	if p.calls == 1 && p.semantic {
		p.calls++
		candidate := *p.rejection
		candidate.Kind = "historical_cleanup_incomplete"
		candidate.Head = strings.Repeat("4", 40)
		candidate.Known = p.rejection.Locations
		candidate.Locations = []PushProtectionLocation{{Commit: candidate.Head, Path: "config.txt", Line: 2, Blob: testTree}}
		return Publication{}, &HistoricalCleanupIncomplete{OriginalHead: p.rejection.Head, Candidate: candidate}
	}
	p.pushes++
	return p.fakePublisher.Publish(ctx, plan, workspace, dir, previous, out)
}

func TestPushRepairRunnerSendsKnownHistoricalFailureBackWithoutPush(t *testing.T) {
	dir, j := testRun(t)
	identity := *frozenTestIdentity()
	j.Plan.PublicationIdentity = &identity
	repo := &pushLifecycleRepository{head: testHead}
	provider := &pushLifecycleProvider{fakeProvider: fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("implemented"), testOutcome("implemented"), testOutcome("reviewed")}}, repository: repo}
	rejection := &PushProtectionRejection{Identity: identity, Cutoff: testBase, Source: testBase, Head: testHead, Tree: testTree, SignedHead: testSigned, Repository: j.Plan.Repository, Branch: j.Plan.Branch, Locations: []PushProtectionLocation{{Commit: testHead, Path: "config.txt", Line: 2, Blob: testTree}}}
	pub := &pushLifecyclePublisher{rejection: rejection, semantic: true}
	checks := &fakeChecker{}
	runner := fakeRunner(&provider.fakeProvider, checks, &pub.fakePublisher, &fakeRepository{})
	runner.Provider, runner.Publisher, runner.Repository = provider, pub, repo
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls != 3 || pub.calls != 3 || pub.pushes != 2 || len(j.PushRepair.Candidates) != 2 || len(provider.calls) != 4 || provider.calls[1].ResumeID != testNative || provider.calls[2].ResumeID != testNative || provider.calls[3].Role != "review" || !provider.historyRepair {
		t.Fatalf("historical correction skipped same-session pipeline: state=%s checks=%d attempts=%d pushes=%d", j.State, checks.calls, pub.calls, pub.pushes)
	}
}
func TestPushRepairRunnerRepeatsChecksAndNormalCIReviewInSameSession(t *testing.T) {
	dir, j := testRun(t)
	identity := *frozenTestIdentity()
	j.Plan.PublicationIdentity = &identity
	os.Mkdir(filepath.Join(j.Workspace, ".git"), 0700)
	repo := &pushLifecycleRepository{head: testHead}
	provider := &pushLifecycleProvider{fakeProvider: fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("implemented"), testOutcome("reviewed")}}, repository: repo}
	rejection := &PushProtectionRejection{Identity: identity, Cutoff: testBase, Source: testBase, Head: testHead, Tree: testTree, SignedHead: testSigned, Repository: j.Plan.Repository, Branch: j.Plan.Branch, Locations: []PushProtectionLocation{{Commit: testHead, Path: "config.txt", Line: 2, Blob: testTree}}}
	pub := &pushLifecyclePublisher{rejection: rejection}
	checks := &fakeChecker{}
	runner := fakeRunner(&provider.fakeProvider, checks, &pub.fakePublisher, &fakeRepository{})
	runner.Provider, runner.Publisher, runner.Repository = provider, pub, repo
	if err := runner.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls != 2 || pub.calls != 2 || pub.ci == 0 || j.PushRepair.Pending != nil || len(provider.calls) != 3 || provider.calls[1].ResumeID != testNative || provider.calls[2].Role != "review" || j.SessionID != testNative {
		t.Fatalf("repair skipped pipeline or changed session: state=%s checks=%d pub=%d ci=%d calls=%+v", j.State, checks.calls, pub.calls, pub.ci, provider.calls)
	}
}
func TestPushProtectionParserFailsClosed(t *testing.T) {
	valid := fakePushDiagnostic("TASK-1", testHead, "config.txt", 2)
	if locations, err := parsePushProtection(valid, "TASK-1"); err != nil || len(locations) != 1 || locations[0].Line != 2 {
		t.Fatal(locations, err)
	}
	for name, raw := range map[string][]byte{
		"wrong ref":           fakePushDiagnostic("other", testHead, "config.txt", 2),
		"wrong rule":          []byte(strings.ReplaceAll(string(valid), "GITHUB PUSH PROTECTION", "ANOTHER RULE")),
		"extra rule":          append(append([]byte(nil), valid...), []byte("remote: - ANOTHER RULE\n")...),
		"transport":           append(append([]byte(nil), valid...), []byte("fatal: transport disconnected\n")...),
		"zero line":           fakePushDiagnostic("TASK-1", testHead, "config.txt", 0),
		"overflow line":       []byte(strings.ReplaceAll(string(valid), ":2\n", ":99999999999999999999999999\n")),
		"traversal":           fakePushDiagnostic("TASK-1", testHead, "../outside", 1),
		"missing path":        []byte(strings.ReplaceAll(string(valid), "remote:            path: config.txt:2\n", "")),
		"duplicate commit":    []byte(strings.ReplaceAll(string(valid), "remote:            path:", "remote:          - commit: "+testHead+"\nremote:            path:")),
		"overflow diagnostic": make([]byte, MaximumPublicationDiagnostic+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePushProtection(raw, "TASK-1"); err == nil {
				t.Fatal("unsafe diagnostic accepted")
			}
		})
	}
}

func TestPushProtectionParserAcceptsExactExemptionDiagnosticVariant(t *testing.T) {
	raw := strings.ReplaceAll(string(fakePushDiagnostic("TASK-1", testHead, "config.txt", 2)), "to allow the secret.", "to request an exemption.")
	locations, err := parsePushProtection([]byte(raw), "TASK-1")
	if err != nil || len(locations) != 1 || locations[0].Commit != testHead || locations[0].Path != "config.txt" || locations[0].Line != 2 {
		t.Fatal("exact supported rejection wording refused", locations, err)
	}
	for _, ending := range []string{"to request a custom exemption.", "to request an exemption", "to request an exemption. arbitrary text"} {
		if _, err := parsePushProtection([]byte(strings.ReplaceAll(raw, "to request an exemption.", ending)), "TASK-1"); err == nil {
			t.Fatal("unsupported diagnostic wording accepted", ending)
		}
	}
}

func TestPushProtectionParserAcceptsOnlyASCIISpaceTransportPadding(t *testing.T) {
	first := string(fakePushDiagnostic("TASK-1", testHead, "config.txt", 2))
	first = strings.Replace(first, "remote:        (?) To push,", "remote:          - commit: "+testSigned+"\nremote:            path: other.txt:3\nremote:        (?) To push,", 1)
	second := string(fakePushDiagnostic("TASK-1", testBase, "third.txt", 4))
	second = "remote:       ——" + strings.SplitN(second, "remote:       ——", 2)[1]
	second = strings.ReplaceAll(second, "to allow the secret.", "to request an exemption.")
	plain := first + second + "error: failed to push some refs to 'https://github.com/example/project.git'\n"
	pad := func(suffix string) string {
		lines := strings.Split(plain, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "remote:") {
				lines[i] += suffix
			}
		}
		return strings.Join(lines, "\n")
	}
	locations, err := parsePushProtection([]byte(pad("        ")), "TASK-1")
	if err != nil || len(locations) != 3 || locations[0].Commit != testHead || locations[0].Path != "config.txt" || locations[0].Line != 2 || locations[1].Commit != testSigned || locations[1].Path != "other.txt" || locations[1].Line != 3 || locations[2].Commit != testBase || locations[2].Path != "third.txt" || locations[2].Line != 4 {
		t.Fatal("transport padding changed complete locations", locations, err)
	}
	for name, raw := range map[string]string{
		"tab padding":           pad("\t"),
		"control padding":       pad("\x01"),
		"unicode padding":       pad("\u00a0"),
		"internal header space": strings.Replace(pad("        "), "remote: error: GH013:", "remote: error:  GH013:", 1),
		"nonremote location":    strings.Replace(pad("        "), "remote:            path: other.txt:3", "            path: other.txt:3", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePushProtection([]byte(raw), "TASK-1"); err == nil {
				t.Fatal("unsupported transport grammar accepted")
			}
		})
	}
}
func TestPushRejectionRequiresExactTrustedOperationAndOwnCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := GitHubPublisher{Command: localCommand, Frozen: &PublicationIdentity{}}
	git := func(args ...string) string {
		t.Helper()
		out, err := p.git(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init")
	git("config", "user.name", "Offline Test")
	git("config", "user.email", "offline@example.invalid")
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("base\n"), 0600)
	git("add", ".")
	git("commit", "-m", "Base")
	base := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("base\ndisposable fake sensitive line\n"), 0600)
	git("add", ".")
	git("commit", "-m", "Implementation")
	original := git("rev-parse", "HEAD")
	plan := Plan{SourceSHA: base, Repository: "example/project", Branch: "TASK-1"}
	p.ExpectedTree = git("rev-parse", "HEAD^{tree}")
	args := p.gitArgs(dir, "push", "--porcelain", "--force-with-lease=refs/heads/TASK-1:", "https://github.com/example/project.git", testHead+":refs/heads/TASK-1")
	failure := &PublisherCommandFailure{Name: "git", Args: args, Exit: 1, Stderr: fakePushDiagnostic("TASK-1", testHead, "config.txt", 2)}
	classify := func(err error, previous Publication, mapping map[string]string) error {
		return p.rejectedPush(ctx, plan, previous, dir, original, testHead, "", mapping, []string{original}, args, err)
	}
	got := classify(failure, Publication{}, map[string]string{original: testHead})
	r, ok := got.(*PushProtectionRejection)
	if !ok || r.Locations[0].Commit != original || r.Locations[0].Blob == "" {
		t.Fatal("missing verified source mapping", got)
	}
	for name, err := range map[string]error{"plain marker": errors.New(string(failure.Stderr)), "wrong command": &PublisherCommandFailure{Name: "gh", Args: args, Exit: 1, Stderr: failure.Stderr}, "wrong argv": &PublisherCommandFailure{Name: "git", Args: []string{"push"}, Exit: 1, Stderr: failure.Stderr}, "success": &PublisherCommandFailure{Name: "git", Args: args, Exit: 0, Stderr: failure.Stderr}} {
		t.Run(name, func(t *testing.T) {
			if _, ok := classify(err, Publication{}, map[string]string{original: testHead}).(*PushProtectionRejection); ok {
				t.Fatal("untrusted failure promoted")
			}
		})
	}
	if _, ok := classify(failure, Publication{}, map[string]string{}).(*PushProtectionRejection); ok {
		t.Fatal("unmapped inherited detection admitted")
	}
	if _, ok := classify(failure, Publication{Number: 1, HeadSHA: testHead}, map[string]string{original: testHead}).(*PushProtectionRejection); ok {
		t.Fatal("published history admitted")
	}
	if _, ok := classify(failure, Publication{Number: 1, HeadSHA: testBase}, map[string]string{original: testHead}).(*PushProtectionRejection); ok {
		t.Fatal("unproved published boundary admitted")
	}
	// A normal update may repair only source commits after the verified signed
	// head currently owned by the published PR.
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("base\nreplacement\ndisposable new fixture\n"), 0600)
	git("add", ".")
	git("commit", "-m", "Unpublished update")
	update := git("rev-parse", "HEAD")
	previous := Publication{Number: 1, HeadSHA: testHead}
	updateArgs := p.gitArgs(dir, "push", "--porcelain", "--force-with-lease=refs/heads/TASK-1:"+testHead, "https://github.com/example/project.git", testSigned+":refs/heads/TASK-1")
	updateFailure := &PublisherCommandFailure{Name: "git", Args: updateArgs, Exit: 1, Stderr: fakePushDiagnostic("TASK-1", testSigned, "config.txt", 3)}
	updateMapping := map[string]string{original: testHead, update: testSigned}
	got = p.rejectedPush(ctx, plan, previous, dir, update, testSigned, testHead, updateMapping, []string{original, update}, updateArgs, updateFailure)
	if rejected, ok := got.(*PushProtectionRejection); !ok || rejected.Cutoff != original || rejected.Locations[0].Commit != update || rejected.Previous != previous || rejected.RemoteHead != testHead {
		t.Fatal("verified unpublished update refused", got)
	}
	updateFailure.Stderr = fakePushDiagnostic("TASK-1", testHead, "config.txt", 2)
	if _, ok := p.rejectedPush(ctx, plan, previous, dir, update, testSigned, testHead, updateMapping, []string{original, update}, updateArgs, updateFailure).(*PushProtectionRejection); ok {
		t.Fatal("published prefix detection admitted during update")
	}
}

func TestPushRepairRequiresHistoricalCleanupWithIdenticalFinalTree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	identity := PublicationIdentity{}
	p := GitHubPublisher{Command: localCommand, Frozen: &identity}
	git := func(args ...string) string {
		t.Helper()
		out, err := p.git(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(text, message string) string {
		t.Helper()
		os.WriteFile(filepath.Join(dir, "config.txt"), []byte(text), 0600)
		git("add", ".")
		git("commit", "-m", message)
		return git("rev-parse", "HEAD")
	}
	git("init")
	git("config", "user.name", "Offline Test")
	git("config", "user.email", "offline@example.invalid")
	base := write("base\n", "Base")
	dirty := write("base\ndisposable fake sensitive line\n", "Temporary fixture")
	blob := git("rev-parse", dirty+":config.txt")
	removed := write("base\n", "Remove fixture at HEAD")
	tree := git("rev-parse", "HEAD^{tree}")
	rejectedBundle := filepath.Join(t.TempDir(), "rejected.bundle")
	git("bundle", "create", rejectedBundle, "HEAD")
	plan := Plan{SourceSHA: base, Repository: "example/project", Branch: "TASK-1"}
	p.PushRepair = &PushProtectionRepair{Pending: &PushProtectionRejection{Identity: identity, Cutoff: base, Source: base, Head: removed, Repository: plan.Repository, Branch: plan.Branch, Locations: []PushProtectionLocation{{Commit: dirty, Path: "config.txt", Line: 2, Blob: blob}}}}
	if err := p.validatePushRepair(ctx, plan, dir, removed, []string{dirty, removed}); err == nil {
		t.Fatal("unchanged rejection accepted")
	}
	git("commit", "--allow-empty", "-m", "Change only identity")
	changedID := git("rev-parse", "HEAD")
	if err := p.validatePushRepair(ctx, plan, dir, changedID, []string{dirty, removed, changedID}); err == nil {
		t.Fatal("HEAD deletion escaped historical cleanup")
	}
	cleanupErr := p.validatePushRepair(ctx, plan, dir, changedID, []string{dirty, removed, changedID})
	history, ok := cleanupErr.(*HistoricalCleanupIncomplete)
	if !ok || history.OriginalHead != removed || len(history.Candidate.Locations) != 1 || history.Candidate.Locations[0].Commit != dirty || history.Candidate.Kind != "historical_cleanup_incomplete" || len(history.Candidate.Known) != 1 {
		t.Fatal("known historical failure did not yield bounded semantic feedback", cleanupErr)
	}
	git("reset", "--hard", base)
	corrected := write("base\nreplacement fixture\n", "Correct original implementation")
	final := write("base\n", "Final implementation")
	if git("rev-parse", "HEAD^{tree}") != tree {
		t.Fatal("fixture final tree changed")
	}
	if err := p.validatePushRepair(ctx, plan, dir, final, []string{corrected, final}); err != nil {
		t.Fatal("real historical correction refused", err)
	}
	git("reflog", "expire", "--expire=now", "--all")
	git("gc", "--prune=now")
	if _, err := p.git(ctx, dir, "cat-file", "-e", blob); err == nil {
		t.Fatal("fixture still retained old offending blob")
	}
	p.RejectedBundlePath = rejectedBundle
	p.PushRepair.Pending.KnownHead, p.PushRepair.Pending.KnownTree = removed, tree
	if err := p.validatePushRepair(ctx, plan, dir, final, []string{corrected, final}); err != nil {
		t.Fatal("durable original audit could not restore private verification", err)
	}
	p.PushRepair.Pending.KnownTree = testTree
	if err := p.validatePushRepair(ctx, plan, dir, final, []string{corrected, final}); err == nil {
		t.Fatal("altered original audit tree accepted")
	}
	p.PushRepair.Pending.KnownTree = tree
	p.PushRepair.Pending.Cutoff = dirty
	if err := p.validatePushRepair(ctx, plan, dir, final, []string{corrected, final}); err == nil {
		t.Fatal("published cutoff rewrite accepted")
	}
	// A later genuine rejection has a different original audit graph. The
	// private audit cache must replace its ref while proving the new boundary.
	p.PushRepair.Pending.Cutoff = base
	git("reset", "--hard", base)
	secondRejected := write("base\nsecond disposable fake sensitive line\n", "Later offline rejected fixture")
	secondTree := git("rev-parse", "HEAD^{tree}")
	secondBlob := git("rev-parse", "HEAD:config.txt")
	secondBundle := filepath.Join(t.TempDir(), "later-rejected.bundle")
	git("bundle", "create", secondBundle, "HEAD")
	git("reset", "--hard", base)
	secondCorrection := write("base\nsecond safe fixture\n", "Correct later rejected history")
	secondFinal := write("base\n", "Later final implementation")
	p.RejectedBundlePath = secondBundle
	p.PushRepair.Pending.Head, p.PushRepair.Pending.KnownHead, p.PushRepair.Pending.KnownTree = secondRejected, secondRejected, secondTree
	p.PushRepair.Pending.Locations = []PushProtectionLocation{{Commit: secondRejected, Path: "config.txt", Line: 2, Blob: secondBlob}}
	if err := p.validatePushRepair(ctx, plan, dir, secondFinal, []string{secondCorrection, secondFinal}); err != nil {
		t.Fatal("later immutable audit failed to replace retained cache ref", err)
	}
	if git("rev-parse", "refs/sdlc/rejected") != secondRejected {
		t.Fatal("new immutable audit head not retained")
	}
}

func TestPushRepairRejectsOnlyEOLAndIndentationChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	identity := PublicationIdentity{}
	p := GitHubPublisher{Command: localCommand, Frozen: &identity}
	git := func(args ...string) string {
		t.Helper()
		out, err := p.git(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(text, message string) string {
		t.Helper()
		os.WriteFile(filepath.Join(dir, "config.txt"), []byte(text), 0600)
		git("add", ".")
		git("commit", "-m", message)
		return git("rev-parse", "HEAD")
	}
	git("init")
	git("config", "user.name", "Offline Test")
	git("config", "user.email", "offline@example.invalid")
	base := write("base\r\n", "Base")
	rejected := write("base\r\n    disposable fake sensitive line\r\n", "Offline rejected fixture")
	blob := git("rev-parse", rejected+":config.txt")
	plan := Plan{SourceSHA: base, Repository: "example/project", Branch: "TASK-1"}
	p.PushRepair = &PushProtectionRepair{Pending: &PushProtectionRejection{Identity: identity, Cutoff: base, Source: base, Head: rejected, Repository: plan.Repository, Branch: plan.Branch, Locations: []PushProtectionLocation{{Commit: rejected, Path: "config.txt", Line: 2, Blob: blob}}}}
	git("reset", "--hard", base)
	eolOnly := write("base\n  disposable fake sensitive line\n", "Change only formatting")
	if _, ok := p.validatePushRepair(ctx, plan, dir, eolOnly, []string{eolOnly}).(*HistoricalCleanupIncomplete); !ok {
		t.Fatal("newline or indentation rewrite escaped historical cleanup")
	}
}

func TestPushRepairGrantIsDurableBoundedAndKeepsSessionAccounting(t *testing.T) {
	dir, j := testRun(t)
	os.Mkdir(filepath.Join(dir, "publisher"), 0700)
	os.WriteFile(filepath.Join(dir, "source.bundle"), []byte("disposable offline bundle"), 0600)
	identity := &PublicationIdentity{}
	j.Plan.PublicationIdentity = identity
	j.SessionID = testNative
	j.Attempt = 4
	j.Rounds = 2
	j.CheckAttempt = 3
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	j.State = "publishing"
	r := &PushProtectionRejection{Identity: *identity, Cutoff: testBase, Source: testBase, Head: testHead, Tree: testTree, SignedHead: testSigned, Repository: j.Plan.Repository, Branch: j.Plan.Branch, Locations: []PushProtectionLocation{{Commit: testHead, Path: "config.txt", Line: 2, Blob: testTree}}}
	runner := Runner{}
	if err := runner.grantPushRepair(dir, j, r); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "repairing" || loaded.SessionID != testNative || loaded.Attempt != 4 || loaded.Rounds != 2 || loaded.CheckAttempt != 3 || loaded.Evidence.Passed || loaded.PushRepair.Pending == nil {
		t.Fatal("repair changed accounting or failed to persist")
	}
	if strings.Contains(loaded.Feedback, testTree) || strings.Contains(loaded.Feedback, "Disposable test type") {
		t.Fatal("private content leaked in guidance")
	}
	if err := validateRejectedBundle(dir, loaded.PushRepair); err != nil {
		t.Fatal(err)
	}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	if err := runner.grantPushRepair(dir, j, r); !errors.Is(err, ErrStopped) {
		t.Fatal("same rejection received second grant", err)
	}
	os.WriteFile(filepath.Join(dir, "publisher", r.AuditBundle), []byte("changed"), 0600)
	if err := validateRejectedBundle(dir, loaded.PushRepair); err == nil {
		t.Fatal("changed audit bundle accepted")
	}
}
