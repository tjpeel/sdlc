package main

import (
	"context"
	"strings"
	"testing"
)

func TestUnknownProviderIsRejectedBeforeAccessingDocker(t *testing.T) {
	for _, command := range []string{"login", "status"} {
		err := auth(context.Background(), []string{command, "--provider", "untrusted"})
		if err == nil || !strings.Contains(err.Error(), "provider must be") {
			t.Fatal("unknown provider was enabled")
		}
	}
}
