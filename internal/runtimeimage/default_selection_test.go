package runtimeimage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSelectionIsExplicitAndPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadDefaultName(dir); err != nil || got != "" {
		t.Fatalf("initial default = %q, %v; want local", got, err)
	}
	if err := WriteDefaultName(dir, "sdk-updated"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadDefaultName(dir); err != nil || got != "sdk-updated" {
		t.Fatalf("selected default = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "runtime-default.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("selection permissions = %v, %v", info, err)
	}
	if err := WriteDefaultName(dir, "../unsafe"); err == nil {
		t.Fatal("unsafe runtime name accepted")
	}
	if got, err := ReadDefaultName(dir); err != nil || got != "sdk-updated" {
		t.Fatalf("invalid selection changed default = %q, %v", got, err)
	}
	if err := WriteDefaultName(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadDefaultName(dir); err != nil || got != "" {
		t.Fatalf("reset default = %q, %v", got, err)
	}
}
