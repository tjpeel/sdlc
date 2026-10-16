package workseries

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func declaredFixture(t *testing.T) (string, string, Plan) {
	t.Helper()
	root, dir, _, _ := amendmentFixture(t)
	if err := os.Remove(filepath.Join(dir, "journal.json")); err != nil {
		t.Fatal(err)
	}
	tickets := filepath.Join(root, ".sdlc", "work", "Example", "tickets")
	docs := map[string]string{"01-a.md": "# First\n**Blocked by:** None\n\n[future boundary](02-b.md)\n", "02-b.md": "# Second\n**Blocked by:** [first](01-a.md)\n", "03-c.md": "# Third\n**Blocked by:** None (can start immediately)\n"}
	for name, text := range docs {
		if err := os.WriteFile(filepath.Join(tickets, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Discover(context.Background(), root, "Example", "main")
	if err != nil {
		t.Fatal(err)
	}
	return root, dir, p
}
func TestDeclaredDependencyHandoffPreservesContextAndPreview(t *testing.T) {
	root, _, p := declaredFixture(t)
	mismatch, err := DeclaredDependencyMismatches(context.Background(), p)
	if err != nil || len(mismatch) != 1 || mismatch[0].Ticket != "02-b.md" {
		t.Fatal(mismatch, err)
	}
	if ValidateDeclaredDependencies(context.Background(), p, "") == nil {
		t.Fatal("missing scheduling prerequisite was admitted")
	}
	if err = ValidateDeclaredDependencies(context.Background(), p, "01-a.md"); err != nil {
		t.Fatal("unrelated boundary link became a prerequisite", err)
	}
	path := filepath.Join(root, ".sdlc", "work", "Example", "plan.json")
	preview, err := ImportDeclaredDependencies(context.Background(), root, "Example", "main", true)
	if err != nil || !preview.Changed {
		t.Fatal(preview, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote a plan")
	}
	if _, err = ImportDeclaredDependencies(context.Background(), root, "Example", "main", false); err != nil {
		t.Fatal(err)
	}
	actual, err := Discover(context.Background(), root, "Example", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Tickets[0].DependsOn) != 0 || !reflect.DeepEqual(actual.Tickets[1].DependsOn, []string{"01-a.md"}) || len(actual.Tickets[2].DependsOn) != 0 {
		t.Fatal(actual)
	}
	if err = ValidateDeclaredDependencies(context.Background(), actual, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	repeat, err := ImportDeclaredDependencies(context.Background(), root, "Example", "main", false)
	if err != nil || repeat.Changed {
		t.Fatal(repeat, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent import changed metadata")
	}
	// Opposite mismatch: prose declares independence while graph retains an edge.
	os.WriteFile(filepath.Join(root, ".sdlc/work/Example/tickets/02-b.md"), []byte("# Second\n**Blocked by:** None\n"), 0600)
	mismatch, err = DeclaredDependencyMismatches(context.Background(), actual)
	if err != nil || len(mismatch) != 1 || len(mismatch[0].Declared) != 0 {
		t.Fatal(mismatch, err)
	}
}
func TestDependencyImportRefusesFrozenFeaturesAndInvalidGraph(t *testing.T) {
	for _, scenario := range []string{"saved", "cycle", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			root, dir, p := declaredFixture(t)
			switch scenario {
			case "saved":
				s := State{Version: 1, Plan: p, Results: map[string]Result{}}
				if err := Save(dir, &s); err != nil {
					t.Fatal(err)
				}
			case "cycle":
				os.WriteFile(filepath.Join(root, ".sdlc/work/Example/tickets/01-a.md"), []byte("# First\n**Blocked by:** [second](02-b.md)\n"), 0600)
			case "unknown":
				os.WriteFile(filepath.Join(root, ".sdlc/work/Example/tickets/02-b.md"), []byte("# Second\n**Blocked by:** [missing](99-absent.md)\n"), 0600)
			}
			for _, dry := range []bool{true, false} {
				if _, err := ImportDeclaredDependencies(context.Background(), root, "Example", "main", dry); err == nil {
					t.Fatal("invalid or frozen handoff accepted", scenario)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".sdlc/work/Example/plan.json")); !os.IsNotExist(err) {
				t.Fatal("refused handoff wrote plan")
			}
		})
	}
}
func TestCompletedMetadataDoesNotEraseHistoricalPrerequisite(t *testing.T) {
	root, _, p := declaredFixture(t)
	p.Tickets[1].DependsOn = []string{"01-a.md"}
	path := filepath.Join(root, ".sdlc/work/Example/tickets/02-b.md")
	os.WriteFile(path, []byte("# Done\n**Status:** completed\n**Blocked by:** None (completed; merged PR: https://github.com/example/project/pull/1)\n"), 0600)
	mismatches, err := DeclaredDependencyMismatches(context.Background(), p)
	if err != nil || len(mismatches) != 0 {
		t.Fatal(mismatches, err)
	}
	if !strings.Contains(string(mustReadDeclared(t, path)), "completed") {
		t.Fatal("read-only check changed metadata")
	}
}
func mustReadDeclared(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSelectedTicketDoesNotParseUnselectedReadiness(t *testing.T) {
	root, _, p := declaredFixture(t)
	path := filepath.Join(root, ".sdlc/work/Example/tickets/03-c.md")
	if err := os.WriteFile(path, []byte("# Future\n**Blocked by:** To be determined\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaredDependencies(context.Background(), p, "01-a.md"); err != nil {
		t.Fatal("independent selected ticket was blocked by unfinished sibling", err)
	}
	if err := ValidateDeclaredDependencies(context.Background(), p, ""); err == nil {
		t.Fatal("whole feature ignored unresolved planning metadata")
	}
}
