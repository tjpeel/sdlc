package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestPublisherFailureKeepsActualStderrOutsidePublicJSON(t *testing.T) {
	oldOutput, oldStage, oldCode, oldExit := failureOutput, failureStage, failureCode, failureExit
	t.Cleanup(func() { failureOutput, failureStage, failureCode, failureExit = oldOutput, oldStage, oldCode, oldExit })
	failureOutput = diagnosticOutput{}
	failureStage = "command"
	failureCode = "command_failed"
	failureExit = 7
	command := exec.Command("/bin/sh", "-c", "printf 'disposable private failure' >&2; printf 'must-not-retain-success-output'; exit 7")
	command.Stderr = &failureOutput
	_, err := trustedCommandOutput(command, true)
	if err == nil {
		t.Fatal("offline subprocess did not fail")
	}
	var private bytes.Buffer
	response := failureResponse(err, &private)
	public, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "disposable") || strings.Contains(string(public), "must-not-retain") || strings.Contains(string(public), "/bin/sh") {
		t.Fatal("raw diagnostics leaked to JSON")
	}
	if !strings.HasPrefix(private.String(), "disposable private failure\nOrigin: publisher command failed:") || response.Diagnostic.ExitCode != 7 {
		t.Fatal("actual stderr or exit lost")
	}
	failureOutput = diagnosticOutput{}
	failureOutput.Write([]byte(strings.Repeat("x", workrun.MaximumPublicationDiagnostic+1)))
	if !failureOutput.exceeded || failureOutput.buffer.Len() != workrun.MaximumPublicationDiagnostic {
		t.Fatal("stderr is not bounded")
	}
}
