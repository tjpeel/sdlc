package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestGitHubChecksWaitsForAllChecksBeforeCollectingFinalFailurePacket(t *testing.T) {
	fixture := genericCIFailureFixture()
	second := map[string]any{"id": 42, "name": "second suite", "head_sha": testHead, "status": "completed", "conclusion": "failure", "html_url": "https://ci.example.invalid/result/42", "details_url": "https://ci.example.invalid/result/42", "app": map[string]any{"id": 90, "slug": "example-checks"}, "output": map[string]any{"annotations_count": 0, "title": "Second failure", "text": "AnotherUnknownFailure: expected two items."}}
	fixture.responses["repos/example/project/check-runs/42"] = second
	index := fixture.responses["repos/example/project/commits/"+testHead+"/check-runs?per_page=100&page=1"].(map[string]any)
	index["total_count"] = 2
	index["check_runs"] = append(index["check_runs"].([]any), second)
	fixture.checks = append(fixture.checks, ciRequestedCheck{Name: "second suite", Bucket: "pending", State: "IN_PROGRESS", Link: "https://ci.example.invalid/result/42"})
	publisher := GitHubPublisher{Command: fixture.command}
	plan := Plan{Repository: "example/project"}
	for _, reverse := range []bool{false, true} {
		if reverse {
			fixture.checks[0], fixture.checks[1] = fixture.checks[1], fixture.checks[0]
		}
		fixture.calls = nil
		result, err := publisher.Checks(context.Background(), plan, annotationPublication())
		if err != nil || result.Status != "pending" || result.FailureContext != nil || len(result.Diagnostics) != 0 {
			t.Fatal("partial failure granted repair before remaining CI completed", result, err)
		}
		for _, call := range fixture.calls {
			if strings.Contains(call, "api ") {
				t.Fatal("pending CI queried failure details", fixture.calls)
			}
		}
	}
	for i := range fixture.checks {
		fixture.checks[i].Bucket = "fail"
		fixture.checks[i].State = "COMPLETED"
	}
	result, err := publisher.Checks(context.Background(), plan, annotationPublication())
	if err != nil || result.Status != "failed" || result.FailureContext == nil || len(result.FailureContext.Checks) != 2 {
		t.Fatal("terminal packet omitted a completed failure", result, err)
	}
	data, _ := json.Marshal(result.FailureContext)
	if !strings.Contains(string(data), "UnknownProbeFailure") || !strings.Contains(string(data), "AnotherUnknownFailure") {
		t.Fatal("failure packet did not collect all final results", string(data))
	}
}

func TestGitHubIdentityUnavailableIsDistinctFromActualMismatch(t *testing.T) {
	for _, kind := range []string{"unavailable", "invalid json", "missing identity", "mismatch", "match"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			publisher := GitHubPublisher{Frozen: &PublicationIdentity{GitHubID: 17, GitHubLogin: "example-user"}, Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name != "gh" || strings.Join(args, " ") != "api user" {
					t.Fatal("unexpected authentication retry")
				}
				switch kind {
				case "unavailable":
					return nil, errors.New("private token and network diagnostics")
				case "invalid json":
					return []byte("private undecodable payload"), nil
				case "missing identity":
					return []byte(`{}`), nil
				case "mismatch":
					return []byte(`{"id":18,"login":"other-account"}`), nil
				default:
					return []byte(`{"id":17,"login":"example-user"}`), nil
				}
			}}
			err := publisher.verifyIdentity(context.Background())
			if calls != 1 {
				t.Fatal("identity guard retried automatically")
			}
			if kind == "match" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("identity guard allowed unsupported identity")
			}
			if kind == "mismatch" {
				if !strings.Contains(err.Error(), "differs from frozen publication identity") || strings.Contains(err.Error(), "unavailable") {
					t.Fatal("actual mismatch lost its refusal", err)
				}
			} else if !strings.Contains(err.Error(), "lookup unavailable") || !strings.Contains(err.Error(), "verify the stored GitHub profile, then resume") {
				t.Fatal("unavailable lookup lacked safe next action", err)
			}
			for _, private := range []string{"private token", "other-account", "undecodable"} {
				if strings.Contains(fmt.Sprint(err), private) {
					t.Fatal("raw account diagnostics leaked", err)
				}
			}
		})
	}
}
