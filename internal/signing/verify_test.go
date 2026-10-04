package signing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const verificationTestImage = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestVerifyUsesSeparateOfflineSecretChannel(t *testing.T) {
	profile, _ := fixture(t)
	calls := 0
	var borrowed []byte
	var borrowedReader *bytes.Reader
	resolver := Resolver{Profile: profile, Run: func(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
		calls++
		if calls == 1 {
			_, err := io.WriteString(output, "disposable-private-key")
			return err
		}
		reader, ok := input.(*bytes.Reader)
		if !ok {
			t.Fatal("key did not use memory channel")
		}
		borrowedReader = reader
		borrowed = make([]byte, reader.Len())
		reader.Read(borrowed)
		if string(borrowed) != "disposable-private-key" {
			t.Fatal("verification did not receive resolved key")
		}
		joined := strings.Join(args, " ")
		for _, required := range []string{"--network none", "--user 1000:1000", "--read-only", "--cap-drop ALL", "--log-driver none", "--tmpfs /tmp:", "--entrypoint /usr/bin/timeout", "--kill-after=5s 45s", verificationTestImage, profile.PublicKey, profile.Fingerprint} {
			if !strings.Contains(joined, required) {
				t.Fatalf("missing verification boundary %s", required)
			}
		}
		hash := sha256.Sum256([]byte(profile.ID + "\n" + profile.PublicKey))
		if !strings.Contains(joined, "io.sdlc.profile="+hex.EncodeToString(hash[:])) {
			t.Fatal("verification has different ownership identity")
		}
		for _, forbidden := range []string{"--mount", "--volume", profile.Reference, profile.BootstrapFile, "disposable-private-key", "fake-service-account-input"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("verification leaked/mounted %s", forbidden)
			}
		}
		_, err := io.WriteString(output, verificationSuccess)
		return err
	}}
	if err := resolver.Verify(context.Background(), verificationTestImage); err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	borrowedReader.Seek(0, io.SeekStart)
	cleared, _ := io.ReadAll(borrowedReader)
	if !bytes.Equal(cleared, make([]byte, len(cleared))) {
		t.Fatal("resolved key remained in host buffer after verification")
	}
	clear(borrowed)
}
func TestVerifyRejectsMutableImageBeforeResolving(t *testing.T) {
	calls := 0
	resolver := Resolver{Run: func(context.Context, io.Reader, io.Writer, ...string) error { calls++; return nil }}
	for _, image := range []string{"sdlc:local", "sha256:invalid", ""} {
		if resolver.Verify(context.Background(), image) == nil {
			t.Fatal("mutable image accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid image retrieved secret")
	}
}
func TestVerifySuppressesDiagnosticsAndRejectsUnexpectedOutput(t *testing.T) {
	for _, kind := range []string{"failure", "extra output", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			profile, _ := fixture(t)
			calls := 0
			resolver := Resolver{Profile: profile, Run: func(_ context.Context, _ io.Reader, output io.Writer, _ ...string) error {
				calls++
				if calls == 1 {
					io.WriteString(output, "disposable-key")
					return nil
				}
				switch kind {
				case "failure":
					return fmt.Errorf("disposable-secret-diagnostic")
				case "extra output":
					io.WriteString(output, verificationSuccess+"disposable-secret-diagnostic")
				case "oversize":
					output.Write(bytes.Repeat([]byte("x"), 65537))
				}
				return nil
			}}
			if err := resolver.Verify(context.Background(), verificationTestImage); err == nil || strings.Contains(err.Error(), "disposable-secret-diagnostic") {
				t.Fatal("verification accepted or leaked diagnostics")
			}
		})
	}
}

// Explicit opt-in image supplied by the test operator. The op retrieval call is
// faked; only verification runs Docker, with a generated key and no network.
func TestVerifyOfflineDockerSignature(t *testing.T) {
	if os.Getenv("SDLC_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_DOCKER_TESTS=1 and SDLC_SIGNING_TEST_IMAGE to an immutable trusted runtime image")
	}
	image := os.Getenv("SDLC_SIGNING_TEST_IMAGE")
	if image == "" {
		t.Skip("SDLC_SIGNING_TEST_IMAGE must identify the immutable trusted runtime image")
	}
	profile, _ := fixture(t)
	directory := filepath.Dir(profile.BootstrapFile)
	keypath := filepath.Join(directory, "disposable-key")
	generation := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "disposable@example.invalid", "-f", keypath)
	generation.Stdout, generation.Stderr = io.Discard, io.Discard
	if generation.Run() != nil {
		t.Fatal("cannot generate disposable test key")
	}
	key, err := os.ReadFile(keypath)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	public, err := exec.Command("/usr/bin/ssh-keygen", "-y", "-f", keypath).Output()
	if err != nil {
		t.Fatal("cannot derive disposable public key")
	}
	profile.PublicKey = strings.Join(strings.Fields(string(public))[:2], " ")
	fingerprint, err := exec.Command("/usr/bin/ssh-keygen", "-lf", keypath+".pub", "-E", "sha256").Output()
	if err != nil {
		t.Fatal("cannot fingerprint disposable key")
	}
	profile.Fingerprint = strings.Fields(string(fingerprint))[1]
	calls := 0
	resolver := Resolver{Profile: profile, Run: func(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
		calls++
		if calls == 1 {
			_, err := output.Write(key)
			return err
		}
		return localCommand(ctx, input, output, args...)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := resolver.Verify(ctx, image); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("verification did not use real offline Docker operation")
	}
}
