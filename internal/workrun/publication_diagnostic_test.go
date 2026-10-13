package workrun

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationDiagnosticRetainsPrivateBoundedMetadata(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(directory, "publisher")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("fatal: disposable offline failure\n")
	hash := sha256.Sum256(raw)
	d := PublicationDiagnostic{Stage: "command", Code: "command_failed", ExitCode: 128, Path: "failure.stderr", SHA256: hex.EncodeToString(hash[:]), Bytes: len(raw)}
	external := filepath.Join(directory, "must-not-change")
	os.WriteFile(external, []byte("unchanged"), 0644)
	os.Symlink(external, filepath.Join(private, "failure.stderr"))
	os.Symlink(external, filepath.Join(private, "failure.json"))
	if err := retainPublicationDiagnostic(directory, d, raw); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPublicationDiagnostic(directory)
	if err != nil || got.Path != filepath.Join(private, "failure.stderr") || got.Code != d.Code {
		t.Fatal(got, err)
	}
	outside, _ := os.ReadFile(external)
	if string(outside) != "unchanged" {
		t.Fatal("diagnostic followed symlink")
	}
	for _, filename := range []string{"failure.stderr", "failure.json"} {
		info, _ := os.Lstat(filepath.Join(private, filename))
		if info.Mode().Perm() != 0600 || !diagnosticOwned(info) {
			t.Fatal("diagnostic is not private and host owned")
		}
	}
	os.Chmod(filepath.Join(private, "failure.stderr"), 0644)
	if _, err = LoadPublicationDiagnostic(directory); err == nil {
		t.Fatal("public diagnostic accepted")
	}
	os.Chmod(filepath.Join(private, "failure.stderr"), 0600)
	os.WriteFile(filepath.Join(private, "failure.stderr"), []byte(strings.Repeat("x", len(raw))), 0600)
	if _, err = LoadPublicationDiagnostic(directory); err == nil {
		t.Fatal("changed diagnostic accepted")
	}
	d.Bytes = MaximumPublicationDiagnostic + 1
	if err = retainPublicationDiagnostic(directory, d, raw); err == nil {
		t.Fatal("oversize diagnostic accepted")
	}
}
func TestPublisherDiagnosticPipeBound(t *testing.T) {
	var output boundedPublisherDiagnostic
	output.Write([]byte(strings.Repeat("x", MaximumPublicationDiagnostic+100)))
	if !output.exceeded || output.buffer.Len() != MaximumPublicationDiagnostic {
		t.Fatal("diagnostic pipe unbounded")
	}
}
