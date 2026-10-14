package workrun

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type liveNativeDocker struct {
	pairingRaceDocker
	stream  func(context.Context, io.Writer, []string) error
	streams int
}

func (d *liveNativeDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "volume" && args[1] == "inspect" {
		return json.Marshal([]map[string]any{{"Name": d.volume, "Driver": "local", "Options": map[string]string{}, "Labels": map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "provider-auth", "io.sdlc.installation": strings.Repeat("d", 32), "io.sdlc.provider": "codex"}}})
	}
	if args[0] == "run" && args[len(args)-1] == "status" {
		return []byte(`{"state":"stored"}`), nil
	}
	return d.pairingRaceDocker.Output(ctx, args...)
}
func (d *liveNativeDocker) Stream(ctx context.Context, _ io.Reader, out, _ io.Writer, args ...string) error {
	d.streams++
	return d.stream(ctx, out, args)
}
func liveNativeFixture(t *testing.T) (NativeProvider, *liveNativeDocker) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	docker := &liveNativeDocker{pairingRaceDocker: pairingRaceDocker{image: "sha256:" + strings.Repeat("a", 64), volume: "sdlc-auth-" + strings.Repeat("d", 32) + "-codex"}}
	state, _ := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: docker.image})
	os.WriteFile(filepath.Join(root, "runtime.json"), state, 0600)
	os.WriteFile(filepath.Join(root, "auth-installation.json"), []byte(`{"id":"`+strings.Repeat("d", 32)+`"}`), 0600)
	runtime := runtimeimage.Manager{Directory: root, Docker: docker}
	manager := providerauth.Manager{Runtime: runtime, Docker: docker}
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	return NativeProvider{Manager: manager, ImageID: docker.image, diagnosticTransport: func(directory string) (*diagnosticService, error) {
		return startDiagnosticServiceAt(directory, "127.0.0.1", "127.0.0.1")
	}}, docker
}
func nativeMounts(t *testing.T, args []string) map[string]string {
	t.Helper()
	mounts := map[string]string{}
	for i, arg := range args {
		if arg != "--mount" {
			continue
		}
		fields, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
		if err != nil {
			t.Fatal(err)
		}
		src, dst := "", ""
		for _, field := range fields {
			if strings.HasPrefix(field, "src=") {
				src = strings.TrimPrefix(field, "src=")
			}
			if strings.HasPrefix(field, "dst=") {
				dst = strings.TrimPrefix(field, "dst=")
			}
		}
		mounts[dst] = src
	}
	return mounts
}
func invokeMountedHelper(t *testing.T, ctx context.Context, mounts map[string]string, request []VerificationRequest) LiveDiagnosticResponse {
	t.Helper()
	data, err := os.ReadFile(mounts["/sdlc-diagnostics.py"])
	if err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/sdlc-diagnostic-requests", mounts["/sdlc-diagnostic-requests"], "/sdlc-diagnostic-responses", mounts["/sdlc-diagnostic-responses"], "/workspace", mounts["/workspace"]).Replace(string(data))
	helper := filepath.Join(t.TempDir(), "helper.py")
	os.WriteFile(helper, []byte(script), 0444)
	input, _ := json.Marshal(request)
	command := exec.CommandContext(ctx, "python3", "-I", helper)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native helper failed: %v %s", err, output)
	}
	var response LiveDiagnosticResponse
	if json.Unmarshal(output, &response) != nil {
		t.Fatal("invalid live response", string(output))
	}
	return response
}
func TestNativeInvocationUsesLiveHelperDirtyCommitRetryWithoutResume(t *testing.T) {
	directory, j := testRun(t)
	j.State = "implementing"
	j.Attempt = 1
	formatterGit(t, j.Workspace, "init", "-q")
	os.WriteFile(filepath.Join(j.Workspace, "example.txt"), []byte("initial"), 0600)
	formatterGit(t, j.Workspace, "add", ".")
	formatterGit(t, j.Workspace, "commit", "-qm", "Fixture checkpoint")
	repo := &fakeRepository{}
	checker := &diagnosticPhaseChecker{}
	runner := fakeRunner(&fakeProvider{}, &checker.fakeChecker, &fakePublisher{}, repo)
	runner.Checker = checker
	runner.rawOutput = io.Discard
	provider, docker := liveNativeFixture(t)
	session := Session{Model: j.Plan.Roles.Implementation, Role: "implementation", Workspace: j.Workspace, Directory: filepath.Join(directory, "native"), Prompt: "fixture", Schema: outcomeSchema, Diagnostics: func(ctx context.Context, request LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
		return runner.liveDiagnostics(ctx, directory, j, request)
	}}
	docker.stream = func(ctx context.Context, out io.Writer, args []string) error {
		mounts := nativeMounts(t, args)
		if mounts["/sdlc-diagnostic-responses"] == "" {
			t.Fatal("native invocation lacked channel")
		}
		head := formatterGit(t, j.Workspace, "rev-parse", "HEAD")
		tree := formatterGit(t, j.Workspace, "rev-parse", "HEAD^{tree}")
		repo.revisions = []Revision{{Head: head, Tree: tree, Clean: false}}
		os.WriteFile(filepath.Join(j.Workspace, "example.txt"), []byte("repair"), 0600)
		first := invokeMountedHelper(t, ctx, mounts, []VerificationRequest{diagnosticRequest("probe")})
		if first.Error == "" || len(first.Results) != 0 || len(checker.requests) != 0 {
			t.Fatal("dirty checkpoint reached checker", first)
		}
		formatterGit(t, j.Workspace, "add", ".")
		formatterGit(t, j.Workspace, "commit", "-qm", "Repair checkpoint")
		head = formatterGit(t, j.Workspace, "rev-parse", "HEAD")
		tree = formatterGit(t, j.Workspace, "rev-parse", "HEAD^{tree}")
		repo.revisions = []Revision{{Head: head, Tree: tree, Clean: true}}
		second := invokeMountedHelper(t, ctx, mounts, []VerificationRequest{diagnosticRequest("probe")})
		if second.Error != "" || len(second.Results) != 1 || !second.Results[0].Passed {
			t.Fatal("commit retry did not continue native turn", second)
		}
		io.WriteString(out, codexEvents(testOutcome("checks_requested")))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := provider.Execute(ctx, session, io.Discard, io.Discard)
	if err != nil || result.Outcome.Status != "checks_requested" || docker.streams != 1 || len(checker.requests) != 1 || j.Attempt != 1 || j.Rounds != 0 || checker.calls != 0 {
		t.Fatal("live checks restarted native turn or bypassed full gates", result, err)
	}
	matches, _ := filepath.Glob(filepath.Join(directory, ".provider-input-*"))
	if len(matches) != 0 {
		t.Fatal("invocation mailboxes survived completion", matches)
	}
}
func TestNativeDiagnosticFailureCancelsInvocationAndJoinsBeforeCleanup(t *testing.T) {
	directory, j := testRun(t)
	formatterGit(t, j.Workspace, "init", "-q")
	os.WriteFile(filepath.Join(j.Workspace, "example.txt"), []byte("initial"), 0600)
	formatterGit(t, j.Workspace, "add", ".")
	formatterGit(t, j.Workspace, "commit", "-qm", "Fixture checkpoint")
	provider, docker := liveNativeFixture(t)
	callbackDone := make(chan struct{})
	requestRoot := ""
	session := Session{Model: j.Plan.Roles.Implementation, Role: "implementation", Workspace: j.Workspace, Directory: filepath.Join(directory, "native"), Prompt: "fixture", Schema: outcomeSchema, Diagnostics: func(context.Context, LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
		defer close(callbackDone)
		return LiveDiagnosticResponse{}, errors.New("private authentication or runtime stderr")
	}}
	docker.stream = func(ctx context.Context, _ io.Writer, args []string) error {
		mounts := nativeMounts(t, args)
		requestRoot = mounts["/sdlc-diagnostic-requests"]
		request := liveRequest("probe")
		data, _ := json.Marshal(request)
		os.WriteFile(filepath.Join(requestRoot, "request.json"), data, 0644)
		<-ctx.Done()
		if _, err := os.Stat(requestRoot); err != nil {
			t.Fatal("mailbox removed before native stopped", err)
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := provider.Execute(ctx, session, io.Discard, io.Discard)
	if err == nil || strings.Contains(err.Error(), "private authentication") {
		t.Fatal("fatal diagnostics leaked raw failure or allowed native completion", err)
	}
	select {
	case <-callbackDone:
	default:
		t.Fatal("service not joined")
	}
	if _, err := os.Stat(requestRoot); !os.IsNotExist(err) {
		t.Fatal("mailbox cleanup did not follow join", err)
	}
}

func TestNativeFinishAndCancellationJoinOutstandingDiagnosticBeforeCleanup(t *testing.T) {
	for _, kind := range []string{"finish", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			directory, j := testRun(t)
			provider, docker := liveNativeFixture(t)
			entered, joined := make(chan struct{}), make(chan struct{})
			requestRoot := ""
			session := Session{Model: j.Plan.Roles.Implementation, Role: "implementation", Workspace: j.Workspace, Directory: filepath.Join(directory, "native"), Prompt: "fixture", Schema: outcomeSchema, Diagnostics: func(ctx context.Context, _ LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
				close(entered)
				<-ctx.Done()
				if _, err := os.Stat(requestRoot); err != nil {
					t.Error("mailbox removed before outstanding check joined", err)
				}
				close(joined)
				return LiveDiagnosticResponse{}, ctx.Err()
			}}
			docker.stream = func(ctx context.Context, out io.Writer, args []string) error {
				requestRoot = nativeMounts(t, args)["/sdlc-diagnostic-requests"]
				request := liveRequest("probe")
				data, _ := json.Marshal(request)
				os.WriteFile(filepath.Join(requestRoot, "request.json"), data, 0644)
				<-entered
				if kind == "cancel" {
					<-ctx.Done()
					return ctx.Err()
				}
				io.WriteString(out, codexEvents(testOutcome("checks_requested")))
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if kind == "cancel" {
				go func() { <-entered; cancel() }()
			}
			_, err := provider.Execute(ctx, session, io.Discard, io.Discard)
			if err == nil {
				t.Fatal("outstanding diagnostic execution escaped hold")
			}
			select {
			case <-joined:
			default:
				t.Fatal("service did not join before return")
			}
			if _, err := os.Stat(requestRoot); !os.IsNotExist(err) {
				t.Fatal("mailbox remained after join", err)
			}
		})
	}
}

func TestUnavailableLiveTransportPreservesNativeStructuredHandoff(t *testing.T) {
	provider, docker := liveNativeFixture(t)
	provider.diagnosticTransport = func(string) (*diagnosticService, error) { return nil, errors.New("unsupported offline bridge") }
	directory, j := testRun(t)
	session := Session{Model: j.Plan.Roles.Implementation, Role: "implementation", Workspace: j.Workspace, Directory: filepath.Join(directory, "native"), Prompt: "For narrow checks during this running turn, use the helper.\nSelected implementation requirements.", Schema: outcomeSchema, Instructions: "For narrow checks during this running turn, use the helper.\nController checks remain required.", Diagnostics: func(context.Context, LiveDiagnosticRequest) (LiveDiagnosticResponse, error) {
		t.Fatal("unavailable channel dispatched diagnostics")
		return LiveDiagnosticResponse{}, nil
	}}
	docker.stream = func(_ context.Context, out io.Writer, args []string) error {
		mounts := nativeMounts(t, args)
		if mounts["/sdlc-diagnostics.py"] != "" || mounts["/sdlc-diagnostic-requests"] != "" || mounts["/sdlc-diagnostic-responses"] != "" {
			t.Fatal("unavailable live channel still mounted")
		}
		inspected := 0
		for destination, source := range mounts {
			if destination != "/instructions.md" && destination != "/prompt.txt" {
				continue
			}
			content, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			inspected++
			if strings.Contains(string(content), "For narrow checks during") || !strings.Contains(string(content), "diagnostics_requested") {
				t.Fatal("agent did not receive structured fallback capability", destination)
			}
		}
		if inspected != 2 {
			t.Fatal("fallback capability instructions were not inspected", inspected)
		}
		io.WriteString(out, codexEvents(testOutcome("checks_requested")))
		return nil
	}
	if result, err := provider.Execute(context.Background(), session, io.Discard, io.Discard); err != nil || result.Outcome.Status != "checks_requested" || docker.streams != 1 {
		t.Fatal("optional transport became a native run gate", err, result)
	}
}
