package workrun

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func overrideGrant(j *Journal, kind string, limit int) {
	j.RunLimitOverride = &RunLimitOverride{GrantedAt: time.Now().UTC(), CheckpointUpdatedAt: j.UpdatedAt, GrantedAttempt: j.Attempt, GrantedRounds: j.Rounds, OriginalSessionID: j.SessionID, LimitKind: kind, OriginalLimit: limit}
}

func retainedRequestHold(t *testing.T) (string, *Journal) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "implementing", retainedVerificationBound
	j.Attempt, j.SessionID = 26, testNative
	j.SessionTurnExtension = &SessionTurnExtension{GrantedAt: time.Now().UTC(), GrantedAttempt: 24, OriginalLimit: 24, Additional: 6, OriginalSessionID: testNative}
	j.Outcome = testOutcome("checks_requested")
	for i := 0; i < 16; i++ {
		request := VerificationRequest{ID: fmt.Sprintf("check_%d", i), Purpose: "inspect candidate", Commands: [][]string{{"go", "test", "./example"}}}
		result := VerificationResult{Request: request}
		if i < 12 {
			result.Key, result.Head, result.Tree = strings.Repeat("a", 64), testHead, testTree
			result.Passed = i < 10
		}
		j.Verification = append(j.Verification, result)
		if i >= 12 {
			j.Outcome.VerificationRequests = append(j.Outcome.VerificationRequests, request)
		}
	}
	j.Outcome.VerificationRequests = append(j.Outcome.VerificationRequests, VerificationRequest{ID: "final_check", Purpose: "inspect final candidate", Commands: [][]string{{"go", "test", "./example"}}})
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j
}

func TestMergeVerificationIsAtomic(t *testing.T) {
	_, j := retainedRequestHold(t)
	before := append([]VerificationResult(nil), j.Verification...)
	requests := append([]VerificationRequest(nil), j.Outcome.VerificationRequests...)
	requests[0].Purpose = "replacement purpose"
	if err := mergeVerification(j, requests); err == nil || err.Error() != retainedVerificationBound {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, j.Verification) {
		t.Fatal("capacity failure partially replaced retained evidence")
	}
	j.Verification[1].Request.Baseline = regressionRequest().Baseline
	before = append([]VerificationResult(nil), j.Verification...)
	requests = []VerificationRequest{j.Verification[0].Request, j.Verification[1].Request}
	requests[0].Purpose = "replacement purpose"
	requests[1].Baseline = nil
	if err := mergeVerification(j, requests); err == nil {
		t.Fatal("baseline downgrade accepted")
	}
	if !reflect.DeepEqual(before, j.Verification) {
		t.Fatal("baseline failure partially replaced retained evidence")
	}
}

func TestOverrideReplaysSavedBatchWithoutNativeTurn(t *testing.T) {
	dir, j := retainedRequestHold(t)
	p := &fakeProvider{}
	c := &fakeChecker{err: errors.New("isolated check worker unavailable")}
	r := fakeRunner(p, c, &fakePublisher{}, &fakeRepository{})
	r.OverrideRunLimits = true
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 0 || j.Attempt != 26 || c.calls != 1 || len(j.Verification) != 17 {
		t.Fatalf("saved batch spent another turn: attempts=%d native=%d checks=%d retained=%d", j.Attempt, len(p.calls), c.calls, len(j.Verification))
	}
	if j.RunLimitOverride == nil || j.RunLimitOverride.LimitKind != "verification_requests" || j.RunLimitOverride.PendingVerificationReplay || j.SessionTurnExtension.Additional != 6 {
		t.Fatal("override audit or extension lost")
	}
	if !j.Verification[0].Passed || j.Verification[10].Passed || j.Verification[16].Request.ID != "final_check" {
		t.Fatal("retained evidence changed")
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded.Verification) != 17 || loaded.RunLimitOverride == nil || loaded.SessionTurnExtension.Additional != 6 {
		t.Fatalf("override did not roundtrip: %v", err)
	}
	r.OverrideRunLimits = true
	if err := r.Run(context.Background(), dir, &loaded, ""); err == nil {
		t.Fatal("override flag admitted nonnumeric check-worker hold")
	}
	if len(p.calls) != 0 {
		t.Fatal("nonnumeric hold invoked provider")
	}
}

func TestOverrideResumesInterruptedSavedBatchReplay(t *testing.T) {
	dir, j := retainedRequestHold(t)
	overrideGrant(j, "verification_requests", 16)
	j.RunLimitOverride.PendingVerificationReplay = true
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	r := fakeRunner(p, &fakeChecker{err: errors.New("check worker unavailable")}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 0 || loaded.Attempt != 26 || len(loaded.Verification) != 17 || loaded.RunLimitOverride.PendingVerificationReplay {
		t.Fatal("interrupted replay consumed native turn")
	}
}

func TestRunLimitOverrideAdmissionRejectsOtherHolds(t *testing.T) {
	_, j := retainedRequestHold(t)
	if err := ValidateRunLimitOverride(*j); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"failed", "provider", "human", "no-session", "pending-inputs", "candidate", "not-full", "not-spent", "bad-outcome", "negative-rounds"} {
		t.Run(name, func(t *testing.T) {
			copy := *j
			switch name {
			case "failed":
				copy.State = "failed"
			case "provider":
				copy.StopReason = "provider usage limit reached"
			case "human":
				copy.State = "waiting_for_human"
			case "no-session":
				copy.SessionID = ""
			case "pending-inputs":
				copy.PendingInputs = &PendingInputRecovery{}
			case "candidate":
				copy.CandidatePhasePending = true
			case "not-full":
				copy.Verification = copy.Verification[:15]
			case "not-spent":
				copy.Attempt = 0
				copy.SessionTurnExtension = nil
			case "bad-outcome":
				copy.Outcome.Status = "failed"
			case "negative-rounds":
				copy.Rounds = -1
			}
			if ValidateRunLimitOverride(copy) == nil {
				t.Fatal("invalid hold accepted")
			}
		})
	}
}

func TestOverridePreservesRequestAndJournalResourceBounds(t *testing.T) {
	dir, j := retainedRequestHold(t)
	overrideGrant(j, "verification_requests", 16)
	if err := mergeVerification(j, j.Outcome.VerificationRequests); err != nil {
		t.Fatal(err)
	}
	requests := make([]VerificationRequest, 17)
	for i := range requests {
		requests[i] = VerificationRequest{ID: fmt.Sprintf("request_%d", i), Purpose: "verification", Commands: [][]string{{"go", "test"}}}
	}
	if mergeVerification(j, requests) == nil {
		t.Fatal("native batch ceiling bypassed")
	}
	bad := requests[0]
	bad.Commands = make([][]string, 17)
	for i := range bad.Commands {
		bad.Commands[i] = []string{"go", "test"}
	}
	if mergeVerification(j, []VerificationRequest{bad}) == nil {
		t.Fatal("command ceiling bypassed")
	}
	bad = requests[0]
	bad.Purpose = strings.Repeat("x", 1025)
	if mergeVerification(j, []VerificationRequest{bad}) == nil {
		t.Fatal("request bytes ceiling bypassed")
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	j.Feedback = strings.Repeat("x", 1024*1024)
	if Save(dir, j) == nil {
		t.Fatal("journal size ceiling bypassed")
	}
	loaded, err := Load(dir)
	if err != nil || len(loaded.Verification) != 17 {
		t.Fatalf("oversized save replaced checkpoint: %v", err)
	}
}

func TestOverrideContinuesOriginalSessionBeyondExtendedCeiling(t *testing.T) {
	dir, j := retainedRequestHold(t)
	overrideGrant(j, "verification_requests", 16)
	j.RunLimitOverride.PendingVerificationReplay = true
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	grant := *j.RunLimitOverride
	extension := *j.SessionTurnExtension
	outcomes := make([]Outcome, 15)
	for i := range outcomes {
		outcomes[i] = testOutcome("checks_requested")
	}
	outcomes[len(outcomes)-1] = testOutcome("waiting_for_human")
	outcomes[len(outcomes)-1].Questions = []string{"Confirm the next scope decision"}
	p := &fakeProvider{outcomes: outcomes}
	c := &supplementaryChecker{}
	r := fakeRunner(p, &c.fakeChecker, &fakePublisher{}, &fakeRepository{})
	r.Checker = c
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	grant.PendingVerificationReplay = false
	if j.Attempt != 41 || len(p.calls) != 15 || j.State != "waiting_for_human" || j.VerificationFeedbackCount <= 2 || !reflect.DeepEqual(grant, *j.RunLimitOverride) || !reflect.DeepEqual(extension, *j.SessionTurnExtension) {
		t.Fatalf("continuation lost accounting: attempt=%d native=%d repeats=%d state=%s", j.Attempt, len(p.calls), j.VerificationFeedbackCount, j.State)
	}
	for _, call := range p.calls {
		if call.ResumeID != testNative {
			t.Fatal("continuation restarted implementation session")
		}
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Attempt != 41 || loaded.SessionTurnExtension.Additional != 6 {
		t.Fatalf("extended continuation failed persistence: %v", err)
	}
}

func TestOverrideRelaxesOnlyNumericAccountingUpperBounds(t *testing.T) {
	_, j := retainedRequestHold(t)
	overrideGrant(j, "verification_requests", 16)
	j.Attempt, j.Rounds = 45, 10
	j.CIRepairBudget = &CIRepairBudget{Used: 4, StartRound: 0, LastGrantedHead: testBase}
	j.ReviewRepairBudget = &ReviewRepairBudget{Used: 4, Limit: 1, StartRound: 0, LastGrantedHead: testBase}
	if err := validatePersistedRunLimitOverride(j); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionTurnExtension(j); err != nil {
		t.Fatal(err)
	}
	if err := validateCIJournal(j); err != nil {
		t.Fatal(err)
	}
	if err := validateReviewJournal(j); err != nil {
		t.Fatal(err)
	}
	without := *j
	without.RunLimitOverride = nil
	if validateSessionTurnExtension(&without) == nil || validateCIJournal(&without) == nil || validateReviewJournal(&without) == nil {
		t.Fatal("default accounting ceilings changed")
	}
	for _, name := range []string{"ci-negative", "ci-rounds", "review-negative", "review-limit", "session", "grant-time", "grant-attempt"} {
		t.Run(name, func(t *testing.T) {
			copy := *j
			ci := *j.CIRepairBudget
			review := *j.ReviewRepairBudget
			extension := *j.SessionTurnExtension
			override := *j.RunLimitOverride
			copy.CIRepairBudget, copy.ReviewRepairBudget, copy.SessionTurnExtension, copy.RunLimitOverride = &ci, &review, &extension, &override
			switch name {
			case "ci-negative":
				ci.Used = -1
			case "ci-rounds":
				ci.StartRound = 9
			case "review-negative":
				review.Used = -1
			case "review-limit":
				review.Limit = 4
			case "session":
				extension.OriginalSessionID = "different-session"
			case "grant-time":
				override.GrantedAt = time.Time{}
			case "grant-attempt":
				override.GrantedAttempt = 46
			}
			if validatePersistedRunLimitOverride(&copy) == nil && validateSessionTurnExtension(&copy) == nil && validateCIJournal(&copy) == nil && validateReviewJournal(&copy) == nil {
				t.Fatal("invalid accounting accepted")
			}
		})
	}
}

func TestRunLimitOverrideAdmitsSupportedNumericHolds(t *testing.T) {
	for _, kind := range []string{"native_turns", "configured_repairs", "ci_repairs", "review_handoffs", "verification_feedback", "verification_requests", "pr_metadata"} {
		t.Run(kind, func(t *testing.T) {
			_, j := retainedRequestHold(t)
			j.Rounds = 4
			switch kind {
			case "native_turns":
				j.ResumeState, j.StopReason, j.Attempt = "repairing", implementationTurnBound, 30
			case "configured_repairs":
				j.ResumeState, j.StopReason = "checking", configuredCheckRepairBound
				j.Evidence = CheckEvidence{CompletedFailureKey: strings.Repeat("a", 64)}
			case "ci_repairs":
				j.ResumeState, j.StopReason = "ci", ciRepairBound
				j.CI.Status = "failed"
				j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
				j.CIRepairBudget = &CIRepairBudget{Used: 3, LastGrantedHead: testBase}
			case "review_handoffs":
				j.ResumeState, j.StopReason = "reviewing", reviewRepairBound
				j.CI.Status = "passed"
				j.Outcome = reviewedFinding("P1")
				j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
				j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, Used: 1, LastGrantedHead: testBase}
			case "verification_feedback":
				j.ResumeState, j.StopReason = "checking", verificationFeedbackBound
				j.VerificationFeedbackCount, j.VerificationFeedbackKey, j.Evidence.Passed = 3, strings.Repeat("b", 64), true
			case "pr_metadata":
				j.ResumeState, j.StopReason = "checking", "PR metadata still violates the frozen publication contract: missing metadata"
				j.PRMetadataCorrections, j.Evidence.Passed = 3, true
				j.Plan.PRMetadataPolicy = DefaultPRMetadataPolicy()
				j.Outcome = testOutcome("implemented")
			}
			if err := ValidateRunLimitOverride(*j); err != nil {
				t.Fatal(err)
			}
			got, _, err := admittedRunLimit(*j)
			if err != nil || got != kind {
				t.Fatalf("admitted %s: %v", got, err)
			}
		})
	}
}

func TestOverrideDoesNotPermitSameHeadRepairHandoffs(t *testing.T) {
	for _, kind := range []string{"ci", "review"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := retainedRequestHold(t)
			overrideGrant(j, "verification_requests", 16)
			j.Rounds = 6
			j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
			p := &fakeProvider{}
			r := fakeRunner(p, &fakeChecker{}, &fakePublisher{statuses: []string{"failed"}}, &fakeRepository{})
			if kind == "ci" {
				j.State = "ci"
				j.CIRepairBudget = &CIRepairBudget{Used: 4, LastGrantedHead: testHead}
				if err := r.checkCI(context.Background(), dir, j); !errors.Is(err, ErrStopped) {
					t.Fatal(err)
				}
				if !strings.Contains(j.StopReason, "new published head") || j.CIRepairBudget.Used != 4 {
					t.Fatal("CI same-head gate bypassed")
				}
			} else {
				j.State = "reviewing"
				j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, Used: 4, LastGrantedHead: testHead}
				if err := r.grantReviewAssessment(dir, j, reviewedFinding("P1").Findings, true); !errors.Is(err, ErrStopped) {
					t.Fatal(err)
				}
				if j.StopReason != reviewUnchangedBound || j.ReviewRepairBudget.Used != 4 {
					t.Fatal("review same-head gate bypassed")
				}
			}
			if len(p.calls) != 0 {
				t.Fatal("same-head hold invoked provider")
			}
		})
	}
}

func TestOverridePreservesProviderFailureAndSourcePatchContract(t *testing.T) {
	dir, j := retainedRequestHold(t)
	overrideGrant(j, "verification_requests", 16)
	j.State, j.ResumeState, j.StopReason = "implementing", "", ""
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	incompatible := regressionRequest()
	incompatible.SourcePatch = &SourcePatchRequest{Paths: []string{"example/source.go"}}
	if mergeVerification(j, []VerificationRequest{incompatible}) == nil {
		t.Fatal("override accepted combined baseline and source correction")
	}
	p := &fakeProvider{executeErr: errors.New("provider account usage exhausted")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if j.State != "failed" || j.Attempt != 27 || len(p.calls) != 1 || p.calls[0].ResumeID != testNative || j.SessionID != testNative {
		t.Fatal("override retried or replaced provider session after failure")
	}
	if err := ValidateRunLimitOverride(*j); err == nil {
		t.Fatal("override admitted provider failure")
	}
}

func TestSavedBatchReplayRetainsCompanionAndAssessmentHandling(t *testing.T) {
	for _, kind := range []string{"companion", "assessment"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := retainedRequestHold(t)
			p := &fakeProvider{}
			c := &fakeChecker{}
			r := fakeRunner(p, c, &fakePublisher{}, &fakeRepository{})
			r.OverrideRunLimits = true
			cloner := &fakeCompanionCloner{err: errors.New("source reader unavailable")}
			if kind == "companion" {
				j.Outcome.RepositoryRequests = []CompanionRepositoryRequest{{ID: "companion", Repository: "example/companion", Purpose: "inspect related contract"}}
				r.CompanionCloner = cloner
			} else {
				j.Outcome.PRFeedbackAssessments = []PRFeedbackAssessment{{ID: "comment:1", Fingerprint: strings.Repeat("a", 64), Disposition: "no_change", Reason: "Current implementation already handles the case", Commits: []string{}}}
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
				t.Fatal(err)
			}
			if len(p.calls) != 0 || c.calls != 0 || j.Attempt != 26 || len(j.Verification) != 17 || !j.RunLimitOverride.PendingVerificationReplay {
				t.Fatal("saved outcome processing skipped or repeated native work")
			}
			if kind == "companion" && (len(cloner.calls) != 1 || cloner.calls[0] != "example/companion") {
				t.Fatal("saved companion request was skipped")
			}
			if kind == "assessment" && j.StopReason != "no pending human feedback revision" {
				t.Fatal("saved PR assessment bypassed ordinary pending-comment gate")
			}
		})
	}
}

func TestCIOverrideRetainsUnknownLowerConfiguredCeiling(t *testing.T) {
	_, j := retainedRequestHold(t)
	j.ResumeState, j.StopReason = "ci", ciRepairBound
	j.Rounds, j.CI.Status = 2, "failed"
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
	j.CIRepairBudget = &CIRepairBudget{Used: 2, LastGrantedHead: testBase}
	if err := ValidateRunLimitOverride(*j); err != nil {
		t.Fatal(err)
	}
	kind, limit, err := admittedRunLimit(*j)
	if err != nil || kind != "ci_repairs" || limit != 0 {
		t.Fatalf("invented historical CI ceiling: %s %d %v", kind, limit, err)
	}
	overrideGrant(j, kind, limit)
	if err := validatePersistedRunLimitOverride(j); err != nil {
		t.Fatal(err)
	}
	if j.CIRepairBudget.Used != 2 || j.CIRepairBudget.LastGrantedHead != testBase {
		t.Fatal("CI admission changed existing ledger")
	}
}

func TestSavedBatchReplayRecoversIntermediateOutcomeCheckpoints(t *testing.T) {
	for _, checkpoint := range []string{"assessment", "first-companion"} {
		t.Run(checkpoint, func(t *testing.T) {
			dir, j := retainedRequestHold(t)
			first := generalFeedback("comment:1", "Inspect the related source")
			next := generalFeedback("comment:2", "Explain the remaining contract")
			j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, Active: true, CandidateStart: testBase, Entries: []PRFeedbackEntry{{Comment: first, Fingerprint: feedbackFingerprint(first), StartedAttempt: 26}, {Comment: next, Fingerprint: feedbackFingerprint(next)}}}
			j.Outcome.PRFeedbackAssessments = assessedOutcome(first, "no_change").PRFeedbackAssessments
			j.Outcome.RepositoryRequests = []CompanionRepositoryRequest{{ID: "first", Repository: "example/first", Purpose: "inspect first contract"}, {ID: "second", Repository: "example/second", Purpose: "inspect second contract"}}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			p := &fakeProvider{}
			cloner := &fakeCompanionCloner{revision: testHead}
			r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			r.OverrideRunLimits, r.CompanionCloner = true, cloner
			interrupted := errors.New("controller interrupted after saved outcome checkpoint")
			r.OnState = func(saved Journal) error {
				if saved.RunLimitOverride == nil || !saved.RunLimitOverride.PendingVerificationReplay {
					return nil
				}
				if checkpoint == "assessment" && saved.PRFeedback.Entries[0].Assessment != nil && len(saved.CompanionRepositories) == 0 {
					return interrupted
				}
				if checkpoint == "first-companion" && len(saved.CompanionRepositories) == 1 {
					return interrupted
				}
				return nil
			}
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
			loaded, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !loaded.RunLimitOverride.PendingVerificationReplay || !loaded.RunLimitOverride.VerificationReplayFeedbackProcessed || loaded.PRFeedback.Entries[0].Assessment == nil || loaded.PRFeedback.Entries[1].Assessment != nil || loaded.Attempt != 26 || len(p.calls) != 0 {
				t.Fatal("intermediate checkpoint lost replay or assessment progress")
			}
			originalMarker := loaded.PRFeedback.Entries[0].Marker
			if checkpoint == "first-companion" && len(loaded.CompanionRepositories) != 1 {
				t.Fatal("first companion was not durably checkpointed")
			}
			// Interrupt again at the durable completion checkpoint, before another turn.
			completed := errors.New("controller interrupted after completed outcome checkpoint")
			r.OverrideRunLimits = false
			r.OnState = func(saved Journal) error {
				if !saved.RunLimitOverride.PendingVerificationReplay && len(saved.CompanionRepositories) == 2 {
					return completed
				}
				return nil
			}
			if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, completed) {
				t.Fatal(err)
			}
			final, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if final.RunLimitOverride.PendingVerificationReplay || final.RunLimitOverride.VerificationReplayFeedbackProcessed || len(final.CompanionRepositories) != 2 || final.PRFeedback.Entries[0].Marker != originalMarker || final.PRFeedback.Entries[1].Assessment != nil || pendingFeedback(&final).Comment.ID != next.ID || !strings.Contains(final.Feedback, "example/second") || final.Attempt != 26 || len(p.calls) != 0 {
				t.Fatal("recovery repeated assessment, dropped companion or consumed native turn")
			}
		})
	}
}
