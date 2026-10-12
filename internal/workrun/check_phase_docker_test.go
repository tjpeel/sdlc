package workrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func TestOfflineCandidatePhase(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_SHARED_DOCKER_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_SHARED_DOCKER_TESTS=1 for credential-free candidate phase checks")
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
	source := realTemp(t)
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, "source.py"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		data, err := isolatedGit(ctx, source, args...)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	git("init", "--initial-branch=main", "--template=")
	write("import pathlib,sys\nassert not (pathlib.Path.home()/'phase-tool').exists()\nassert not pathlib.Path('phase-build').exists()\nprint('baseline-before',flush=True)\nsys.exit(1)\n")
	git("add", ".")
	git("-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-m", "Create disposable baseline")
	baseline := strings.TrimSpace(git("rev-parse", "HEAD"))
	write("import pathlib\nassert (pathlib.Path.home()/'phase-tool').read_text()=='warm'\nassert pathlib.Path('phase-build').read_text()=='warm'\nprint('candidate-after',flush=True)\n")
	git("add", ".")
	git("-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-m", "Create disposable candidate")
	checker := DockerChecker{Runtime: manager, ImageID: state.ImageID}
	err = checker.WithCandidate(ctx, source, io.Discard, func(scoped Checker) error {
		prepare := "import pathlib; (pathlib.Path.home()/'phase-tool').write_text('warm'); pathlib.Path('phase-build').write_text('warm')"
		if err := scoped.Check(ctx, source, [][]string{{"python3", "-c", prepare}}, io.Discard); err != nil {
			return err
		}
		request := VerificationRequest{ID: "candidate", Purpose: "preserve candidate state and isolate baseline", Commands: [][]string{{"python3", "source.py"}}, Baseline: &VerificationBaseline{Revision: baseline, Paths: []string{"source.py"}, ExpectedExitCode: 1, FailureContains: []string{"baseline-before"}}}
		if err := scoped.(VerificationChecker).Verify(ctx, source, request, io.Discard); err != nil {
			return err
		}
		mutate := "import pathlib; assert (pathlib.Path.home()/'phase-tool').read_text()=='warm'; pathlib.Path('source.py').write_text('changed check source')"
		if err := scoped.Check(ctx, source, [][]string{{"python3", "-c", mutate}}, io.Discard); err != nil {
			return err
		}
		fresh := "import pathlib; assert not (pathlib.Path.home()/'phase-tool').exists(); assert not pathlib.Path('phase-build').exists(); assert 'candidate-after' in pathlib.Path('source.py').read_text()"
		return scoped.Check(ctx, source, [][]string{{"python3", "-c", fresh}}, io.Discard)
	})
	if err != nil {
		t.Fatal(err)
	}
}
