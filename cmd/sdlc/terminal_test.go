package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalStatusDoesNotInitializeOrAuthorize(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	t.Setenv("SDLC_STATE_DIR", state)
	var out bytes.Buffer
	if err := terminalCommand(context.Background(), []string{"status", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"ready":false`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("status wrote state")
	}
}
