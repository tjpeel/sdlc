package workrun

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Retain only fixed error categories, never URLs, response bodies or credentials.
type ciRequestError struct {
	Status int
	Kind   string
	cause  error
}

func (e *ciRequestError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("GitHub request failed (HTTP %d)", e.Status)
	}
	return "GitHub request failed (" + e.Kind + ")"
}
func (e *ciRequestError) Unwrap() error { return e.cause }

type ciDiagnosticOutput struct{ buffer bytes.Buffer }

func (b *ciDiagnosticOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}

var ciHTTPStatus = regexp.MustCompile(`(?i)\bHTTP[ /:]*([45][0-9]{2})\b`)

func classifyCIRequest(err error, diagnostic string) error {
	if err == nil {
		return nil
	}
	e := &ciRequestError{Kind: "command or transport error", cause: err}
	if match := ciHTTPStatus.FindStringSubmatch(diagnostic); match != nil {
		e.Status, _ = strconv.Atoi(match[1])
		return e
	}
	text := strings.ToLower(diagnostic)
	for _, marker := range []string{"connection reset", "i/o timeout", "tls handshake timeout", "temporary failure in name resolution", "no such host", "unexpected eof"} {
		if strings.Contains(text, marker) {
			e.Kind = "temporary network error"
			return e
		}
	}
	if strings.Contains(text, "unknown flag") {
		e.Kind = "unsupported GitHub CLI flag"
	}
	return e
}

// A retry is permitted only for an isolated job-log read. A joined cleanup error
// must not be mistaken for the underlying retryable request failure.
func readCIJobLog(ctx context.Context, fetch func() ([]byte, error), wait func(context.Context, time.Duration) error) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		data, err := fetch()
		if len(data) > maxActionsJobLogBytes {
			return nil, &ciResponseSizeError{Limit: maxActionsJobLogBytes}
		}
		if err == nil {
			return data, nil
		}
		request, isolated := err.(*ciRequestError)
		retryable := isolated && (request.Status == 404 || request.Status == 500 || request.Status == 502 || request.Status == 503 || request.Status == 504 || request.Kind == "temporary network error")
		if !retryable || attempt == 2 || ctx.Err() != nil {
			return nil, err
		}
		delay := 2 * time.Second
		if attempt == 1 {
			delay = 5 * time.Second
		}
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("job log retry exhausted")
}
func waitCIJobLog(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
