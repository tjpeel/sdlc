package workrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type movingPatchCommand struct {
	patchCommandFake
	move  func()
	moved bool
}

func (c *movingPatchCommand) Run(ctx context.Context, out io.Writer, args ...string) error {
	if !c.moved {
		c.moved = true
		c.move()
	}
	return c.patchCommandFake.Run(ctx, out, args...)
}
func TestLiveRealSourceCorrectionStaleRetryAndOperationalFailures(t *testing.T) {
	for _, kind := range []string{"post identity", "collector stale", "stale cleanup", "stale input", "collector runtime"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := testRun(t)
			j.State = "implementing"
			root, _, head, tree := sourcePatchCollectorFixture(t, "before\n")
			if err := os.Remove(j.Workspace); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(root, j.Workspace); err != nil {
				t.Fatal(err)
			}
			checker, _, _, _ := checkFixture(t)
			artifact := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
			artifact.Head, artifact.Tree = head, tree
			docker := &patchDockerFake{names: map[string]bool{}, result: artifact}
			checker.Runtime.Docker = docker
			if kind != "post identity" {
				response := &collectorResponseDocker{patchDockerFake: docker, response: []byte(`{"failure":"source_patch_candidate_stale"}`)}
				if kind == "collector runtime" {
					response.responseErr = errors.New("private transport failure")
				}
				checker.Runtime.Docker = response
			}
			if kind == "stale cleanup" {
				docker.cleanupFail = true
			}
			if kind == "stale input" {
				os.Mkdir(filepath.Join(directory, "check-inputs"), 0700)
				os.WriteFile(filepath.Join(directory, "check-inputs", ".env"), []byte("fake=before"), 0600)
				j.Plan.CheckInputs = []Input{{Path: ".env", SHA256: patchDigest("fake=before")}}
			}
			moving := &movingPatchCommand{patchCommandFake: patchCommandFake{docker: docker}, move: func() {
				os.WriteFile(filepath.Join(j.Workspace, "src", "Example.cs"), []byte("later checkpoint\n"), 0644)
				formatterGit(t, j.Workspace, "add", ".")
				formatterGit(t, j.Workspace, "commit", "-qm", "Concurrent fixture checkpoint")
				if kind == "stale input" {
					os.WriteFile(filepath.Join(directory, "check-inputs", ".env"), []byte("fake=after"), 0600)
				}
			}}
			checker.Runner = moving
			runner := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{revisions: []Revision{{Head: head, Tree: tree, Clean: true}}})
			runner.Checker = checker
			runner.rawOutput = io.Discard
			request := liveRequest()
			request.Head, request.Tree = head, tree
			request.Requests = []VerificationRequest{sourcePatchRequest()}
			response, err := runner.liveDiagnostics(context.Background(), directory, j, request)
			recoverable := kind == "post identity" || kind == "collector stale"
			if recoverable && (err != nil || response.Error == "") {
				t.Fatal("ordinary checkpoint movement cancelled native turn", response, err)
			}
			if !recoverable && err == nil {
				t.Fatal("cleanup/input/runtime failure became recoverable", response)
			}
			if !moving.moved || len(response.Results) != 0 || len(j.LiveDiagnostics) != 0 {
				t.Fatal("real stale boundary not exercised or provisional artifact leaked")
			}
			receipts, _ := filepath.Glob(filepath.Join(directory, "live-source-patch-*.json"))
			if len(receipts) != 0 {
				t.Fatal("stale batch exported artifacts")
			}
		})
	}
}
func TestSourceCorrectionIdentityUnavailableDoesNotBecomeStale(t *testing.T) {
	err := sourcePatchIdentity(context.Background(), t.TempDir(), testHead, testTree)
	if err == nil || sourcePatchStaleOnly(err) {
		t.Fatal("Git retrieval failure was treated as checkpoint movement", err)
	}
	if sourcePatchStaleOnly(errors.Join(errSourcePatchStale, errors.New("cleanup failed"))) {
		t.Fatal("cleanup failure lost precedence")
	}
}
