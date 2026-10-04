// sdlc-publisher is a trusted controller entrypoint. It never checks out or runs repository code.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func trustedCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	executable := ""
	switch name {
	case "git":
		executable = "/usr/bin/git"
	case "gh":
		executable = "/usr/local/bin/gh"
	case "ssh-keygen":
		executable = "/usr/bin/ssh-keygen"
	default:
		return nil, fmt.Errorf("unsupported publisher executable")
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "GH_CONFIG_DIR=/github-auth", "GH_HOST=github.com", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "LANG=C.UTF-8"}
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return output, fmt.Errorf("publisher command failed")
	}
	return output, nil
}
func execute(ctx context.Context, request workrun.PublisherRequest, input io.Reader) (workrun.PublisherResponse, error) {
	if request.Plan.PublicationIdentity == nil {
		return workrun.PublisherResponse{}, fmt.Errorf("missing frozen identity")
	}
	identity := request.Plan.PublicationIdentity
	if err := identity.Validate(); err != nil {
		return workrun.PublisherResponse{}, err
	}
	publisher := workrun.GitHubPublisher{Command: trustedCommand, Frozen: identity, ExpectedHead: request.ExpectedHead, ExpectedTree: request.ExpectedTree, BundlePath: "/source.bundle", SigningKeyPath: "/tmp/signing-key", AllowedSignersPath: "/tmp/allowed-signers"}
	switch request.Action {
	case "publish":
		key, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil || len(key) == 0 || len(key) > 65536 {
			return workrun.PublisherResponse{}, fmt.Errorf("invalid signing input")
		}
		defer func() {
			for i := range key {
				key[i] = 0
			}
		}()
		if err := os.WriteFile("/tmp/signing-key", key, 0600); err != nil {
			return workrun.PublisherResponse{}, err
		}
		clear(key)
		defer os.Remove("/tmp/signing-key")
		public, err := trustedCommand(ctx, "ssh-keygen", "-y", "-P", "", "-f", "/tmp/signing-key")
		fields := strings.Fields(string(public))
		if err != nil || len(fields) < 2 || strings.Join(fields[:2], " ") != identity.SSHPublicKey {
			return workrun.PublisherResponse{}, fmt.Errorf("signing public key mismatch")
		}
		public = []byte(strings.Join(fields[:2], " "))
		if err := os.WriteFile("/tmp/signing-public", append(public, '\n'), 0600); err != nil {
			return workrun.PublisherResponse{}, err
		}
		defer os.Remove("/tmp/signing-public")
		fingerprint, err := trustedCommand(ctx, "ssh-keygen", "-lf", "/tmp/signing-public", "-E", "sha256")
		fields = strings.Fields(string(fingerprint))
		if err != nil || len(fields) < 2 || fields[1] != identity.SSHFingerprint {
			return workrun.PublisherResponse{}, fmt.Errorf("signing fingerprint mismatch")
		}
		if err := os.WriteFile("/tmp/allowed-signers", []byte("* "+strings.TrimSpace(string(public))+"\n"), 0600); err != nil {
			return workrun.PublisherResponse{}, err
		}
		defer os.Remove("/tmp/allowed-signers")
		publisher.AfterSigning = func() error { return os.Remove("/tmp/signing-key") }
		publication, err := publisher.Publish(ctx, request.Plan, "", "/publisher", request.Previous, nil)
		return workrun.PublisherResponse{Publication: publication}, err
	case "checks":
		checks, err := publisher.Checks(ctx, request.Plan, request.Previous)
		return workrun.PublisherResponse{Checks: checks}, err
	default:
		return workrun.PublisherResponse{}, fmt.Errorf("invalid publisher action")
	}
}
func main() {
	// A detached or killed Docker client must not leave credentials available
	// indefinitely. PID 1 exiting also terminates its remaining processes.
	deadline := time.AfterFunc(2*time.Minute, func() { os.Exit(1) })
	defer deadline.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	response := workrun.PublisherResponse{Error: "publisher refused request"}
	file, err := os.Open("/request.json")
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(file, 1024*1024+1))
		file.Close()
	}
	if err == nil && len(data) <= 1024*1024 {
		var request workrun.PublisherRequest
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) == nil {
			var extra any
			if decoder.Decode(&extra) == io.EOF {
				if result, err := execute(ctx, request, os.Stdin); err == nil {
					response = result
				}
			}
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
}
