package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPromptIdentityDoesNotRunFiltersOrFSMonitor(t *testing.T) {
	root := repo(t)
	write(t, root, "README.md", "initial\n")
	run(t, root, "add", "README.md")
	run(t, root, "commit", "-m", "Create disposable source")
	marker := filepath.Join(t.TempDir(), "called")
	hook := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf called > '"+marker+"'\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run(t, root, "config", "core.fsmonitor", hook)
	run(t, root, "config", "filter.example.clean", hook)
	write(t, root, ".gitattributes", "README.md filter=example\n")
	write(t, root, "README.md", "changed\n")
	before := workSnapshot(t, root)
	identity, err := InspectIdentity(context.Background(), root)
	if err != nil || identity.Branch != "main" || !identity.Dirty || identity.Revision == "" {
		t.Fatalf("%+v %v", identity, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("prompt executed source helper")
	}
	after := workSnapshot(t, root)
	if len(before) != len(after) {
		t.Fatal("prompt changed checkout")
	}
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("prompt changed %s", path)
		}
	}
}
