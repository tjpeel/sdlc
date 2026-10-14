package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeBoundRun(t *testing.T) (string, *Journal) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "repairing", implementationTurnBound
	j.Attempt, j.SessionID = 24, testNative
	j.Rounds, j.RepairWindowStart = 25, 24
	j.CIRepairBudget = &CIRepairBudget{StartRound: 25}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, StartRound: 25}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j
}

func TestNativeTurnExtensionPersistsAndNeverRefundsFailedCalls(t *testing.T) {
	dir, j := nativeBoundRun(t)
	p := &fakeProvider{executeErr: errors.New("offline provider failure")}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ExtendSessionTurnBudget = 2
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || j.Attempt != 25 || j.Rounds != 25 || j.RepairWindowStart != 24 || j.CIRepairBudget.StartRound != 25 || j.ReviewRepairBudget.Limit != 1 {
		t.Fatal("extension changed original session or phase budgets")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if historicalNativeTurnLimit(&loaded) != 26 || loaded.SessionTurnExtension.GrantedAttempt != 24 {
		t.Fatal("grant not persisted")
	}
	if err := ValidateSessionTurnExtension(loaded, 2); err == nil {
		t.Fatal("repeat extension accepted")
	}
	r.ExtendSessionTurnBudget = 0
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if loaded.Attempt != 26 || len(p.calls) != 2 {
		t.Fatal("failed turn refunded")
	}
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if loaded.Attempt != 27 || len(p.calls) != 3 || loaded.StopReason == implementationTurnBound {
		t.Fatal("legacy extension imposed a dispatch ceiling")
	}
}

func TestNativeTurnExtensionSharesLimitWithFreshReview(t *testing.T) {
	dir, j := nativeBoundRun(t)
	p := &fakeProvider{outcomes: []Outcome{testOutcome("checks_requested"), testOutcome("implemented"), testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ExtendSessionTurnBudget = 3
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.Attempt != 27 || len(p.calls) != 3 || p.calls[0].ResumeID != testNative || p.calls[1].ResumeID != testNative || p.calls[2].ResumeID != "" {
		t.Fatal("implementation and fresh review did not share extended capacity")
	}
}

func TestNativeTurnExtensionRejectsWithoutCheckpointChanges(t *testing.T) {
	for _, name := range []string{"unrelated", "stage", "attempt", "session", "pending", "input", "reporting", "combined", "check-input", "cleanup"} {
		t.Run(name, func(t *testing.T) {
			dir, j := nativeBoundRun(t)
			r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
			r.ExtendSessionTurnBudget = 1
			switch name {
			case "cleanup":
				j.CandidatePhasePending = true
			case "unrelated":
				j.StopReason = "provider unavailable"
			case "stage":
				j.ResumeState = "checking"
			case "attempt":
				j.Attempt = 23
			case "session":
				j.SessionID = ""
			case "pending":
				j.PendingInputs = &PendingInputRecovery{}
			case "input":
				j.Plan.Inputs = []Input{{Path: "missing.txt", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
			case "check-input":
				j.Plan.CheckInputs = []Input{{Path: "missing.txt", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
			case "reporting":
				r.OnStart = func(Journal) error { return errors.New("offline reporting error") }
			case "combined":
				r.InitializeCIRepairBudget = true
			}
			// Persist the malformed admission state only for cases supported by journal validation.
			if name != "pending" {
				if err := Save(dir, j); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			snapshot, _ := json.Marshal(j)
			if err := r.Run(context.Background(), dir, j, ""); err == nil {
				t.Fatal("invalid extension accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			now, _ := json.Marshal(j)
			if string(before) != string(after) || string(snapshot) != string(now) || len(r.Provider.(*fakeProvider).calls) != 0 {
				t.Fatal("rejection changed checkpoint or called provider")
			}
		})
	}
}

func TestNativeTurnExtensionValidationAndDefaultCap(t *testing.T) {
	_, j := nativeBoundRun(t)
	for _, stage := range []string{"implementing", "repairing", "ci", "reviewing", "awaiting_reviewer"} {
		copy := *j
		copy.ResumeState = stage
		if stage == "reviewing" || stage == "awaiting_reviewer" {
			copy.StopReason = reviewTurnBound
		}
		if err := ValidateSessionTurnExtension(copy, 24); err != nil {
			t.Fatal(stage, err)
		}
	}
	for _, n := range []int{0, -1, 25} {
		if ValidateSessionTurnExtension(*j, n) == nil {
			t.Fatal("invalid amount accepted")
		}
	}
	e := SessionTurnExtension{GrantedAt: time.Now().UTC(), GrantedAttempt: 24, OriginalLimit: 24, Additional: 1, OriginalSessionID: testNative}
	for _, name := range []string{"time", "attempt", "original", "additional", "session", "decrement"} {
		copy := *j
		grant := e
		copy.SessionTurnExtension = &grant
		switch name {
		case "time":
			grant.GrantedAt = time.Time{}
		case "attempt":
			grant.GrantedAttempt = 23
		case "original":
			grant.OriginalLimit = 25
		case "additional":
			grant.Additional = 25
		case "session":
			grant.OriginalSessionID = "rotated"
		case "decrement":
			copy.Attempt = 23
		}
		if validateSessionTurnExtension(&copy) == nil {
			t.Fatal(name, "invalid persisted extension accepted")
		}
	}
	dir, j := nativeBoundRun(t)
	p := &fakeProvider{}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	for i := 0; i < 32; i++ {
		p.outcomes = append(p.outcomes, testOutcome("checks_requested"))
	}
	j.SessionTurnExtension = &e
	for i := 0; i < 32; i++ {
		if _, err := r.session(context.Background(), dir, j, "implementation", j.Workspace, testNative); err != nil {
			t.Fatal(err)
		}
	}
	if j.Attempt != 56 || len(p.calls) != 32 || j.SessionID != testNative || validateSessionTurnExtension(j) != nil {
		t.Fatal("successful dispatches lost cumulative accounting or original session")
	}
}

func TestNativeTurnExtensionAdmitsCIAndLegacyReviewWithoutResettingBudgets(t *testing.T) {
	dir, j := legacyReviewRun(t)
	j.Attempt = 24
	j.SessionTurnExtension = &SessionTurnExtension{GrantedAt: time.Now().UTC(), GrantedAttempt: 24, OriginalLimit: 24, Additional: 2, OriginalSessionID: testNative}
	if err := ValidateReviewRepairInitialization(*j, 1); err != nil {
		t.Fatal("legacy review ignored effective native capacity", err)
	}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 1, StartRound: 12}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := r.grantReviewAssessment(dir, j, j.Outcome.Findings, false); err != nil {
		t.Fatal(err)
	}
	if j.Attempt != 24 || j.CIRepairBudget.Used != 1 || j.ReviewRepairBudget.Used != 1 {
		t.Fatal("review grant changed native or CI accounting")
	}
	// CI is separately bounded but must admit native capacity above the default cap.
	dir, j = testRun(t)
	j.State = "ci"
	j.SessionID = testNative
	j.Attempt = 24
	j.SessionTurnExtension = &SessionTurnExtension{GrantedAt: time.Now().UTC(), GrantedAttempt: 24, OriginalLimit: 24, Additional: 1, OriginalSessionID: testNative}
	j.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", HeadSHA: testHead, BaseSHA: testBase}
	j.CIRepairBudget = &CIRepairBudget{}
	r = fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{failures: 100}, &fakeRepository{})
	if err := r.checkCI(context.Background(), dir, j); err != nil {
		t.Fatal("CI ignored extended native capacity", err)
	}
	if j.CIRepairBudget.Used != 1 || j.Attempt != 24 || j.State != "repairing" {
		t.Fatal("CI grant changed capacity or failed to enter repair")
	}
}

func TestMalformedNativeTurnExtensionRejectedBySaveAndLoad(t *testing.T) {
	dir, j := nativeBoundRun(t)
	j.SessionTurnExtension = &SessionTurnExtension{GrantedAt: time.Now().UTC(), GrantedAttempt: 24, OriginalLimit: 24, Additional: 1, OriginalSessionID: testNative}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	j.SessionTurnExtension.Additional = 25
	if err := Save(dir, j); err == nil {
		t.Fatal("Save accepted inflated grant")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if string(before) != string(after) {
		t.Fatal("invalid Save replaced checkpoint")
	}
	var value map[string]any
	if err := json.Unmarshal(before, &value); err != nil {
		t.Fatal(err)
	}
	value["session_turn_extension"].(map[string]any)["additional"] = 25
	data, _ := json.Marshal(value)
	if err := os.WriteFile(filepath.Join(dir, "journal.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted inflated persisted grant")
	}
}

func TestNativeTurnExtensionResumesReviewerCapInOriginalRun(t *testing.T) {
	dir, j := legacyReviewRun(t)
	j.Attempt = 24
	j.StopReason = reviewTurnBound
	j.Outcome = testOutcome("implemented")
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
	r := fakeRunner(p, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	r.ExtendSessionTurnBudget = 1
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || j.Attempt != 25 || j.SessionID != testNative || len(p.calls) != 1 || p.calls[0].Role != "review" || p.calls[0].ResumeID != "" || j.CIRepairBudget.Used != 1 || j.Rounds != 13 {
		t.Fatal("review continuation changed original implementation or phase accounting")
	}
}
