package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOperatorDirectionRequiresRunAndFile(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"--run", "example"}, {"--file", "direction.txt"}} {
		if operatorDirectionCommand(context.Background(), args, &out) == nil {
			t.Fatal("incomplete direction accepted")
		}
	}
	if err := operatorDirectionCommand(context.Background(), []string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--file") {
		t.Fatal("missing help", err)
	}
}
