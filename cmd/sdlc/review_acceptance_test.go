package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReviewAcceptanceCommandRequiresExplicitReasonFile(t *testing.T) {
	var output bytes.Buffer
	if err := reviewAcceptanceCommand(context.Background(), []string{"--run", "example"}, &output); err == nil {
		t.Fatal("accepted a request without a reason file")
	}
	if err := reviewAcceptanceCommand(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), "--reason-file") {
		t.Fatalf("help omitted the explicit reason: %v", err)
	}
}
