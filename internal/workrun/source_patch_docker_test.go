package workrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

type offlinePatchCommandRunner struct{}

func (offlinePatchCommandRunner) Run(ctx context.Context, output io.Writer, args ...string) error {
	args = slices.Clone(args)
	i := slices.Index(args, "--network")
	if i >= 0 {
		args[i+1] = "none"
	}
	return (localCheckRunner{}).Run(ctx, output, args...)
}

func TestOfflineSourcePatch(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_SOURCE_PATCH_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_SOURCE_PATCH_TESTS=1 for credential-free source correction Docker checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	manager, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, _, head, tree := sourcePatchCollectorFixture(t, "before\r\n")
	for _, shared := range []bool{false, true} {
		checker := DockerChecker{Runtime: manager, ImageID: state.ImageID, Runner: offlinePatchCommandRunner{}, DockerTests: shared, InputDirectory: t.TempDir()}
		if shared {
			checker.DaemonMode = SharedTestDaemon
		}
		request := sourcePatchRequest()
		request.Commands = [][]string{{"python3", "-I", "-c", "import pathlib; assert not pathlib.Path('/run/sdlc/docker.sock').exists(); assert not (pathlib.Path.home()/'.codex/auth.json').exists(); pathlib.Path('src/Example.cs').write_text('after\\n'); print('UNTRUSTED_PATCH_OUTPUT')"}}
		var log bytes.Buffer
		result, err := checker.VerifySourcePatch(ctx, root, request, head, tree, &log)
		if err != nil || result == nil || result.Diff == "" || !strings.Contains(result.Diff, "+after") || strings.Contains(result.Diff, "UNTRUSTED_PATCH_OUTPUT") || strings.Contains(log.String(), "diff_sha256") {
			t.Fatal("real Docker correction failed", err)
		}
		if data, _ := os.ReadFile(filepath.Join(root, "src/Example.cs")); string(data) != "before\r\n" {
			t.Fatal("host source changed")
		}
		for _, script := range []string{"import pathlib; pathlib.Path('README.md').write_text('outside selection')", "import pathlib; p=pathlib.Path('src/Example.cs'); p.unlink(); p.symlink_to('../README.md')"} {
			request.Commands = [][]string{{"python3", "-I", "-c", script}}
			if result, err := checker.VerifySourcePatch(ctx, root, request, head, tree, io.Discard); err == nil || result != nil {
				t.Fatal("real Docker exported unselected or linked source")
			}
		}
	}
}

func TestOfflineSourcePatchDiffBudget(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_SOURCE_PATCH_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_SOURCE_PATCH_TESTS=1 for credential-free source correction Docker checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	manager, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, _, head, tree := sourcePatchBudgetFixture(t)
	checker := DockerChecker{Runtime: manager, ImageID: state.ImageID, Runner: offlinePatchCommandRunner{}}
	request := sourcePatchRequest()
	request.SourcePatch.Paths = []string{"src/Example.cs", "src/Other.cs"}
	request.Commands = [][]string{{"python3", "-I", "-c", fmt.Sprintf("import pathlib; [pathlib.Path(p).write_text('after\\n'*%d) for p in ['src/Example.cs','src/Other.cs']]", maxSourcePatchDiff/12)}}
	var log bytes.Buffer
	artifact, err := checker.VerifySourcePatch(ctx, root, request, head, tree, &log)
	if !errors.Is(err, ErrCheckFailed) || artifact != nil {
		t.Fatal("real cumulative overflow did not request refinement", err)
	}
	if strings.Contains(log.String(), `"failure"`) || strings.Contains(log.String(), "diff_sha256") {
		t.Fatal("collector response reached command output")
	}
	request.SourcePatch.Paths = []string{"src/Example.cs"}
	request.Commands = [][]string{{"python3", "-I", "-c", fmt.Sprintf("import pathlib; pathlib.Path('src/Example.cs').write_text('after\\n'*%d)", maxSourcePatchDiff/12)}}
	artifact, err = checker.VerifySourcePatch(ctx, root, request, head, tree, io.Discard)
	if err != nil || artifact == nil || artifact.Diff == "" || len(artifact.Diff) > maxSourcePatchDiff {
		t.Fatal("real named refinement failed", err)
	}
	for _, p := range []string{"src/Example.cs", "src/Other.cs"} {
		if data, _ := os.ReadFile(filepath.Join(root, p)); string(data) != "before\n" {
			t.Fatal("budget refinement changed host source")
		}
	}
}
