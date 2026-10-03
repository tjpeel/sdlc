package main

import (
	"context"
	"strings"
	"testing"
)

func TestUnimplementedProviderIsRejectedBeforeAccessingDocker(t *testing.T) {
	for _, command := range []string{"login", "status"} {
		err := auth(context.Background(), []string{command, "--provider", "claude"})
		if err == nil || !strings.Contains(err.Error(), "not implemented yet") {
			t.Fatal("unimplemented provider was enabled")
		}
	}
}
