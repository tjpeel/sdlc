package main

import (
	"context"
	"io"
	"os/exec"
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

func TestTrustedAPICommandBoundsActualSubprocessOutput(t *testing.T) {
	for _, script := range []string{"head -c 524289 /dev/zero"} {
		command := exec.Command("/bin/sh", "-c", script)
		command.Stderr = io.Discard
		data, err := trustedCommandOutput(command, true)
		if err == nil || len(data) != 0 {
			t.Fatal("unsafe API output retained", len(data), err)
		}
	}
	command := exec.Command("/bin/sh", "-c", "printf '{\"safe\":true}'")
	data, err := trustedCommandOutput(command, true)
	if err != nil || string(data) != `{"safe":true}` {
		t.Fatal("bounded API response lost", string(data), err)
	}
}

func TestTrustedGitHubCommandRetainsStructuredFailureAndPendingOutput(t *testing.T) {
	for _, code := range []string{"1", "8"} {
		command := exec.Command("/bin/sh", "-c", `printf '[{"name":"tests","bucket":"pending"}]'; exit `+code)
		data, err := trustedCommandOutput(command, true)
		if err == nil || string(data) != `[{"name":"tests","bucket":"pending"}]` {
			t.Fatal("structured gh check status discarded", string(data), err)
		}
	}
}
