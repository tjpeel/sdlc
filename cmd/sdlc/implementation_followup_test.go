package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestImplementationFollowupRequiresRunAndFile(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"--run", "run"}, {"--file", "x"}} {
		if implementationFollowupCommand(context.Background(), args, &out) == nil {
			t.Fatal("incomplete acceptance")
		}
	}
	if err := implementationFollowupCommand(context.Background(), []string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--file") {
		t.Fatal("missing help", err)
	}
}
