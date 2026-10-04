package signing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercise the resolver's actual user and sandbox flags with the pinned official
// CLI, while replacing credential retrieval with an offline initialization check.
func TestResolverOfflineDockerInitializesOfficialCLI(t *testing.T) {
	if os.Getenv("SDLC_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_DOCKER_TESTS=1 to test the pinned official op image offline")
	}
	for _, name := range []string{"OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_HOST", "OP_CONNECT_TOKEN"} {
		t.Setenv(name, "")
	}
	profile, _ := fixture(t)
	calls := 0
	probeSucceeded := false
	resolver := Resolver{Profile: profile, Run: func(ctx context.Context, _ io.Reader, output io.Writer, args ...string) error {
		calls++
		probeArgs := append([]string(nil), args...)
		name := ""
		networkReplaced := false
		helperReplaced := false
		userFound := false
		logDisabled := false
		for i, arg := range probeArgs {
			switch arg {
			case "--name":
				if i+1 < len(probeArgs) {
					name = probeArgs[i+1]
				}
			case "--network":
				if i+1 < len(probeArgs) {
					probeArgs[i+1] = "none"
					networkReplaced = true
				}
			case "--user":
				userFound = i+1 < len(probeArgs) && probeArgs[i+1] != ""
			case "--log-driver":
				logDisabled = i+1 < len(probeArgs) && probeArgs[i+1] == "none"
			case "--mount", "--volume", "-v":
				return fmt.Errorf("offline op probe must not mount host storage")
			case helper:
				probeArgs[i] = `set -eu
umask 077
unset OP_SERVICE_ACCOUNT_TOKEN OP_CONNECT_HOST OP_CONNECT_TOKEN
mkdir /tmp/sdlc-op
export HOME=/tmp/sdlc-op OP_CONFIG_DIR=/tmp/sdlc-op/.op
op account list --format=json
`
				helperReplaced = true
			}
		}
		joined := strings.Join(probeArgs, " ")
		if !strings.HasPrefix(name, "sdlc-signing-") || !networkReplaced || !helperReplaced || !userFound || !logDisabled || !strings.Contains(joined, "--rm") || !strings.Contains(joined, "io.sdlc.kind=signing") || !strings.Contains(joined, "io.sdlc.profile=") {
			return fmt.Errorf("offline op probe lacks resolver boundaries")
		}
		t.Cleanup(func() { cleanupOfflineOPContainer(t, name) })
		var stdout, diagnostics bytes.Buffer
		command := exec.CommandContext(ctx, "docker", probeArgs...)
		// Leave Stdin nil: the fake bootstrap read by Resolve never reaches op.
		command.Stdout, command.Stderr = &stdout, &diagnostics
		if command.Run() != nil {
			return fmt.Errorf("official op initialization failed under the resolver Docker user")
		}
		if strings.TrimSpace(stdout.String()) != "[]" {
			return fmt.Errorf("official op initialization did not return an empty offline account list")
		}
		probeSucceeded = true
		_, err := io.WriteString(output, "disposable-signing-key")
		return err
	}}
	key, err := resolver.Resolve(context.Background())
	defer clear(key)
	if err != nil || calls != 1 || !probeSucceeded || string(key) != "disposable-signing-key" {
		t.Fatal("resolver could not initialize the pinned official op CLI offline; native diagnostics withheld")
	}
}

func cleanupOfflineOPContainer(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list := func() ([]byte, error) {
		command := exec.CommandContext(ctx, "docker", "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		command.Stderr = io.Discard
		return command.Output()
	}
	listed, err := list()
	if err != nil {
		t.Error("cannot confirm offline op container cleanup")
		return
	}
	if strings.TrimSpace(string(listed)) == name {
		command := exec.CommandContext(ctx, "docker", "rm", "--force", name)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if command.Run() != nil {
			t.Error("cannot remove offline op container")
			return
		}
	}
	listed, err = list()
	if err != nil || len(bytes.TrimSpace(listed)) != 0 {
		t.Error("offline op container cleanup could not be confirmed")
	}
}
