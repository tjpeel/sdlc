package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReferencesMetadataOnlyAndNoWrites(t *testing.T) {
	root := workRepo(t)
	workTicket(t, root, "Example stream", "10-later.md")
	workTicket(t, root, "Example stream", "01-first.md")
	write(t, root, ".sdlc/work/Example stream/tickets/01-first.md", strings.Repeat("\x00\xff", maximumSize))
	workTicket(t, root, "Other", "01-other.md")
	write(t, root, ".sdlc/project.json", "invalid JSON")
	before := workSnapshot(t, root)
	result, err := References(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || len(result.References) != 2 {
		t.Fatalf("%+v", result)
	}
	want := []string{".sdlc/work/Example stream/tickets/01-first.md", ".sdlc/work/Example stream/tickets/10-later.md"}
	if result.References[0].Name != "Example stream" || !reflect.DeepEqual(result.References[0].Tickets, want) {
		t.Fatalf("%+v", result.References)
	}
	if after := workSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("reference discovery changed checkout")
	}
}

func TestReferencesRejectsTrackedAndLinkedWork(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		root := workRepo(t)
		workTicket(t, root, "Example", "01-example.md")
		if _, err := git(context.Background(), root, "add", "-f", ".sdlc/work/Example/tickets/01-example.md"); err != nil {
			t.Fatal(err)
		}
		if _, err := References(context.Background(), root); err == nil || !strings.Contains(err.Error(), "tracked") {
			t.Fatalf("tracked discovery: %v", err)
		}
	})
	t.Run("linked", func(t *testing.T) {
		root := workRepo(t)
		if err := os.MkdirAll(filepath.Join(root, ".sdlc"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, ".sdlc/work")); err != nil {
			t.Skip(err)
		}
		if _, err := References(context.Background(), root); err == nil {
			t.Fatal("linked work accepted")
		}
	})
}

func TestReferencesMissingWorkDoesNotInitialize(t *testing.T) {
	root := repo(t)
	before := workSnapshot(t, root)
	result, err := References(context.Background(), root)
	if err != nil || len(result.References) != 0 {
		t.Fatalf("%+v: %v", result, err)
	}
	if after := workSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("missing work was initialized")
	}
}
