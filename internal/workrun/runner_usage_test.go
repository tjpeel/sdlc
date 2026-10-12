package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
)

type usageProvider struct {
	quota     bool
	directory string
	input     int
	err       error
	cancel    context.CancelFunc
	t         *testing.T
}

func (p *usageProvider) Status(context.Context, string) (string, error) { return "stored", nil }
func (p *usageProvider) Execute(_ context.Context, s Session, out, _ io.Writer) (SessionResult, error) {
	// Persistence starts before any native request and does not depend on the
	// ephemeral dashboard tracker receiving the stream.
	summary, err := runusage.LoadSummary(p.directory, "run-one", 0)
	if err != nil || summary.Pending != 1 {
		p.t.Fatalf("attempt start not durable: %+v %v", summary, err)
	}
	if p.quota {
		fmt.Fprintln(out, quotaBoundary("before", 80))
		saved, err := runusage.LoadSummary(p.directory, "run-one", 0)
		if err != nil || saved.Subscriptions["codex"] == nil || saved.Subscriptions["codex"].Before == nil || saved.Subscriptions["codex"].After != nil {
			p.t.Fatalf("baseline not durable before model work: %+v %v", saved.Subscriptions, err)
		}
	}
	fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
	fmt.Fprintf(out, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":%d,\"cached_input_tokens\":%d,\"output_tokens\":%d}}", p.input, p.input/2, p.input/10)
	if p.quota {
		fmt.Fprintln(out)
		fmt.Fprintln(out, quotaBoundary("after", 75))
	}
	if p.cancel != nil {
		p.cancel()
	}
	return SessionResult{SessionID: testNative, ReportedModel: s.Model.Name}, p.err
}

func quotaBoundary(phase string, remaining int) string {
	return fmt.Sprintf(`{"type":"sdlc.subscription_snapshot","provider":"codex","phase":%q,"snapshot":{"source":"codex.app_server.rate_limits","observed_at":%q,"status":"observed","windows":{"codex/primary":{"remaining_percent":%d,"resets_at":4102444800,"window_duration_mins":300}}}}`, phase, time.Now().UTC().Format(time.RFC3339Nano), remaining)
}

func TestQuotaBaselineAndFailureEndRemainDurable(t *testing.T) {
	directory, journal := testRun(t)
	provider := &usageProvider{directory: directory, input: 100, quota: true, t: t, err: errors.New("offline native failure")}
	runner := Runner{Provider: provider, Output: io.Discard}
	if _, err := runner.session(context.Background(), directory, journal, "implementation", journal.Workspace, ""); !errors.Is(err, provider.err) {
		t.Fatal(err)
	}
	summary, err := runusage.LoadSummary(directory, journal.ID, journal.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	change := summary.Subscriptions["codex"].Windows["codex/primary"]
	if summary.Failed != 1 || change.UsedPercentagePoints == nil || *change.UsedPercentagePoints != 5 {
		t.Fatalf("failure erased capacity snapshots: %+v", summary)
	}
}

func TestSessionRetainsUsageAfterResumeFailureAndCancellation(t *testing.T) {
	directory, journal := testRun(t)
	provider := &usageProvider{directory: directory, input: 100, t: t}
	runner := Runner{Provider: provider, Output: io.Discard}
	if _, err := runner.session(context.Background(), directory, journal, "implementation", journal.Workspace, ""); err != nil {
		t.Fatal(err)
	}
	provider.input = 150
	provider.err = errors.New("offline provider failure")
	if _, err := runner.session(context.Background(), directory, journal, "implementation", journal.Workspace, testNative); !errors.Is(err, provider.err) {
		t.Fatal(err)
	}
	summary, err := runusage.LoadSummary(directory, journal.ID, journal.Attempt)
	if err != nil || summary.Completed != 1 || summary.Failed != 1 || summary.Missing != 0 {
		t.Fatalf("final records not retained: %+v %v", summary, err)
	}
	for _, group := range summary.Groups {
		if group.EventTiming == nil || group.EventTiming.RecordedAttempts != 1 || group.EventTiming.Events != 2 {
			t.Fatalf("session reception timing not retained: %+v", group.EventTiming)
		}
		if group.Role == "repair" && (group.Tokens.InputTokens == nil || *group.Tokens.InputTokens != 50) {
			t.Fatalf("resume cumulative counters counted twice: %+v", group)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider.cancel = cancel
	provider.input = 180
	if _, err := runner.session(ctx, directory, journal, "implementation", journal.Workspace, testNative); !errors.Is(err, provider.err) {
		t.Fatal(err)
	}
	summary, err = runusage.LoadSummary(directory, journal.ID, journal.Attempt)
	if err != nil || summary.Aborted != 1 || summary.Pending != 0 || summary.Attempts != 3 {
		t.Fatalf("cancellation lost its final record: %+v %v", summary, err)
	}
}

func TestMetricsPreserveSupportedCodexEfforts(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			directory, journal := testRun(t)
			journal.Plan.Roles.Implementation.Effort = effort
			if err := ValidateModel(journal.Plan.Roles.Implementation); err != nil {
				t.Fatal(err)
			}
			provider := &usageProvider{directory: directory, input: 100, t: t}
			runner := Runner{Provider: provider, Output: io.Discard}
			if _, err := runner.session(context.Background(), directory, journal, "implementation", journal.Workspace, ""); err != nil {
				t.Fatalf("supported job blocked by numeric recording: %v", err)
			}
		})
	}
}
