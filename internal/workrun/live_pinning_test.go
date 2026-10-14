package workrun

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveCandidateCopiesExactAcceptedTreeAfterHeadMoves(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "copy")
	os.Mkdir(source, 0700)
	os.Mkdir(destination, 0700)
	formatterGit(t, source, "init", "-q")
	os.WriteFile(filepath.Join(source, "first.txt"), []byte("accepted source"), 0644)
	formatterGit(t, source, "add", ".")
	formatterGit(t, source, "commit", "-qm", "Accepted checkpoint")
	tree := formatterGit(t, source, "rev-parse", "HEAD^{tree}")
	os.Remove(filepath.Join(source, "first.txt"))
	os.WriteFile(filepath.Join(source, "later.txt"), []byte("later source"), 0644)
	formatterGit(t, source, "add", ".")
	formatterGit(t, source, "commit", "-qm", "Later checkpoint")
	script := strings.NewReplacer("/source", source, "/workspace", destination, "/inputs", filepath.Join(root, "absent-inputs")).Replace(copyCheckWorkspace)
	script = "import os\nos.chown=lambda *args,**kwargs:None\n" + script
	command := exec.Command("python3", "-I", "-c", script)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SDLC_CHECK_TREE=" + tree}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("exact-tree copy failed: %v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(destination, "first.txt"))
	if err != nil || string(data) != "accepted source" {
		t.Fatal("HEAD race changed accepted source", err, string(data))
	}
	if _, err := os.Stat(filepath.Join(destination, "later.txt")); !os.IsNotExist(err) {
		t.Fatal("later HEAD leaked into accepted copy")
	}
	fingerprint := strings.NewReplacer("/source", source, "/workspace", destination, "/inputs", filepath.Join(root, "absent-inputs")).Replace(candidateFingerprint)
	command = exec.Command("python3", "-I", "-c", fingerprint)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SDLC_CHECK_TREE=" + tree}
	output, err = command.CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(output))) != 64 {
		t.Fatal("accepted tree fingerprint failed", err, string(output))
	}
	// Changing a file named only by the newer HEAD cannot change the accepted
	// tree's fingerprint. This catches enumeration of HEAD after acceptance.
	before := string(output)
	os.WriteFile(filepath.Join(destination, "later.txt"), []byte("unrelated late file"), 0644)
	command = exec.Command("python3", "-I", "-c", fingerprint)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SDLC_CHECK_TREE=" + tree}
	output, err = command.CombinedOutput()
	if err != nil || string(output) != before {
		t.Fatal("fingerprint enumerated newer HEAD", err, string(output))
	}
}
func TestLiveCandidatePinSurvivesFreshFallback(t *testing.T) {
	checker, docker, _, workspace := checkFixture(t)
	docker.fingerprints = []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	pinned := checker.WithDiagnosticRevision(Revision{Head: testHead, Tree: testTree, Clean: true})
	err := pinned.(CandidatePhaseChecker).WithCandidate(context.Background(), workspace, io.Discard, func(scoped Checker) error {
		if err := scoped.Check(context.Background(), workspace, [][]string{{"test", "first"}}, io.Discard); err != nil {
			return err
		}
		return scoped.(VerificationChecker).Verify(context.Background(), workspace, diagnosticRequest("next"), io.Discard)
	})
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	fingerprints := 0
	for _, call := range docker.calls {
		if len(call) == 0 || call[0] != "run" {
			continue
		}
		text := strings.Join(call, " ")
		if !strings.Contains(text, "-copy") && !strings.Contains(text, "-fingerprint") {
			continue
		}
		if !strings.Contains(text, "SDLC_CHECK_TREE="+testTree) {
			t.Fatal("accepted tree lost in phase/fallback", call)
		}
		if strings.Contains(text, "-copy") {
			copies++
		}
		if strings.Contains(text, "-fingerprint") {
			fingerprints++
		}
	}
	if copies < 2 || fingerprints < 2 {
		t.Fatal("test did not exercise source-change fallback", copies, fingerprints)
	}
}
