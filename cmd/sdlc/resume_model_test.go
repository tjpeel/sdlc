package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestResumeModelRequiresExplicitModelAndEffort(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"--run", "example", "--model", "model"}, {"--run", "example", "--effort", "high"}} {
		if resumeModelCommand(context.Background(), args, &out) == nil {
			t.Fatal("incomplete selection accepted")
		}
	}
	if err := resumeModelCommand(context.Background(), []string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--effort") {
		t.Fatal("help missing", err)
	}
}
