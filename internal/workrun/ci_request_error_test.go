package workrun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func TestCIJobLogRetriesOnlyIsolatedTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not ready", &ciRequestError{Status: 404}, 3},
		{"server", &ciRequestError{Status: 500}, 3},
		{"gateway", &ciRequestError{Status: 502}, 3},
		{"unavailable", &ciRequestError{Status: 503}, 3},
		{"timeout", &ciRequestError{Status: 504}, 3},
		{"network", &ciRequestError{Kind: "temporary network error"}, 3},
		{"auth", &ciRequestError{Status: 401}, 1},
		{"permission", &ciRequestError{Status: 403}, 1},
		{"rate limit", &ciRequestError{Status: 429}, 1},
		{"client", &ciRequestError{Kind: "unsupported GitHub CLI flag"}, 1},
		{"cleanup", errors.Join(&ciRequestError{Status: 404}, errors.New("cleanup failed")), 1},
		{"intake", &ciResponseSizeError{Limit: maxActionsJobLogBytes}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, waits := 0, 0
			data, err := readCIJobLog(context.Background(), func() ([]byte, error) {
				calls++
				if calls < 3 {
					return nil, tc.err
				}
				return []byte("log"), nil
			}, func(context.Context, time.Duration) error { waits++; return nil })
			if calls != tc.want || waits != tc.want-1 || (tc.want == 3 && (err != nil || string(data) != "log")) || (tc.want == 1 && err == nil) {
				t.Fatal("wrong retry eligibility", calls, waits, err)
			}
		})
	}
}

type ciDiagnosticTestRunner struct{}

func (ciDiagnosticTestRunner) Run(context.Context, io.Reader, io.Writer, ...string) error {
	return errors.New("missing diagnostic route")
}
func (ciDiagnosticTestRunner) RunDiagnostic(_ context.Context, _ io.Reader, _ io.Writer, diagnostic io.Writer, _ ...string) error {
	_, _ = io.WriteString(diagnostic, "gh: service unavailable (HTTP 503) https://example.invalid?sig=private-test-value")
	return errors.New("exit status 1")
}
func TestFrozenCITransportPreservesSafeRequestCategoryAndCleanupHold(t *testing.T) {
	docker := &frozenCIControllerDocker{pairingRaceDocker: pairingRaceDocker{image: "sha256:" + strings.Repeat("a", 64), volume: "sdlc-github-auth-" + strings.Repeat("b", 32) + "-github"}}
	p := DockerPublisher{Runtime: runtimeimage.Manager{Docker: docker}, ImageID: docker.image, Runner: ciDiagnosticTestRunner{}}
	args := []string{"api", "--hostname", "github.com", "--allow-escape-sequences", "repos/example/project/actions/jobs/99/logs"}
	_, err := p.ciControllerCommand(context.Background(), docker.volume, args)
	request, ok := err.(*ciRequestError)
	if !ok || request.Status != 503 || strings.Contains(err.Error(), "private-test-value") {
		t.Fatal("runtime error category lost or unsafe", err)
	}
	docker.cleanupFailure = true
	_, err = p.ciControllerCommand(context.Background(), docker.volume, args)
	calls := 0
	_, retryErr := readCIJobLog(context.Background(), func() ([]byte, error) { calls++; return nil, err }, func(context.Context, time.Duration) error { t.Fatal("cleanup failure retried"); return nil })
	if retryErr == nil || calls != 1 {
		t.Fatal("cleanup hold bypassed", calls, retryErr)
	}
}

func TestCILogAvailabilityRetryRetainsVerifiedFailureContext(t *testing.T) {
	fixture := actionsCIFailureFixture()
	calls := 0
	command := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[len(args)-1], "/logs") {
			calls++
			if calls == 1 {
				return nil, &ciRequestError{Status: 404}
			}
		}
		return fixture.command(ctx, name, args...)
	}
	result, err := (GitHubPublisher{Command: command}).Checks(context.Background(), Plan{Repository: "example/project"}, Publication{Number: 1, HeadSHA: testHead, BaseSHA: testBase})
	if err != nil || calls != 2 || result.FailureContext == nil || len(result.FailureContext.Checks) != 1 {
		t.Fatal("temporary log hold prevented verified CI feedback", calls, err, result)
	}
}

func TestCIJobLogRetryBoundAndCancellation(t *testing.T) {
	calls := 0
	delays := []time.Duration{}
	_, err := readCIJobLog(context.Background(), func() ([]byte, error) { calls++; return nil, &ciRequestError{Status: 404} }, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if err == nil || calls != 3 || len(delays) != 2 || delays[0] != 2*time.Second || delays[1] != 5*time.Second {
		t.Fatal("retry not bounded", calls, delays, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls = 0
	_, err = readCIJobLog(ctx, func() ([]byte, error) { calls++; return nil, &ciRequestError{Status: 503} }, func(context.Context, time.Duration) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("retry ignored cancellation", calls, err)
	}
}

func TestCIRequestErrorRetainsCategoryWithoutPrivateText(t *testing.T) {
	raw := "gh: service unavailable (HTTP 503) https://example.invalid/log?sig=private-signed-value ghp_disposableToken"
	err := classifyCIRequest(errors.New("exit status 1"), raw)
	request, ok := err.(*ciRequestError)
	if !ok || request.Status != 503 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "ghp_") || strings.Contains(err.Error(), "https:") {
		t.Fatal("unsafe or missing diagnostic", err)
	}
	var diagnostic ciDiagnosticOutput
	input := []byte(strings.Repeat("x", 10000))
	written, writeErr := diagnostic.Write(input)
	if writeErr != nil || written != len(input) || diagnostic.buffer.Len() != 4096 {
		t.Fatal("unbounded diagnostic intake", written, diagnostic.buffer.Len(), writeErr)
	}
}
