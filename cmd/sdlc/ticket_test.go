package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tjpeel/sdlc/internal/workseries"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedTicketMergeChecksRemoteStateAndAncestry(t *testing.T) {
	original := ticketGH
	defer func() { ticketGH = original }()
	for _, scenario := range []string{"merged", "open", "fork", "diverged", "network"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			ticketGH = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls++
				if scenario == "network" {
					return nil, errors.New("offline unavailable")
				}
				if args[0] == "pr" {
					state := "MERGED"
					if scenario == "open" {
						state = "OPEN"
					}
					return json.Marshal(map[string]any{"number": 17, "url": "https://github.com/example/project/pull/17", "state": state, "baseRefName": "main", "headRefName": "feature", "headRefOid": strings.Repeat("b", 40), "isCrossRepository": scenario == "fork", "mergedAt": "2026-01-02T03:04:05Z", "mergeCommit": map[string]string{"oid": strings.Repeat("c", 40)}})
				}
				if strings.Contains(args[1], "/git/ref/") {
					return []byte(`{"object":{"sha":"` + strings.Repeat("a", 40) + `"}}`), nil
				}
				if !strings.Contains(args[1], strings.Repeat("c", 40)+"..."+strings.Repeat("a", 40)) {
					t.Fatal("comparison does not pin exact commits", args)
				}
				status := "ahead"
				if scenario == "diverged" {
					status = "diverged"
				}
				return json.Marshal(map[string]string{"status": status})
			}
			e, err := verifiedTicketMerge(context.Background(), "/unused", "example/project", 17)
			if scenario == "merged" {
				if err != nil || calls != 3 || e.IntegrationSHA != strings.Repeat("a", 40) {
					t.Fatal(e, err, calls)
				}
			} else if err == nil {
				t.Fatal("unverified completion accepted")
			}
		})
	}
}
func TestTicketCommandRejectsAmbiguousFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"status"}, {"complete", "--reference", "EXAMPLE", "--ticket", "01-a.md"}, {"status", "--reference", "EXAMPLE", "--pr", "https://github.com/example/project/pull/1"}, {"status", "--reference", "EXAMPLE", "--dry-run"}} {
		if err := ticketCommand(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal(args)
		}
	}
}

func TestTicketStatusUsesJournalDependenciesAndCanReadBeforeFeatureStarts(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tickets := filepath.Join(root, ".sdlc", "work", "EXAMPLE", "tickets")
	if err := os.MkdirAll(tickets, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"01-a.md", "02-b.md"} {
		if err := os.WriteFile(filepath.Join(tickets, file), []byte("# Requirement\n**Status:** ready-for-agent\n**Blocked by:** None\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	args := []string{"status", "--reference", "EXAMPLE", "--run-root", root, "--json"}
	if err := ticketCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	var rows []ticketStatus
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0].State != "pending" {
		t.Fatal(out.String(), err)
	}
	planData := []byte(`{"version":1,"tickets":{"02-b.md":{"depends_on":["01-a.md"]}}}`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(tickets), "plan.json"), planData, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ticketCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || rows[1].State != "waiting_for_dependencies" || len(rows[1].BlockedBy) != 1 || !rows[1].MetadataStale {
		t.Fatal(out.String(), err)
	}
	dir, err := workseries.Directory(root, "EXAMPLE", true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workseries.Discover(context.Background(), root, "EXAMPLE", "main")
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: plan, Results: map[string]workseries.Result{"01-a.md": {State: "merged"}}}
	if err := workseries.Save(dir, &state); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ticketCommand(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || rows[0].State != "merged" || !rows[0].MetadataStale || len(rows[1].BlockedBy) != 0 {
		t.Fatal(out.String(), err)
	}
}
