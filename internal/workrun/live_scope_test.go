package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type scopeCollectorDocker struct {
	*patchDockerFake
	source, copy string
}

func (d *scopeCollectorDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	data, err := d.patchDockerFake.Output(ctx, args...)
	if err != nil || !slices.Contains(args, collectSourcePatchPython) {
		return data, err
	}
	script := strings.NewReplacer("/source", d.source, "/workspace", d.copy).Replace(collectSourcePatchPython)
	command := exec.CommandContext(ctx, "python3", "-I", "-c", script, args[len(args)-1])
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	return command.Output()
}

type scopeFormatRunner struct {
	patchCommandFake
	copy string
}

func (r *scopeFormatRunner) Run(ctx context.Context, out io.Writer, args ...string) error {
	if err := r.patchCommandFake.Run(ctx, out, args...); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(r.copy, "src", "Example.cs"), []byte("selected formatted\n"), 0644)
	os.WriteFile(filepath.Join(r.copy, "src", "Other.cs"), []byte("outside formatted\n"), 0644)
	return nil
}

type scopeBatchChecker struct {
	DockerChecker
	requests []VerificationRequest
}

func (c *scopeBatchChecker) WithTimingObserver(observer func(CheckTiming)) Checker {
	c.onTiming = observer
	return c
}
func (c *scopeBatchChecker) WithDiagnosticRevision(r Revision) Checker {
	c.ExpectedTree = r.Tree
	return c
}
func (c *scopeBatchChecker) WithCandidate(_ context.Context, _ string, _ io.Writer, run func(Checker) error) error {
	return run(c)
}
func (c *scopeBatchChecker) Verify(_ context.Context, _ string, r VerificationRequest, out io.Writer) error {
	c.requests = append(c.requests, r)
	stream := out.(interface{ commandOutput(int, bool) io.Writer }).commandOutput(1, false)
	io.WriteString(stream, "completed-"+r.ID)
	code := 0
	if r.ID == "compile" {
		code = 1
	}
	stream.(interface{ completed(int) }).completed(code)
	if code != 0 {
		return ErrCheckFailed
	}
	return nil
}
func scopeFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root, copy, _, _ := sourcePatchCollectorFixture(t, "before\n")
	sourceWrite(t, root, "src/Other.cs", "outside before\n")
	formatterGit(t, root, "add", "src")
	formatterGit(t, root, "commit", "-qm", "Add another disposable source file")
	sourceWrite(t, copy, "src/Other.cs", "outside before\n")
	os.Chmod(filepath.Join(copy, "src", "Other.cs"), 0644)
	return root, copy, formatterGit(t, root, "rev-parse", "HEAD"), formatterGit(t, root, "rev-parse", "HEAD^{tree}")
}
func TestActualCollectorScopeRejectionIsBoundedSemanticFailure(t *testing.T) {
	root, copy, head, tree := scopeFixture(t)
	sourceWrite(t, copy, "src/Example.cs", "selected formatted\n")
	sourceWrite(t, copy, "src/Other.cs", "outside formatted\n")
	os.Chmod(filepath.Join(copy, "src", "Other.cs"), 0644)
	result, err := runSourcePatchCollector(t, root, copy, head, tree)
	var scope *sourcePatchScopeError
	if result != nil || !errors.As(err, &scope) || errors.Is(err, ErrCheckFailed) {
		t.Fatalf("confirmed out-of-selection formatting became operational failure instead of bounded rejection: %v", err)
	}
	if !strings.Contains(err.Error(), "selected paths") {
		t.Fatal("scope failure did not explain command selection", err)
	}
}
func TestLiveRealScopedFormatterRejectionPreservesIndependentResults(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "semantic scope rejection"
		if cleanup {
			name = "cleanup remains fatal"
		}
		t.Run(name, func(t *testing.T) {
			directory, j := testRun(t)
			j.State = "implementing"
			j.Attempt = 2
			root, copy, head, tree := scopeFixture(t)
			os.Remove(j.Workspace)
			if err := os.Rename(root, j.Workspace); err != nil {
				t.Fatal(err)
			}
			base, _, _, _ := checkFixture(t)
			artifact := patchFixtureResult("")
			artifact.Head, artifact.Tree = head, tree
			docker := &patchDockerFake{names: map[string]bool{}, result: artifact}
			docker.cleanupFail = cleanup
			base.Runtime.Docker = &scopeCollectorDocker{patchDockerFake: docker, source: j.Workspace, copy: copy}
			base.Runner = &scopeFormatRunner{patchCommandFake: patchCommandFake{docker: docker}, copy: copy}
			checker := &scopeBatchChecker{DockerChecker: base}
			runner := fakeRunner(&fakeProvider{}, &fakeChecker{}, &fakePublisher{}, &fakeRepository{revisions: []Revision{{Head: head, Tree: tree, Clean: true}}})
			runner.Checker = checker
			runner.rawOutput = io.Discard
			request := liveRequest()
			request.Head, request.Tree = head, tree
			request.Requests = []VerificationRequest{diagnosticRequest("compile"), sourcePatchRequest(), diagnosticRequest("later")}
			response, err := runner.liveDiagnostics(context.Background(), directory, j, request)
			if cleanup {
				if err == nil || len(response.Results) != 0 {
					t.Fatal("cleanup fault became normal tool feedback", response, err)
				}
				return
			}
			if err != nil || len(response.Results) != 3 || response.Results[0].Passed || response.Results[1].Passed || !response.Results[2].Passed {
				t.Fatal("scope rejection discarded earlier results or ended independent batch", response, err)
			}
			middle := response.Results[1]
			if middle.SourcePatch != nil || !strings.Contains(strings.ToLower(middle.Diagnostic), "narrow") || !strings.Contains(middle.Diagnostic, "selected paths") || !strings.Contains(middle.Diagnostic, "working root") {
				t.Fatal("scope rejection exported correction or lacked actionable selection feedback", middle)
			}
			if len(checker.requests) != 2 || j.Attempt != 2 || j.Rounds != 0 || j.State != "implementing" {
				t.Fatal("scope rejection ended native turn or changed budgets")
			}
			receipts, _ := filepath.Glob(filepath.Join(directory, "live-source-patch-*.json"))
			if len(receipts) != 0 {
				t.Fatal("rejected formatter exported artifact")
			}
		})
	}
}
func TestScopeRejectionDoesNotHideCollectorIntegrityOrPreparationFault(t *testing.T) {
	if errors.Is(errors.Join(&sourcePatchScopeError{}, errors.New("cleanup fault")), ErrCheckFailed) {
		t.Fatal("scope rejection relaxed existing non-live operational holds")
	}
	if sourcePatchScopeOnly(errors.Join(&sourcePatchScopeError{}, errors.New("operational fault"))) {
		t.Fatal("joined operational failure became a scope rejection")
	}
	for _, kind := range []string{"linked later file", "pre-command mutation"} {
		t.Run(kind, func(t *testing.T) {
			root, copy, head, tree := scopeFixture(t)
			sourceWrite(t, root, "src/ZUnsafe.cs", "safe\n")
			formatterGit(t, root, "add", "src")
			formatterGit(t, root, "commit", "-qm", "Add final disposable file")
			sourceWrite(t, copy, "src/ZUnsafe.cs", "safe\n")
			os.Chmod(filepath.Join(copy, "src", "ZUnsafe.cs"), 0644)
			head = formatterGit(t, root, "rev-parse", "HEAD")
			tree = formatterGit(t, root, "rev-parse", "HEAD^{tree}")
			sourceWrite(t, copy, "src/Other.cs", "outside formatted\n")
			os.Chmod(filepath.Join(copy, "src", "Other.cs"), 0644)
			if kind == "linked later file" {
				os.Remove(filepath.Join(copy, "src", "ZUnsafe.cs"))
				os.Symlink("Example.cs", filepath.Join(copy, "src", "ZUnsafe.cs"))
			}
			selection, _ := json.Marshal(map[string]any{"paths": []string{"src/Example.cs"}, "head": head, "tree": tree, "capture": kind != "pre-command mutation"})
			script := strings.NewReplacer("/source", root, "/workspace", copy).Replace(collectSourcePatchPython)
			command := exec.Command("python3", "-I", "-c", script, string(selection))
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			data, err := command.Output()
			if err == nil {
				if known, failure := sourcePatchCollectionFailure(data); known && sourcePatchScopeOnly(failure) {
					t.Fatal("scope rejection hid integrity/preparation failure")
				}
				t.Fatal("unsafe source unexpectedly validated")
			}
		})
	}
}
