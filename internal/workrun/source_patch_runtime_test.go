package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

type patchDockerFake struct {
	checkDockerFake
	names    map[string]bool
	result   *SourcePatchResult
	captures int
}

func (docker *patchDockerFake) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "run" {
		i := slices.Index(args, "--name")
		if i >= 0 {
			docker.names[args[i+1]] = true
		}
	}
	if args[0] == "rm" {
		if !docker.names[args[2]] {
			return nil, errors.New("cannot remove absent container")
		}
		delete(docker.names, args[2])
	}
	data, err := docker.checkDockerFake.Output(ctx, args...)
	if err != nil {
		return data, err
	}
	if slices.Contains(args, collectSourcePatchPython) {
		var selection struct {
			Capture bool `json:"capture"`
		}
		if err := json.Unmarshal([]byte(args[len(args)-1]), &selection); err != nil {
			return nil, err
		}
		result := *docker.result
		if selection.Capture {
			docker.captures++
		} else {
			result.Diff = ""
			result.DiffSHA256 = patchDigest("")
			result.Files = append([]SourcePatchFile(nil), result.Files...)
			for i := range result.Files {
				result.Files[i].After = result.Files[i].Before
			}
		}
		return json.Marshal(result)
	}
	return data, nil
}
func (docker *patchDockerFake) Run(context.Context, ...string) error { panic("unexpected Docker Run") }

type patchCommandFake struct {
	checkRunnerFake
	docker *patchDockerFake
}

type collectorResponseDocker struct {
	*patchDockerFake
	response    []byte
	responseErr error
}

func (docker *collectorResponseDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	data, err := docker.patchDockerFake.Output(ctx, args...)
	if err == nil && slices.Contains(args, collectSourcePatchPython) {
		var selection struct {
			Capture bool `json:"capture"`
		}
		if json.Unmarshal([]byte(args[len(args)-1]), &selection) == nil && selection.Capture {
			return docker.response, docker.responseErr
		}
	}
	return data, err
}

func TestSourcePatchKnownDiffBudgetRequestsRefinement(t *testing.T) {
	checker, _, _, _ := checkFixture(t)
	root, _, head, tree := sourcePatchCollectorFixture(t, "before\n")
	result := patchFixtureResult("")
	result.Head, result.Tree = head, tree
	base := &patchDockerFake{names: map[string]bool{}, result: result}
	checker.Runtime.Docker = &collectorResponseDocker{patchDockerFake: base, response: []byte(`{"failure":"source_patch_diff_budget_exceeded"}`)}
	checker.Runner = &patchCommandFake{docker: base}
	artifact, err := checker.VerifySourcePatch(context.Background(), root, sourcePatchRequest(), head, tree, io.Discard)
	if !errors.Is(err, ErrCheckFailed) || artifact != nil {
		t.Fatalf("known budget overflow did not request safe refinement: %v", err)
	}
	feedback := verificationDiagnostic(sourcePatchRequest(), tree, err)
	if !strings.Contains(feedback, "source_patch.paths") || !strings.Contains(feedback, "formatter arguments") {
		t.Fatal("bounded request refinement guidance absent", feedback)
	}
}

func TestSourcePatchRefinementMarkerIsStrictAndOperationalErrorsStayPrivate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  string
		dockerErr error
		cleanup   bool
	}{
		{"unknown code", `{"failure":"private-fixture-code"}`, nil, false},
		{"extra source", `{"failure":"source_patch_diff_budget_exceeded","source":"private-fixture-source"}`, nil, false},
		{"duplicate code", `{"failure":"private-fixture-code","failure":"source_patch_diff_budget_exceeded"}`, nil, false},
		{"duplicate known code", `{"failure":"source_patch_diff_budget_exceeded","failure":"source_patch_diff_budget_exceeded"}`, nil, false},
		{"trailing output", `{"failure":"source_patch_diff_budget_exceeded"} private-fixture-trace`, nil, false},
		{"stderr substring", "private-fixture-trace source diff exceeds bound", nil, false},
		{"Docker failure", `{"failure":"source_patch_diff_budget_exceeded"}`, errors.New("private-fixture-Docker-error"), false},
		{"cancellation", `{"failure":"source_patch_diff_budget_exceeded"}`, context.Canceled, false},
		{"cleanup failure", `{"failure":"source_patch_diff_budget_exceeded"}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker, _, _, _ := checkFixture(t)
			root, _, head, tree := sourcePatchCollectorFixture(t, "before\n")
			result := patchFixtureResult("")
			result.Head, result.Tree = head, tree
			base := &patchDockerFake{names: map[string]bool{}, result: result}
			base.cleanupFail = tc.cleanup
			checker.Runtime.Docker = &collectorResponseDocker{patchDockerFake: base, response: []byte(tc.response), responseErr: tc.dockerErr}
			checker.Runner = &patchCommandFake{docker: base}
			artifact, err := checker.VerifySourcePatch(context.Background(), root, sourcePatchRequest(), head, tree, io.Discard)
			if err == nil || errors.Is(err, ErrCheckFailed) || artifact != nil {
				t.Fatal("unexpected collector failure became semantic or exported data", err)
			}
			if tc.dockerErr != nil && !errors.Is(err, tc.dockerErr) {
				t.Fatal("underlying operational error lost", err)
			}
			feedback := verificationDiagnostic(sourcePatchRequest(), tree, err)
			if strings.Contains(feedback, "private-fixture") || strings.Contains(feedback, "source_patch_diff_budget_exceeded") {
				t.Fatal("collector source or raw error leaked", feedback)
			}
		})
	}
}

func (runner *patchCommandFake) Run(ctx context.Context, output io.Writer, args ...string) error {
	i := slices.Index(args, "--name")
	if i >= 0 {
		runner.docker.names[args[i+1]] = true
	}
	return runner.checkRunnerFake.Run(ctx, output, args...)
}

func TestSourcePatchDockerLifecycleIsSourceOnlyAndDiscardsFailures(t *testing.T) {
	for _, scenario := range []string{"success", "command failure", "cleanup failure"} {
		t.Run(scenario, func(t *testing.T) {
			checker, _, _, _ := checkFixture(t)
			root, _, head, tree := sourcePatchCollectorFixture(t, "before\n")
			result := patchFixtureResult("--- a/src/Example.cs\n+++ b/src/Example.cs\n@@ -1 +1 @@\n-before\n+after\n")
			result.Head, result.Tree = head, tree
			docker := &patchDockerFake{names: map[string]bool{}, result: result}
			runner := &patchCommandFake{docker: docker}
			checker.Runtime.Docker = docker
			checker.Runner = runner
			checker.InputDirectory = t.TempDir()
			checker.DockerTests = true
			checker.DaemonMode = SharedTestDaemon
			if scenario == "command failure" {
				runner.fail = true
			}
			if scenario == "cleanup failure" {
				docker.cleanupFail = true
			}
			var log bytes.Buffer
			artifact, err := checker.VerifySourcePatch(context.Background(), root, sourcePatchRequest(), head, tree, &log)
			if scenario == "success" {
				if err != nil || artifact == nil {
					t.Fatal(err)
				}
			} else if err == nil || artifact != nil {
				t.Fatal("failed collection exported artifact")
			}
			if scenario == "command failure" && (!errors.Is(err, ErrCheckFailed) || docker.captures != 0) {
				t.Fatal("failed command captured source or lost semantic classification", err)
			}
			if strings.Contains(log.String(), "diff_sha256") || strings.Contains(log.String(), "+++ b/") {
				t.Fatal("collector stdout reached command log")
			}
			for _, args := range docker.calls {
				text := strings.Join(args, " ")
				if strings.Contains(text, "dst=/inputs") || strings.Contains(text, "--privileged") || strings.Contains(text, "docker.sock") || strings.Contains(text, "dst=/run/sdlc") || strings.Contains(text, "-templates") {
					t.Fatal("source export used inputs, daemon or shared source", text)
				}
				if slices.Contains(args, collectSourcePatchPython) {
					if !slices.Contains(args, "-I") || !strings.Contains(text, "--workdir /") || !strings.Contains(text, "--network none") || !strings.Contains(text, "dst=/source,readonly") || !strings.Contains(text, "dst=/workspace,readonly") {
						t.Fatal("collector isolation missing", text)
					}
				}
			}
			for _, args := range runner.calls {
				text := strings.Join(args, " ")
				if strings.Contains(text, "DOCKER_HOST=") || strings.Contains(text, "dst=/inputs") || !strings.Contains(text, "dst=/tmp/check-home") || strings.Contains(text, "nuget-cache") {
					t.Fatal("formatter HOME or credential boundary missing", text)
				}
			}
		})
	}
}
