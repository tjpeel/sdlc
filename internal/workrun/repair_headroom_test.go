package workrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/tjpeel/sdlc/internal/runprogress"
	"io"
	"strings"
	"testing"
	"time"
)

func TestConfiguredFailuresDoNotSpendCIOrReviewRounds(t *testing.T) {
	dir, j := testRun(t)
	j.Rounds = 20
	configuredRepairs(j)
	// Subsequent failures in other stages remain in the shared history.
	j.Rounds += 15
	r := fakeRunner(&fakeProvider{}, &fakeChecker{failures: 20}, &fakePublisher{}, &fakeRepository{})
	r.MaxRounds = 12
	r.rawOutput = io.Discard
	recorder, err := runprogress.Open(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	r.progress = recorder
	for i := 0; i < 13; i++ {
		revision := Revision{Head: fmt.Sprintf("%040x", i+1), Tree: testTree, Clean: true}
		err := r.checkCandidate(context.Background(), dir, j, revision)
		if i < 12 && err != nil {
			t.Fatalf("other stages spent configured allowance at cycle %d: %v", i+1, err)
		}
		if i == 12 && (!errors.Is(err, ErrStopped) || j.StopReason != configuredCheckRepairBound) {
			t.Fatal("configured failures did not retain finite bound", err)
		}
	}
	if j.Rounds != 48 || j.ConfiguredRepairBudget.Used != 13 || j.ConfiguredRepairBudget.LegacyRounds != 20 {
		t.Fatal("failure history lost", j.ConfiguredRepairBudget, j.Rounds)
	}
	// A proven configured pass, rather than resume or CI, opens the next window.
	r.Checker = &fakeChecker{}
	if err := r.checkCandidate(context.Background(), dir, j, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if j.ConfiguredRepairBudget.WindowStart != 13 || j.ConfiguredRepairBudget.Used != 13 || j.Rounds != 48 {
		t.Fatal("successful suite reset cumulative history")
	}
}

func TestCIRepairHeadroomRetainsSameHeadHold(t *testing.T) {
	dir, j := testRun(t)
	j.SessionID = testNative
	j.Attempt = 40
	j.Rounds = 8
	j.CIRepairBudget = &CIRepairBudget{Used: 4, StartRound: 0, LastGrantedHead: testBase}
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: testHead, BaseSHA: testBase}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{failures: 100}, &fakeRepository{})
	for i := 4; i < 12; i++ {
		j.Publication.HeadSHA = fmt.Sprintf("%040x", i+10)
		if err := r.checkCI(context.Background(), dir, j); err != nil {
			t.Fatal("default headroom stopped early", i, err)
		}
	}
	if j.CIRepairBudget.Used != 12 || j.Rounds != 16 || j.Attempt != 40 {
		t.Fatal("CI history changed")
	}
	if err := r.checkCI(context.Background(), dir, j); !errors.Is(err, ErrStopped) || !strings.Contains(j.StopReason, "unchanged") {
		t.Fatal("same-head guard lost", err)
	}
	j.Publication.HeadSHA = testHead
	if err := r.checkCI(context.Background(), dir, j); !errors.Is(err, ErrStopped) || j.StopReason != ciRepairBound {
		t.Fatal("CI bound lost", err)
	}
}

func TestMissingCICheckCreationAfterThreeMinutesStillWaits(t *testing.T) {
	dir, j := testRun(t)
	j.State = "ci"
	j.SessionID = testNative
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: testHead, BaseSHA: testBase}
	j.MissingChecksSince = time.Now().Add(-3 * time.Minute)
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{statuses: []string{"missing"}}, &fakeRepository{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx, dir, j, ""); !errors.Is(err, ErrStopped) || strings.Contains(j.StopReason, "startup grace") {
		t.Fatal("delayed CI creation exhausted default grace", err, j.StopReason)
	}
	if j.Attempt != 0 {
		t.Fatal("CI waiting dispatched a provider")
	}
}

func TestReviewRepairIncreasePreservesUsageAndPersistsAudit(t *testing.T) {
	dir, j := testRun(t)
	j.SessionID = testNative
	j.Attempt, j.Rounds = 21, 6
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3, Used: 1, StartRound: 2, LastGrantedHead: testHead}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	before := *j.ReviewRepairBudget
	checkpoint := j.UpdatedAt
	if err := ValidateReviewRepairIncrease(*j, 12); err != nil {
		t.Fatal(err)
	}
	increaseReviewRepairLimit(j, 12)
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := loaded.ReviewRepairBudget
	if b.Limit != 12 || b.Used != before.Used || b.StartRound != before.StartRound || b.LastGrantedHead != before.LastGrantedHead || loaded.Attempt != 21 || loaded.Rounds != 6 || loaded.SessionID != testNative {
		t.Fatal("increase changed cumulative accounting")
	}
	if len(loaded.ReviewRepairIncreases) != 1 {
		t.Fatal("increase audit missing")
	}
	a := loaded.ReviewRepairIncreases[0]
	if a.OriginalLimit != 3 || a.Limit != 12 || !a.CheckpointUpdatedAt.Equal(checkpoint) || a.SessionID != testNative || a.Attempt != 21 || a.Rounds != 6 {
		t.Fatal("increase audit lost original boundary", a)
	}
	if ValidateReviewRepairIncrease(loaded, 12) == nil || ValidateReviewRepairIncrease(loaded, 3) == nil {
		t.Fatal("non-increase accepted")
	}
}

func TestConfiguredCrossStageHeadroom(t *testing.T) {
	dir, j := testRun(t)
	j.SessionID = testNative
	j.Publication = annotationPublication()
	j.CIRepairBudget = &CIRepairBudget{}
	j.ReviewRepairBudget = &ReviewRepairBudget{Limit: 3}
	r := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{failures: 100}, &fakeRepository{})
	r.MaxRounds = 1
	r.rawOutput = io.Discard
	recorder, err := runprogress.Open(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	r.progress = recorder
	if err := r.checkCandidate(context.Background(), dir, j, Revision{Head: testHead, Tree: testTree, Clean: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.checkCI(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	if err := r.grantReviewAssessment(dir, j, reviewedFinding("P1").Findings, true); err != nil {
		t.Fatal(err)
	}
	r.Checker = &fakeChecker{failures: 10}
	if err := r.checkCandidate(context.Background(), dir, j, Revision{Head: testBase, Tree: testBase, Clean: true}); err != nil {
		t.Fatal("CI and review grants spent configured failure allowance", err)
	}
	if j.Rounds != 3 || j.State != "repairing" {
		t.Fatal("cross-stage history or repair result lost", j.Rounds, j.State)
	}
}
