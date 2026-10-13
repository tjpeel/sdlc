package providerauth

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const testOperation = "sdlc-headless-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testInstallation = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const testOperationImage = "sha256:test-operation-image"

func TestOperationProcessHelper(t *testing.T) {
	if os.Getenv("SDLC_OPERATION_TEST") == "" {
		return
	}
	manager := New(runtimeimage.Manager{Directory: os.Getenv("SDLC_OPERATION_TEST")})
	lock, err := manager.registerOperation(testOperation, "sdlc-auth-"+testInstallation+"-codex", "codex", testOperationImage)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Println("operation-ready")
	var input [1]byte
	_, _ = os.Stdin.Read(input[:])
}

func TestOperationLivenessAcrossProcesses(t *testing.T) {
	directory := t.TempDir()
	manager := New(runtimeimage.Manager{Directory: directory})
	command := exec.Command(os.Args[0], "-test.run=^TestOperationProcessHelper$")
	command.Env = append(os.Environ(), "SDLC_OPERATION_TEST="+directory)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "operation-ready" {
		t.Fatal("operation did not register")
	}
	if !manager.liveOperation(testOperation, testInstallation, "codex", testOperationImage) {
		t.Fatal("live invocation rejected")
	}
	if manager.liveOperation(testOperation, testInstallation, "claude", testOperationImage) || manager.liveOperation(testOperation, testInstallation, "codex", "sha256:other") {
		t.Fatal("wrong invocation accepted")
	}
	input.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if manager.liveOperation(testOperation, testInstallation, "codex", testOperationImage) {
		t.Fatal("orphan invocation accepted")
	}
}
