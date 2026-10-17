package workrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCIStatusRetriesTransientReadsOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    int
	}{
		{"network", &ciRequestError{Kind: "temporary network error"}, 3},
		{"server", &ciRequestError{Status: 500}, 3},
		{"gateway", &ciRequestError{Status: 502}, 3},
		{"unavailable", &ciRequestError{Status: 503}, 3},
		{"timeout", &ciRequestError{Status: 504}, 3},
		{"authentication", &ciRequestError{Status: 401}, 1},
		{"permissions", &ciRequestError{Status: 403}, 1},
		{"rate limit", &ciRequestError{Status: 429}, 1},
		{"not found", &ciRequestError{Status: 404}, 1},
		{"unsupported client", &ciRequestError{Kind: "unsupported GitHub CLI flag"}, 1},
		{"cleanup", errors.Join(&ciRequestError{Status: 503}, errors.New("cleanup failed")), 1},
		{"oversized", &ciResponseSizeError{Limit: 1024}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var delays []time.Duration
			data, err := readCIStatus(context.Background(), func() ([]byte, error) {
				calls++
				if calls < 3 {
					return nil, tc.failure
				}
				return []byte("checks"), nil
			}, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
			if calls != tc.want {
				t.Fatal("wrong number of reads", calls)
			}
			if tc.want == 3 {
				if err != nil || string(data) != "checks" || !reflect.DeepEqual(delays, []time.Duration{time.Second, 5 * time.Second}) {
					t.Fatal(data, err, delays)
				}
			} else if err == nil || len(delays) != 0 {
				t.Fatal("retried permanent error", err, delays)
			}
		})
	}
}

func TestCIStatusExhaustionAndCancellation(t *testing.T) {
	failure := &ciRequestError{Status: 503}
	calls := 0
	_, err := readCIStatus(context.Background(), func() ([]byte, error) { calls++; return nil, failure }, func(context.Context, time.Duration) error { return nil })
	if err != failure || calls != 3 {
		t.Fatal("lost final failure", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	_, err = readCIStatus(ctx, func() ([]byte, error) { calls++; return nil, failure }, func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("continued after cancellation", calls, err)
	}
}
