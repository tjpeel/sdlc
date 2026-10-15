package workrun

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestCheckTimingObservesCommandCopyAndCleanupBoundaries(t *testing.T) {
	checker, _, _, workspace := checkFixture(t)
	var observations []CheckTiming
	timed := checker.WithTimingObserver(func(observation CheckTiming) { observations = append(observations, observation) })
	if err := timed.Check(context.Background(), workspace, [][]string{{"dotnet", "restore"}, {"dotnet", "build", "--no-restore"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 4 || observations[0].Kind != "source_copy" || observations[1].Kind != "command" || observations[1].Command != 1 || observations[2].Command != 2 || observations[3].Kind != "cleanup" {
		t.Fatal("command/setup/cleanup boundaries were not observed", observations)
	}
	for _, observation := range observations {
		if observation.Phase != "candidate" || observation.DurationMS < 0 {
			t.Fatal("invalid observed timing", observation)
		}
	}
}

func TestCheckTimingHistoryRetainsNewestBoundedObservations(t *testing.T) {
	timings := Timings{}
	for i := range maximumCheckTimings + 5 {
		timings.addCheckTiming(CheckTiming{Attempt: i})
	}
	if len(timings.CheckOperations) != maximumCheckTimings || timings.DroppedCheckOperations != 5 || timings.CheckOperations[0].Attempt != 5 {
		t.Fatal("check operation history did not retain bounded recent evidence")
	}
	if (Timings{Version: 1}).Recorded() == nil || (Timings{Version: 2}).Recorded() == nil || (Timings{}).Recorded() != nil {
		t.Fatal("timing history compatibility changed")
	}
}

type timedControllerChecker struct {
	supplementaryChecker
	observer func(CheckTiming)
}

func (checker *timedControllerChecker) WithTimingObserver(observer func(CheckTiming)) Checker {
	copy := *checker
	copy.observer = observer
	return &copy
}

func (checker *timedControllerChecker) Check(context.Context, string, [][]string, io.Writer) error {
	checker.observer(CheckTiming{Kind: "command", Phase: "candidate", Command: 1, DurationMS: 17})
	return nil
}

type delayedChecksPublisher struct{ fakePublisher }

func (publisher *delayedChecksPublisher) Checks(ctx context.Context, plan Plan, publication Publication) (CIResult, error) {
	time.Sleep(15 * time.Millisecond)
	return publisher.fakePublisher.Checks(ctx, plan, publication)
}

func TestControllerRecordsQueryTimeAndCommandTimings(t *testing.T) {
	directory, journal := testRun(t)
	provider := &fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}
	checker := &timedControllerChecker{}
	publisher := &delayedChecksPublisher{fakePublisher: fakePublisher{statuses: []string{"pending", "passed"}}}
	runner := fakeRunner(provider, &checker.fakeChecker, &publisher.fakePublisher, &fakeRepository{})
	runner.Checker, runner.Publisher = checker, publisher
	runner.PollInterval = time.Millisecond
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Timings.CIQueryMS < 30 || loaded.Timings.CIWallMS < loaded.Timings.CIQueryMS+loaded.Timings.CIWaitMS-1 || loaded.Timings.Version != 2 || len(loaded.Timings.CheckOperations) != 1 || loaded.Timings.CheckOperations[0].Attempt != 1 {
		t.Fatal("observed wall/query/command timing was lost", loaded.Timings)
	}
}

func TestLegacyTimingHistoriesRemainExplicitlyPartial(t *testing.T) {
	for _, version := range []int{0, 1} {
		directory, journal := testRun(t)
		journal.Publication = Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: testBase, HeadSHA: testHead}
		journal.State = "ci"
		journal.Attempt = 1
		journal.Timings.Version = version
		provider := &fakeProvider{outcomes: []Outcome{testOutcome("reviewed")}}
		runner := fakeRunner(provider, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
		if err := runner.Run(context.Background(), directory, journal, ""); err != nil {
			t.Fatal(err)
		}
		if !journal.Timings.Partial || journal.Timings.Version != 2 {
			t.Fatal("older activity presented as complete timing history", version)
		}
	}
	// A newly prepared journal has no earlier controller intervals to omit.
	directory, journal := testRun(t)
	runner := fakeRunner(&fakeProvider{outcomes: []Outcome{testOutcome("implemented"), testOutcome("reviewed")}}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{})
	if err := runner.Run(context.Background(), directory, journal, ""); err != nil || journal.Timings.Partial {
		t.Fatal("new run timing history marked partial", err)
	}
}
