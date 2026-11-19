package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestProviderTakeoverRequiresExplicitReviewerAndReason(t *testing.T) {
	var output bytes.Buffer
	for _, args := range [][]string{{"--run", "example", "--provider", "codex"}, {"--run", "example", "--provider", "codex", "--reason-file", "reason"}, {"--run", "example", "--provider", "codex", "--review-provider", "codex"}} {
		if err := providerTakeoverCommand(context.Background(), args, &output); err == nil {
			t.Fatal("incomplete operator choice accepted")
		}
	}
	if err := providerTakeoverCommand(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "--review-provider") || !strings.Contains(output.String(), "--reason-file") {
		t.Fatal("takeover help omitted explicit choices", err)
	}
}
