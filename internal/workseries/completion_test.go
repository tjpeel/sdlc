package workseries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tjpeel/sdlc/internal/filelock"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func completionFixture(t *testing.T) (string, string, State, MergedTicket, string) {
	t.Helper()
	root, dir, s, _ := amendmentFixture(t)
	delete(s.Results, "02-b.md")
	s.Results["01-a.md"] = Result{State: "blocked"}
	path := filepath.Join(root, ".sdlc", "work", "Example", "tickets", "01-a.md")
	text := []byte("# Requirement\n\n**Status:** ready-for-agent\n**Blocked by:** None (can start immediately)\n\nKeep the substantive requirement.\n")
	if err := os.WriteFile(path, text, 0644); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"Repository": "example/project", "ImageID": "unchanged-runtime", "InputHashes": map[string]string{".sdlc/work/Example/tickets/01-a.md": amendmentHash(text)}, "Roles": map[string]string{"review": "unchanged"}}
	s.Settings, _ = json.Marshal(settings)
	if err := Save(dir, &s); err != nil {
		t.Fatal(err)
	}
	e := MergedTicket{Repository: "example/project", Number: 17, URL: "https://github.com/example/project/pull/17", Branch: "feature", Base: "main", IntegrationSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), MergeSHA: strings.Repeat("c", 40), MergedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	return root, dir, s, e, path
}
func TestCompletionUpdatesEvidenceMetadataAndOnlyMatchingFrozenHash(t *testing.T) {
	root, dir, old, e, path := completionFixture(t)
	original, _ := os.ReadFile(path)
	journal, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	p, err := CompleteTicket(context.Background(), root, "Example", "01-a.md", e, true)
	if err != nil || !p.Changed || !p.DryRun {
		t.Fatal(p, err)
	}
	afterDry, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(journal, afterDry) {
		t.Fatal("dry-run changed journal")
	}
	if _, err = CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false); err != nil {
		t.Fatal(err)
	}
	next, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := next.Results["01-a.md"]
	if result.State != "merged" || result.MergeSHA != e.MergeSHA || result.HeadSHA != e.HeadSHA || result.BaseSHA != e.IntegrationSHA || result.RunID != "" {
		t.Fatal(result)
	}
	text, _ := os.ReadFile(path)
	status, blocked := TicketMetadata(text)
	if status != "completed" || !strings.Contains(blocked, e.URL) || !bytes.Contains(text, []byte("Keep the substantive requirement.")) {
		t.Fatal(string(text))
	}
	var settings map[string]json.RawMessage
	json.Unmarshal(next.Settings, &settings)
	var hashes map[string]string
	json.Unmarshal(settings["InputHashes"], &hashes)
	if hashes[".sdlc/work/Example/tickets/01-a.md"] != amendmentHash(text) {
		t.Fatal(hashes)
	}
	if string(settings["ImageID"]) != `"unchanged-runtime"` || string(settings["Roles"]) != `{"review":"unchanged"}` {
		t.Fatal(string(next.Settings))
	}
	if len(next.TicketCompletions) != 1 || !reflect.DeepEqual(next.TicketCompletions[0].Previous, old.Results["01-a.md"]) || next.TicketCompletions[0].Before != amendmentHash(original) {
		t.Fatal(next.TicketCompletions)
	}
	p, err = CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false)
	if err != nil || p.Changed {
		t.Fatal("repeat is not idempotent", p, err)
	}
}
func TestCompletionRecoversBothInterruptedFileWrites(t *testing.T) {
	for _, stage := range []string{"ticket", "journal"} {
		t.Run(stage, func(t *testing.T) {
			root, dir, _, e, path := completionFixture(t)
			originalWrite := completionWrite
			originalTicketWrite := completionTicketWrite
			defer func() { completionWrite = originalWrite; completionTicketWrite = originalTicketWrite }()
			fail := func(string, []byte) error { return errors.New("offline interrupted write") }
			if stage == "ticket" {
				completionTicketWrite = fail
			} else {
				completionWrite = fail
			}
			if _, err := CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false); err == nil {
				t.Fatal("expected interruption")
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("normal controller must not load partial completion")
			}
			if CheckDependencyAmendment(root, "Example") == nil {
				t.Fatal("direct preparation must be held")
			}
			completionWrite = originalWrite
			completionTicketWrite = originalTicketWrite
			// The target integration branch may have advanced during interruption.
			e.IntegrationSHA = strings.Repeat("d", 40)
			p, err := CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false)
			if err != nil || !p.Recovery {
				t.Fatal(p, err)
			}
			s, err := Load(dir)
			if err != nil || s.Results["01-a.md"].State != "merged" {
				t.Fatal(err)
			}
			text, _ := os.ReadFile(path)
			status, _ := TicketMetadata(text)
			if status != "completed" {
				t.Fatal(status)
			}
		})
	}
}
func TestCompletionRefusesConflictingOrUnfrozenWorkWithoutWriting(t *testing.T) {
	for _, scenario := range []string{"repository", "base", "hash", "retained-child", "feature-lock", "symlink", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			root, dir, s, e, path := completionFixture(t)
			ticket := "01-a.md"
			switch scenario {
			case "repository":
				e.Repository = "other/project"
				e.URL = "https://github.com/other/project/pull/17"
			case "base":
				e.Base = "other"
			case "hash":
				os.WriteFile(path, []byte("Unexpected substantive change"), 0600)
			case "retained-child":
				s.Results["03-c.md"] = Result{State: "blocked", RunID: strings.Repeat("3", 24)}
				if err := Save(dir, &s); err != nil {
					t.Fatal(err)
				}
			case "feature-lock":
				l, err := filelock.Acquire(filepath.Join(dir, "series.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			case "symlink":
				target := filepath.Join(root, "outside.md")
				os.WriteFile(target, []byte("outside"), 0600)
				os.Remove(path)
				os.Symlink(target, path)
			case "unknown":
				ticket = "99-unknown.md"
				os.WriteFile(filepath.Join(filepath.Dir(path), ticket), []byte("unknown"), 0600)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			if _, err := CompleteTicket(context.Background(), root, "Example", ticket, e, false); err == nil {
				t.Fatal("unsafe completion accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("rejected operation changed checkpoint")
			}
		})
	}
}
func TestCompletedTicketPreservesLineEndingsAndDoesNotEditOtherStatusMentions(t *testing.T) {
	before := []byte("# Ticket\r\n**Status:** ready-for-agent\r\n**Blocked by:** None\r\n\r\nExample **Status:** keep\r\n")
	after := completedTicketText(before, "https://github.com/example/project/pull/17")
	if bytes.Count(after, []byte("\n")) != bytes.Count(after, []byte("\r\n")) || !bytes.Contains(after, []byte("Example **Status:** keep")) {
		t.Fatal(string(after))
	}
}

func TestRepeatedCompletionDoesNotHideFrozenHashDrift(t *testing.T) {
	root, dir, _, e, _ := completionFixture(t)
	if _, err := CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	json.Unmarshal(s.Settings, &settings)
	settings["InputHashes"] = json.RawMessage(`{".sdlc/work/Example/tickets/01-a.md":"incorrect"}`)
	s.Settings, _ = json.Marshal(settings)
	if err = Save(dir, &s); err != nil {
		t.Fatal(err)
	}
	if _, err = CompleteTicket(context.Background(), root, "Example", "01-a.md", e, false); err == nil {
		t.Fatal("idempotent completion hid frozen input drift")
	}
}
