package featureguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingMigrationFencesJournalAccess(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".sdlc", "work", "TASK-1", "series")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Check(root, "TASK-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RuntimeMigrationPending), []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Check(root, "TASK-1"); err == nil {
		t.Fatal("pending transaction did not fence journal")
	}
	if err := Check(root, "../other"); err == nil {
		t.Fatal("unsafe reference accepted")
	}
}
