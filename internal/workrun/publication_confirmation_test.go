package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPublicationConfirmationRetriesOnlyRecoverableObservations(t *testing.T) {
	for _, scenario := range []string{"previous-head", "transport", "exhausted", "unrelated-head", "changed-base", "changed-pr", "malformed", "adoption-identity", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			plan := Plan{Repository: "example/project", BaseSHA: testBase}
			previous := Publication{Number: 1, HeadSHA: testHead}
			calls, waits := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			publisher := GitHubPublisher{Command: func(context.Context, string, ...string) ([]byte, error) {
				calls++
				result := publishedPRObservation{Number: 1, URL: "https://github.com/example/project/pull/1", Base: testBase, Head: testSigned}
				switch scenario {
				case "transport":
					if calls < 3 {
						return nil, fmt.Errorf("temporary read failure")
					}
				case "previous-head", "cancelled":
					if calls < 3 {
						result.Head = testHead
					}
				case "exhausted":
					result.Head = testHead
				case "unrelated-head":
					result.Head = testTree
				case "changed-base":
					result.Base = testTree
				case "changed-pr":
					result.Number = 2
				case "malformed":
					return []byte("{"), nil
				case "adoption-identity":
					result.State = "CLOSED"
					result.Head = testHead
				}
				return json.Marshal(result)
			}}
			if scenario == "adoption-identity" {
				plan.Adoption = &PRAdoption{Number: 1, URL: "https://github.com/example/project/pull/1"}
			}
			wait := func(ctx context.Context, delay time.Duration) error {
				want := time.Second * time.Duration(1<<waits)
				if delay != want {
					t.Fatalf("delay %v, want %v", delay, want)
				}
				waits++
				if scenario == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			result, err := publisher.confirmPublishedPR(ctx, plan, "1", "", testSigned, previous, wait)
			switch scenario {
			case "transport", "previous-head":
				if err != nil || result.Head != testSigned || calls != 3 || waits != 2 {
					t.Fatalf("recovery: %+v %v calls=%d waits=%d", result, err, calls, waits)
				}
			case "exhausted":
				if err == nil || calls != 5 || waits != 4 {
					t.Fatalf("unbounded retry: %v %d %d", err, calls, waits)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("cancel: %v calls=%d", err, calls)
				}
			default:
				if err == nil || calls != 1 || waits != 0 {
					t.Fatalf("unsafe observation retried: %v calls=%d waits=%d", err, calls, waits)
				}
			}
		})
	}
}
