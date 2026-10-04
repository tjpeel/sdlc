package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestTrustedCommandRejectsArbitraryExecutable(t *testing.T) {
	if _, err := trustedCommand(context.Background(), "/tmp/repository-hook"); err == nil {
		t.Fatal("arbitrary executable accepted")
	}
}
func TestPublisherRejectsMissingFrozenIdentity(t *testing.T) {
	if _, err := execute(context.Background(), workrun.PublisherRequest{Action: "publish"}, strings.NewReader("disposable fake signing material")); err == nil {
		t.Fatal("missing identity accepted")
	}
}
func TestPublisherRejectsUnknownOperation(t *testing.T) {
	identity := &workrun.PublicationIdentity{ProfileID: "example", GitHubID: 1, GitHubLogin: "example", GitName: "Example", GitEmail: "example@example.invalid", SSHPublicKey: "ssh-ed25519 AAAA", SSHFingerprint: "SHA256:example"}
	if _, err := execute(context.Background(), workrun.PublisherRequest{Action: "shell", Plan: workrun.Plan{PublicationIdentity: identity}}, strings.NewReader("")); err == nil {
		t.Fatal("arbitrary operation accepted")
	}
}
