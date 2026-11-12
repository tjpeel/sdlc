package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workseries"
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

func TestProvisionalInvalidatorUsesBindingEstablishedDuringVerification(t *testing.T) {
	var invalidator workseries.ProvisionalInvalidator
	bound := bindProvisionalInvalidator(&invalidator)
	if err := bound(context.Background(), workseries.Ticket{}, workseries.Result{}); err == nil {
		t.Fatal("missing verifier accepted")
	}
	calls := 0
	// Admission receives bound before its verifier installs the actual fencer.
	invalidator = func(_ context.Context, ticket workseries.Ticket, result workseries.Result) error {
		calls++
		if ticket.File != "02-child.md" || result.RunID != "child" {
			t.Fatal("incorrect descendant forwarded")
		}
		return nil
	}
	if err := bound(context.Background(), workseries.Ticket{File: "02-child.md"}, workseries.Result{RunID: "child"}); err != nil || calls != 1 {
		t.Fatalf("fencer was not called: %v", err)
	}
}
