package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCommandErrorPlainAndReportedResults(t *testing.T) {
	var output bytes.Buffer
	reportCommandError(&output, errors.New("runtime replacement is blocked"))
	if output.String() != "sdlc: runtime replacement is blocked\n" {
		t.Fatal(output.String())
	}
	output.Reset()
	reportCommandError(&output, errors.New("\x1b]0;hidden\x07problem\u202e\x1b[31m"))
	if output.String() != "sdlc: problem\n" {
		t.Fatal("unsafe error output:", output.String())
	}
	output.Reset()
	err := fmt.Errorf("installer: %w", &reportedError{errors.New("exit status 3")})
	reportCommandError(&output, err)
	if output.Len() != 0 {
		t.Fatal("duplicated child result:", output.String())
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Fatal("failure status lost")
	}
}
