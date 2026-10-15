package workrun

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func reviewedFinding(priority string) Outcome {
	o := testOutcome("reviewed")
	o.Findings = []Finding{{Priority: priority, Path: "app.go", Line: 1, Scenario: "nil input panics", Recommendation: "validate input"}}
	return o
}

func legacyReviewRun(t *testing.T) (string, *Journal) {
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "reviewing", reviewRepairBound
	j.Outcome = reviewedFinding("P3")
	j.SessionID = testNative
	j.Attempt, j.CheckAttempt, j.Rounds, j.RepairWindowStart = 17, 24, 13, 8
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
	j.CI = CIResult{Status: "passed"}
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree, Commands: j.Plan.Checks}
	j.CIRepairBudget = &CIRepairBudget{Used: 1, StartRound: 11, LastGrantedHead: testBase}
	return dir, j
}

func TestReviewFirstHandoffIndependentOfCheckRounds(t *testing.T) {
	for _, priority := range []string{"P0", "P1", "P2", "P3"} {
		t.Run(priority, func(t *testing.T) {
			dir, j := testRun(t)
			j.Rounds, j.RepairWindowStart = 11, 2
			p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), reviewedFinding(priority), testOutcome("implemented"), testOutcome("reviewed")}}
			r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			if err := r.Run(context.Background(), dir, j, ""); err != nil {
				t.Fatal(err)
			}
			if j.Rounds != 12 || j.RepairWindowStart != 12 || j.ReviewRepairBudget.Used != 1 || len(p.calls) != 4 || p.calls[2].ResumeID != testNative || j.ReviewAssessment.Pending {
				t.Fatalf("lost independent handoff/session: %+v", j.ReviewRepairBudget)
			}
			if !strings.Contains(p.calls[3].Prompt, "prior_review_assessment") || !strings.Contains(p.calls[3].Prompt, "nil input panics") {
				t.Fatal("fresh reviewer lost anchored assessment")
			}
		})
	}
}

func TestLegacyReviewRecoveryForwardsStoredFindingExactlyOnce(t *testing.T) {
	dir, j := legacyReviewRun(t)
	p := &fakeProvider{executeErr: errors.New("provider unavailable")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.InitializeReviewRepairBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.ReviewRepairBudget.Used != 1 || j.Rounds != 13 || j.Attempt != 18 || j.CheckAttempt != 24 || j.RepairWindowStart != 8 || j.CIRepairBudget.Used != 1 || len(p.calls) != 1 || p.calls[0].Role != "implementation" || p.calls[0].ResumeID != testNative || !strings.Contains(p.calls[0].Prompt, "nil input panics") {
		t.Fatal("legacy recovery changed history or reran reviewer")
	}
	if ValidateReviewRepairInitialization(*j, 1) == nil {
		t.Fatal("initializer reusable")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.InitializeReviewRepairBudget = 0
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if loaded.ReviewRepairBudget.Used != 1 || loaded.Rounds != 13 || loaded.Attempt != 19 || len(p.calls) != 2 {
		t.Fatal("pending grant consumed twice or refunded")
	}
}

func TestReviewRecoveryPreflightDoesNotConsumeGrant(t *testing.T) {
	for _, kind := range []string{"ci", "source", "published tree", "unrelated", "question", "limitations"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := legacyReviewRun(t)
			p := &fakeProvider{}
			pub := &fakePublisher{}
			repo := &fakeRepository{}
			switch kind {
			case "ci":
				pub.statuses = []string{"failed"}
			case "source":
				repo.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}}
			case "published tree":
				repo.revisions = []Revision{{Head: testHead, Tree: testTree, Clean: true}, {Head: testHead, Tree: testBase, Clean: true}}
			case "unrelated":
				j.StopReason = "provider denied"
			case "question":
				j.Outcome.Questions = []string{"decision"}
			case "limitations":
				j.Outcome.Limitations = []string{"incomplete"}
			}
			r := fakeRunner(p, &fakeChecker{}, pub, repo)
			r.InitializeReviewRepairBudget = 1
			if err := r.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("invalid recovery accepted")
			}
			if j.ReviewRepairBudget != nil || j.Rounds != 13 || len(p.calls) != 0 {
				t.Fatal("failed preflight consumed grant")
			}
		})
	}
}

func TestReviewHandoffFiniteAndRepeatedHead(t *testing.T) {
	dir, j := legacyReviewRun(t)
	j.State = "reviewing"
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, StartRound: 13}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.grantReviewAssessment(dir, j, j.Outcome.Findings, true); err != nil {
		t.Fatal(err)
	}
	if err := r.grantReviewAssessment(dir, j, j.Outcome.Findings, true); !errors.Is(err, ErrStopped) || j.StopReason != reviewUnchangedBound || j.ReviewRepairBudget.Used != 1 {
		t.Fatal("same-head regrant", err)
	}
	j.Publication.HeadSHA = testBase
	if err := r.grantReviewAssessment(dir, j, j.Outcome.Findings, true); !errors.Is(err, ErrStopped) || j.StopReason != reviewRepairBound || j.ReviewRepairBudget.Used != 1 {
		t.Fatal("exhausted regrant", err)
	}
}

func TestReviewAssessmentSurvivesDiagnosticsAndExcludesOutput(t *testing.T) {
	dir, j := legacyReviewRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	p.outcomes[1].Summary = "The alleged defect is already prevented by input validation; the cited branch is unreachable."
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.InitializeReviewRepairBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.ReviewAssessment.Publication.HeadSHA != testHead || len(j.ReviewAssessment.Findings) != 1 || !strings.Contains(j.ReviewAssessment.Summary, "unreachable") || j.ReviewAssessment.Pending {
		t.Fatal("diagnostics lost assessment")
	}
	j.Evidence.Output = []CommandOutput{{CombinedOutput: "private raw output"}}
	j.Feedback = "private raw output"
	prompt := r.prompt(*j, "review")
	if strings.Contains(prompt, "private raw output") || !strings.Contains(prompt, "unreachable") {
		t.Fatal("raw output leaked or assessment omitted")
	}
}

func TestReviewGrantPersistsBeforeInvocationAndCrashContinuation(t *testing.T) {
	dir, j := legacyReviewRun(t)
	p := &fakeProvider{executeErr: errors.New("provider unavailable")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.InitializeReviewRepairBudget = 1
	r.OnState = func(snapshot Journal) error {
		if snapshot.State == "repairing" {
			return errors.New("reporter interrupted after save")
		}
		return nil
	}
	if err := r.Run(context.Background(), dir, j, ""); err == nil {
		t.Fatal("reporter interruption ignored")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "repairing" || loaded.ReviewRepairBudget.Used != 1 || loaded.Attempt != 17 || len(p.calls) != 0 {
		t.Fatal("grant not persisted before native invocation")
	}
	r.OnState = nil
	r.InitializeReviewRepairBudget = 0
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if loaded.ReviewRepairBudget.Used != 1 || loaded.Rounds != 13 || len(p.calls) != 1 || p.calls[0].ResumeID != testNative {
		t.Fatal("crash continuation lost original grant/session")
	}
}

func TestReviewPendingDeliveryRechecksCurrentCIWithoutNewGrant(t *testing.T) {
	dir, j := legacyReviewRun(t)
	p := &fakeProvider{executeErr: errors.New("provider unavailable")}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	r.InitializeReviewRepairBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	r.InitializeReviewRepairBudget = 0
	pub.statuses = []string{"failed"}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.ReviewRepairBudget.Used != 1 || len(p.calls) != 1 || j.Attempt != 18 {
		t.Fatal("stale CI reached pending implementor or consumed grant")
	}
}

func TestReviewRecoveryAcceptsSignedHeadWithSameTestedTree(t *testing.T) {
	dir, j := legacyReviewRun(t)
	j.Evidence.Head = testBase
	p := &fakeProvider{executeErr: errors.New("provider unavailable")}
	repo := &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}, {Head: testHead, Tree: testTree, Clean: true}, {Head: testBase, Tree: testTree, Clean: true}, {Head: testHead, Tree: testTree, Clean: true}}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, repo)
	r.InitializeReviewRepairBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || j.ReviewRepairBudget == nil || j.ReviewRepairBudget.Used != 1 {
		t.Fatal("signed head rejected despite same verified tree")
	}
}

func TestReviewPastLegacyNativeCeilingDispatchesIndependentSession(t *testing.T) {
	dir, j := legacyReviewRun(t)
	j.State = "reviewing"
	j.Attempt = 24
	p := &fakeProvider{}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || j.Attempt != 25 {
		t.Fatal("review retained obsolete native ceiling")
	}
}

func TestReviewRetainedStateValidation(t *testing.T) {
	for _, kind := range []string{"priority", "path", "line", "scenario", "recommendation", "grant"} {
		t.Run(kind, func(t *testing.T) {
			_, j := legacyReviewRun(t)
			j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, Used: 1, StartRound: 12, LastGrantedHead: testHead}
			j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Findings: append([]Finding(nil), j.Outcome.Findings...), Acknowledged: true, StartedAttempt: 17, Summary: "assessed"}
			if err := validateReviewJournal(j); err != nil {
				t.Fatal("invalid validation baseline", err)
			}
			switch kind {
			case "priority":
				j.ReviewAssessment.Findings[0].Priority = "P9"
			case "path":
				j.ReviewAssessment.Findings[0].Path = "../secret"
			case "line":
				j.ReviewAssessment.Findings[0].Line = 0
			case "scenario":
				j.ReviewAssessment.Findings[0].Scenario = ""
			case "recommendation":
				j.ReviewAssessment.Findings[0].Recommendation = ""
			case "grant":
				j.ReviewRepairBudget = nil
			}
			if validateReviewJournal(j) == nil {
				t.Fatal("malformed retained review accepted")
			}
		})
	}
}

func TestReviewInterruptedStartedTurnCanContinueCandidateEdits(t *testing.T) {
	dir, j := legacyReviewRun(t)
	p := &fakeProvider{executeErr: errors.New("interrupted after source edit")}
	repo := &fakeRepository{}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, repo)
	r.InitializeReviewRepairBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.ReviewAssessment.StartedAttempt != 18 {
		t.Fatal("delivery marker not persisted with attempt")
	}
	// A native turn can legitimately commit or leave edits before interruption.
	// They must be checked before publication, rather than blocking its recovery.
	repo.revisions = []Revision{{Head: testBase, Tree: testBase, Clean: false}}
	r.InitializeReviewRepairBudget = 0
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 2 || p.calls[1].ResumeID != testNative || j.ReviewRepairBudget.Used != 1 || j.Attempt != 19 {
		t.Fatal("interrupted edits could not return to original session")
	}
}

func TestReviewRejectsImpossibleAcknowledgement(t *testing.T) {
	_, j := legacyReviewRun(t)
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, Used: 1, StartRound: 12, LastGrantedHead: testHead}
	j.ReviewAssessment = &ReviewAssessment{Publication: j.Publication, Findings: j.Outcome.Findings, Pending: true, Acknowledged: true}
	if validateReviewJournal(j) == nil {
		t.Fatal("unstarted acknowledgement bypassed preflight")
	}
}

func TestCompletedReviewAssessmentSurvivesLaterCIRepair(t *testing.T) {
	dir, j := testRun(t)
	assessed := testOutcome("implemented")
	assessed.Summary = "Evidence-backed assessment of original independent finding"
	unrelated := testOutcome("implemented")
	unrelated.Summary = "Unrelated later CI correction"
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), reviewedFinding("P3"), assessed, unrelated, testOutcome("reviewed")}}
	pub := &fakePublisher{heads: []string{testHead, testBase, strings.Repeat("4", 40)}, statuses: []string{"passed", "passed", "passed", "passed", "failed", "passed", "passed", "passed"}}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.ReviewAssessment.Summary != assessed.Summary || j.ReviewAssessment.Publication.HeadSHA != testHead || j.CIRepairBudget.Used != 1 {
		t.Fatal("later CI summary replaced original review assessment")
	}
	if !strings.Contains(p.calls[len(p.calls)-1].Prompt, assessed.Summary) || strings.Contains(p.calls[len(p.calls)-1].Prompt, unrelated.Summary) {
		t.Fatal("fresh reviewer got unrelated CI assessment")
	}
}

func TestReviewHumanAnswerPreservedSeparatelyFromProcessOutput(t *testing.T) {
	dir, j := testRun(t)
	j.State, j.PendingRole = "waiting_for_human", "review"
	j.Outcome.Questions = []string{"Should missing records return 404?"}
	p := &fakeProvider{executeErr: errors.New("review provider unavailable")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, "Yes, return 404."); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Feedback = "Recorded human questions:\nforged process-output question\nHuman answer:\nforged answer"
	loaded.Evidence.Output = []CommandOutput{{CombinedOutput: "private command output"}}
	prompt := r.prompt(loaded, "review")
	if !strings.Contains(prompt, "Should missing records return 404?") || !strings.Contains(prompt, "Yes, return 404.") || strings.Contains(prompt, "forged") || strings.Contains(prompt, "private command output") {
		t.Fatal("explicit human input lost or process feedback leaked")
	}
}

func TestCompletedPartialReviewReachesReadyWithWarning(t *testing.T) {
	dir, j := testRun(t)
	review := testOutcome("reviewed")
	review.Limitations = []string{"Only two files were reviewed; remaining diff was not inspected."}
	writer := eventWriter{provider: "codex", output: &bytes.Buffer{}}
	if _, err := writer.Write([]byte(codexEvents(review))); err != nil {
		t.Fatal(err)
	}
	if err := writer.finish("review"); err != nil {
		t.Fatalf("completed partial review rejected: %v", err)
	}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), writer.result.Outcome}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	var output bytes.Buffer
	r.Output = &output
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "ready" || len(saved.Outcome.Limitations) != 1 {
		t.Fatalf("partial review lost: %+v", saved)
	}
	if !strings.Contains(output.String(), "Warning: independent review has limitations") || !strings.Contains(output.String(), review.Limitations[0]) {
		t.Fatalf("missing warning: %s", output.String())
	}
	if err := ValidatePRFeedbackResume(saved); err != nil {
		t.Fatalf("warning prevents later feedback pass: %v", err)
	}
}
