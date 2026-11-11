package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTicketProvisionalHelpAndRequiredFlags(t *testing.T) {
	var out bytes.Buffer
	if err := ticketCommand(context.Background(), []string{"provisional", "--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--revoke") || !strings.Contains(out.String(), "--dry-run") {
		t.Fatal("provisional command help incomplete")
	}
	for _, args := range [][]string{{"provisional"}, {"provisional", "--reference", "TASK-1"}, {"provisional", "--reference", "TASK-1", "--ticket", "01.md", "extra"}} {
		if err := ticketCommand(context.Background(), args, &out); err == nil {
			t.Fatalf("invalid options admitted: %v", args)
		}
	}
}
