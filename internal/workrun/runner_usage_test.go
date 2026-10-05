package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

type usageProvider struct {
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
	fmt.Fprintf(out, "{\"type\":\"thread.started\",\"thread_id\":%q}\n", testNative)
	fmt.Fprintf(out, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":%d,\"cached_input_tokens\":%d,\"output_tokens\":%d}}", p.input, p.input/2, p.input/10)
	if p.cancel != nil {
		p.cancel()
	}
	return SessionResult{SessionID: testNative, ReportedModel: s.Model.Name}, p.err
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
