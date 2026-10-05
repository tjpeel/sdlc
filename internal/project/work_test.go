package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func workRepo(t *testing.T) string {
	t.Helper()
	root := repo(t)
	write(t, root, ".git/info/exclude", "# Existing local excludes\n/.sdlc/work/\n")
	return root
}

func workTicket(t *testing.T, root, reference, name string) {
	t.Helper()
	write(t, root, filepath.Join(".sdlc/work", reference, "tickets", name), "disposable ticket contents\n")
}

func workSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[relative] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInspectWorkNumericOrderAndNoWrites(t *testing.T) {
	root := workRepo(t)
	reference := "Example work (v2)"
	for _, name := range []string{"100-later.md", "10-middle.md", "02-earlier.md", "1-first.md", "20-A title (with CAPS).md"} {
		workTicket(t, root, reference, name)
	}
	// Ticket discovery must not inspect bodies, including invalid text or data
	// larger than the configuration reader accepts.
	write(t, root, filepath.Join(".sdlc/work", reference, "tickets", "02-earlier.md"), strings.Repeat("\x00\xff", maximumSize))
	workTicket(t, root, reference, "README.md")
	workTicket(t, root, reference, "03-not-a-ticket.txt")
	workTicket(t, root, "Other", "01-other.md")
	write(t, root, "nested/.keep", "")
	before := workSnapshot(t, root)
	result, err := InspectWork(context.Background(), filepath.Join(root, "nested"), reference)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for _, name := range []string{"1-first.md", "02-earlier.md", "10-middle.md", "20-A title (with CAPS).md", "100-later.md"} {
		want = append(want, filepath.ToSlash(filepath.Join(".sdlc/work", reference, "tickets", name)))
	}
	if result.Root != resolved || result.Reference != reference || !reflect.DeepEqual(result.Tickets, want) {
		t.Fatalf("unexpected work selection: %+v", result)
	}
	if after := workSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("work inspection changed the checkout or Git metadata")
	}
}

func TestInspectWorkInvalidReferenceBeforeInspection(t *testing.T) {
	root := t.TempDir()
	for _, reference := range []string{"", ".", "..", "../example", "example/nested", "example\\nested", "example\n", "example\x00", string([]byte{0xff})} {
		if _, err := InspectWork(context.Background(), root, reference); err == nil || !strings.Contains(err.Error(), "work reference") {
			t.Fatalf("invalid reference accepted or inspected: %q: %v", reference, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid work inspection created state")
	}
}

func TestInspectWorkRequiresExactReferenceSpelling(t *testing.T) {
	root := workRepo(t)
	workTicket(t, root, "Example", "01-example.md")
	result, err := InspectWork(context.Background(), root, "Example")
	if err != nil || result.Reference != "Example" || !reflect.DeepEqual(result.Tickets, []string{".sdlc/work/Example/tickets/01-example.md"}) {
		t.Fatalf("exact reference spelling failed: %+v: %v", result, err)
	}
	for _, reference := range []string{"example", "EXAMPLE"} {
		if _, err := InspectWork(context.Background(), root, reference); err == nil || !strings.Contains(err.Error(), "exactly") {
			t.Fatalf("wrong-case reference accepted: %q: %v", reference, err)
		}
	}
}

func TestInspectWorkColonReference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory names cannot contain a colon")
	}
	root := workRepo(t)
	reference := "Example: work"
	workTicket(t, root, reference, "01-example.md")
	result, err := InspectWork(context.Background(), root, reference)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reference != reference || !reflect.DeepEqual(result.Tickets, []string{".sdlc/work/Example: work/tickets/01-example.md"}) {
		t.Fatalf("literal colon reference was changed: %+v", result)
	}
}

func TestInspectWorkMissingInputs(t *testing.T) {
	for _, scenario := range []string{"no work", "no tickets directory", "empty tickets directory", "notes only"} {
		t.Run(scenario, func(t *testing.T) {
			root := workRepo(t)
			switch scenario {
			case "no tickets directory":
				write(t, root, ".sdlc/work/Example/specification.md", "disposable notes\n")
			case "empty tickets directory":
				if err := os.MkdirAll(filepath.Join(root, ".sdlc/work/Example/tickets"), 0700); err != nil {
					t.Fatal(err)
				}
			case "notes only":
				workTicket(t, root, "Example", "README.md")
			}
			before := workSnapshot(t, root)
			if _, err := InspectWork(context.Background(), root, "Example"); err == nil {
				t.Fatal("missing tickets accepted")
			}
			if after := workSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("failed work inspection wrote state")
			}
		})
	}
	if _, err := InspectWork(context.Background(), t.TempDir(), "Example"); err == nil {
		t.Fatal("non-repository accepted")
	}
}

func TestInspectWorkRejectsAmbiguousAndMalformedNumberedTickets(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		files []string
	}{
		{"duplicate", []string{"02-first.md", "002-second.md"}},
		{"empty title", []string{"01-.md"}},
		{"missing hyphen", []string{"01title.md"}},
		{"missing title", []string{"01.md"}},
		{"uppercase extension", []string{"01-title.MD"}},
		{"control character", []string{"01-title\n.md"}},
		{"zero", []string{"00-title.md"}},
		{"overflow", []string{"18446744073709551616-title.md"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := workRepo(t)
			for _, name := range scenario.files {
				workTicket(t, root, "Example", name)
			}
			if _, err := InspectWork(context.Background(), root, "Example"); err == nil {
				t.Fatal("ambiguous or malformed numbered ticket accepted")
			}
		})
	}
}

func TestInspectWorkRejectsSymlinksAndNonregularTickets(t *testing.T) {
	for _, relative := range []string{".sdlc", ".sdlc/work", ".sdlc/work/Example", ".sdlc/work/Example/tickets", ".sdlc/work/Example/tickets/01-example.md"} {
		t.Run(relative, func(t *testing.T) {
			root := workRepo(t)
			workTicket(t, root, "Example", "01-example.md")
			path := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectWork(context.Background(), root, "Example"); err == nil {
				t.Fatal("symlink work input accepted")
			}
		})
	}
	root := workRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".sdlc/work/Example/tickets/01-directory.md"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectWork(context.Background(), root, "Example"); err == nil {
		t.Fatal("directory ticket accepted")
	}
}

func TestInspectWorkRequiresIgnoredAndUntrackedInputs(t *testing.T) {
	for _, scenario := range []string{"not ignored", "ignore override", "tracked ticket", "tracked other work", "tracked case variant"} {
		t.Run(scenario, func(t *testing.T) {
			root := workRepo(t)
			workTicket(t, root, "Example", "01-example.md")
			switch scenario {
			case "not ignored":
				write(t, root, ".git/info/exclude", "# Existing local excludes\n")
			case "ignore override":
				write(t, root, ".gitignore", "!/.sdlc/work/\n!/.sdlc/work/Example/\n!/.sdlc/work/Example/tickets/\n!/.sdlc/work/Example/tickets/01-example.md\n")
			case "tracked ticket":
				run(t, root, "add", "-f", ".sdlc/work/Example/tickets/01-example.md")
			case "tracked other work":
				workTicket(t, root, "Other", "01-other.md")
				run(t, root, "add", "-f", ".sdlc/work/Other")
			case "tracked case variant":
				write(t, root, ".SDLC/work/Other/notes.txt", "disposable notes\n")
				run(t, root, "add", "-f", ".SDLC/work/Other")
			}
			before := workSnapshot(t, root)
			if _, err := InspectWork(context.Background(), root, "Example"); err == nil {
				t.Fatal("unsafe private work accepted")
			}
			if after := workSnapshot(t, root); !reflect.DeepEqual(before, after) {
				for path, original := range before {
					if current, exists := after[path]; !exists || current != original {
						t.Errorf("work inspection changed path %q", path)
					}
				}
				for path := range after {
					if _, exists := before[path]; !exists {
						t.Errorf("work inspection created path %q", path)
					}
				}
				t.Fatal("failed protection check wrote state")
			}
		})
	}
}

func TestInspectWorkNewRepositoryAndDirectoryBatches(t *testing.T) {
	root := t.TempDir()
	run(t, root, "init", "-b", "main")
	write(t, root, ".git/info/exclude", "/.sdlc/work/\n")
	for id := 1; id <= 130; id++ {
		workTicket(t, root, "Example", fmt.Sprintf("%02d-example.md", id))
	}
	result, err := InspectWork(context.Background(), root, "Example")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tickets) != 130 || !strings.HasSuffix(result.Tickets[0], "/01-example.md") || !strings.HasSuffix(result.Tickets[129], "/130-example.md") {
		t.Fatalf("incorrect ticket order: %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectWork(ctx, root, "Example"); err != context.Canceled {
		t.Fatalf("expected cancelled inspection, got %v", err)
	}
}
