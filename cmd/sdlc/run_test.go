package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func runArgs(extra ...string) []string {
	return append([]string{"--reference", "TASK-1", "--ticket", "01-selected.md"}, extra...)
}

func TestRunCaptureFailureIsVisibleBeforeExecutionJournal(t *testing.T) {
	for _, reportingFails := range []bool{false, true} {
		t.Run(fmt.Sprint(reportingFails), func(t *testing.T) {
			root := runGitFixture(t)
			marker := forbidConnectedRunCommands(t, root)
			state, profile := pairingFixture(t)
			t.Setenv("SDLC_STATE_DIR", state)
			t.Setenv("CI", "")
			pair := githubprofile.Pair{Version: 1, GitHubProfile: "personal", AccountID: 123, Login: "example-user", SigningProfile: "personal-key", SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
			if err := githubprofile.Store(state, pair, false); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("README.md", filepath.Join(root, "unsafe-source")); err != nil {
				t.Fatal(err)
			}
			options, err := parseRunOptions(runArgs("--repo", "example/project", "--github-profile", "personal"))
			if err != nil {
				t.Fatal(err)
			}
			options.featureOwned = true
			if reportingFails {
				if err := os.WriteFile(filepath.Join(state, "runs"), []byte("public registry obstruction"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			err = runSelectedCommand(context.Background(), options, &output)
			if err == nil || !strings.Contains(err.Error(), "regular files without symlinks") {
				t.Fatalf("capture failure not reproduced: %v", err)
			}
			if reportingFails {
				if !strings.Contains(err.Error(), "cannot report preparation failure") {
					t.Fatalf("reporting error replaced original failure: %v", err)
				}
				return
			}
			views, listErr := runstatus.New(state).List(time.Now())
			if listErr != nil || len(views) != 1 {
				t.Fatalf("preparation failure absent from dashboard: %+v %v", views, listErr)
			}
			v := views[0]
			if v.Journal != nil || v.State != "blocked" || !v.NeedsAttention || !v.FeatureOwned || v.StopReason != err.Error() {
				t.Fatalf("original capture failure lost: %+v", v)
			}
			if _, err := os.Lstat(filepath.Join(v.Directory, "journal.json")); !os.IsNotExist(err) {
				t.Fatal("failed capture created an execution journal")
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatal("preparation failure reached external account/provider commands")
			}
		})
	}
}

func TestRuntimeRunLeaseBlocksBuildUntilControllerExits(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private-state")
	lease, err := leaseRuntimeRun(context.Background(), directory, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := filepath.Join(directory, "runtime-build.lock")
	if build, err := filelock.Acquire(path); err == nil {
		build.Close()
		t.Fatal("runtime build overlapped the controller lease")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	other, err := leaseRuntimeRun(ctx, directory, io.Discard)
	if err != nil {
		t.Fatal("another controller could not share the runtime", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	build, err := filelock.Acquire(path)
	if err != nil {
		t.Fatal("completed controller retained the runtime lease", err)
	}
	build.Close()
}

func TestRejectedRunSelectionsDoNotCreateRuntimeLeaseState(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	for _, args := range [][]string{
		runArgs("--repo", "invalid"),
		runArgs("--effort", "unsupported"),
		runArgs("--repo", "example/project", "--input", "missing.md"),
		runArgs("--ticket", "missing.md"),
		runArgs("--resume", "not-a-run-id"),
	} {
		if err := runCommand(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("invalid selection accepted: %v", args)
		}
		if _, err := os.Stat(os.Getenv("SDLC_STATE_DIR")); !os.IsNotExist(err) {
			t.Fatalf("invalid selection created runtime state: %v", args)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid selection reached a connected command")
	}
}

func TestRunFreezesSelectedSidecarImages(t *testing.T) {
	pins := runtimepins.Pins{SigningImage: "1password/op:2.40.1@sha256:" + strings.Repeat("a", 64), DaemonImage: "docker:29.9.0-dind@sha256:" + strings.Repeat("b", 64)}
	var plan workrun.Plan
	if err := freezeSidecarImages(&plan, runtimeimage.State{DependencyPins: &pins}); err != nil {
		t.Fatal(err)
	}
	wantSigning, wantDaemon := pins.SigningImage, pins.DaemonImage
	pins.SigningImage = runtimepins.DefaultSigningImage
	pins.DaemonImage = "docker:29.9.1-dind@sha256:" + strings.Repeat("c", 64)
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var resumed workrun.Plan
	if err := json.Unmarshal(data, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.SigningImage != wantSigning || resumed.DaemonImage != wantDaemon {
		t.Fatalf("recorded sidecars changed with the installation: %+v", resumed)
	}
}

type runDaemonMetadata struct {
	fail     bool
	requests []string
}

func (metadata *runDaemonMetadata) Do(request *http.Request) (*http.Response, error) {
	metadata.requests = append(metadata.requests, request.URL.Host+request.URL.Path)
	if metadata.fail {
		return nil, errors.New("disposable metadata failure")
	}
	switch request.URL.Host + request.URL.Path {
	case "auth.docker.io/token":
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"token":"fake-anonymous-registry-response"}`))}, nil
	case "registry-1.docker.io/v2/library/docker/manifests/29.8.2-dind":
		body := []byte(`{"schemaVersion":2}`)
		sum := sha256.Sum256(body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Docker-Content-Digest": []string{"sha256:" + hex.EncodeToString(sum[:])}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	default:
		return nil, errors.New("unexpected public metadata request")
	}
}

type runDaemonDocker struct {
	fail  bool
	calls [][]string
}

func (docker *runDaemonDocker) Run(_ context.Context, args ...string) error {
	docker.calls = append(docker.calls, append([]string(nil), args...))
	if docker.fail {
		return errors.New("disposable pull failure")
	}
	return nil
}

func (*runDaemonDocker) Output(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("unexpected Docker metadata operation")
}

func TestNewDockerTestRunFreezesOrdinaryRuntimeDaemonDigest(t *testing.T) {
	metadata, docker := &runDaemonMetadata{}, &runDaemonDocker{}
	checker := runtimeupdates.Checker{Client: metadata}
	plan := workrun.Plan{DockerTests: true}
	if err := freezeRuntimeImages(context.Background(), &plan, runtimeimage.State{}, runtimeimage.Manager{Docker: docker}, checker.ResolveDefaultDaemonImage); err != nil {
		t.Fatal(err)
	}
	if err := runtimepins.ValidateDaemonImage(plan.DaemonImage); err != nil || plan.SigningImage != runtimepins.DefaultSigningImage {
		t.Fatalf("ordinary runtime did not freeze exact sidecars: %+v, %v", plan, err)
	}
	if !strings.HasPrefix(plan.DaemonImage, runtimepins.DefaultDaemonImage+"@sha256:") || !reflect.DeepEqual(docker.calls, [][]string{{"pull", plan.DaemonImage}}) || len(metadata.requests) != 2 {
		t.Fatalf("daemon was not resolved and pulled before admission: plan=%+v, pulls=%v, requests=%v", plan, docker.calls, metadata.requests)
	}
}

func TestNewDockerTestRunStopsBeforeFreezingUnavailableDaemon(t *testing.T) {
	for _, cause := range []string{"metadata", "pull", "unapproved image"} {
		t.Run(cause, func(t *testing.T) {
			metadata, docker := &runDaemonMetadata{fail: cause == "metadata"}, &runDaemonDocker{fail: cause == "pull"}
			checker := runtimeupdates.Checker{Client: metadata}
			lookup := checker.ResolveDefaultDaemonImage
			if cause == "unapproved image" {
				lookup = func(context.Context) (string, error) { return runtimepins.DefaultDaemonImage, nil }
			}
			plan := workrun.Plan{DockerTests: true}
			before := plan
			if err := freezeRuntimeImages(context.Background(), &plan, runtimeimage.State{}, runtimeimage.Manager{Docker: docker}, lookup); err == nil {
				t.Fatal("unavailable or mutable daemon was admitted")
			}
			if !reflect.DeepEqual(plan, before) || (cause != "pull" && len(docker.calls) != 0) {
				t.Fatalf("failed preflight froze a plan or reached pull: %+v, %v", plan, docker.calls)
			}
		})
	}
}

func TestNewRunWithoutDockerTestsNeedsNoDaemonMetadataOrPull(t *testing.T) {
	metadata, docker := &runDaemonMetadata{fail: true}, &runDaemonDocker{fail: true}
	var plan workrun.Plan
	if err := freezeRuntimeImages(context.Background(), &plan, runtimeimage.State{}, runtimeimage.Manager{Docker: docker}, (runtimeupdates.Checker{Client: metadata}).ResolveDefaultDaemonImage); err != nil || len(metadata.requests) != 0 || len(docker.calls) != 0 {
		t.Fatalf("ordinary checks required a privileged sidecar: %+v, %v", plan, err)
	}
}

func TestRunFreezesLegacyDefaultsAndRejectsInvalidSelectedImages(t *testing.T) {
	var plan workrun.Plan
	if err := freezeSidecarImages(&plan, runtimeimage.State{}); err != nil || plan.SigningImage != runtimepins.DefaultSigningImage || plan.DaemonImage != "" {
		t.Fatalf("legacy runtime selection: %+v, %v", plan, err)
	}
	before := plan
	for _, pins := range []runtimepins.Pins{
		{SigningImage: "1password/op:latest", DaemonImage: "docker:29.9.0-dind@sha256:" + strings.Repeat("b", 64)},
		{SigningImage: runtimepins.DefaultSigningImage, DaemonImage: runtimepins.DefaultDaemonImage},
	} {
		if err := freezeSidecarImages(&plan, runtimeimage.State{DependencyPins: &pins}); err == nil {
			t.Fatal("invalid sidecar selection was frozen")
		}
		if !reflect.DeepEqual(plan, before) {
			t.Fatal("invalid selection changed the frozen plan")
		}
	}
}
func TestRunOptionsDefaultsRepeatableInputsAndDockerOptIn(t *testing.T) {
	options, err := parseRunOptions(runArgs("--input", "spec.md", "--input", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	if options.provider != "codex" || options.base != "main" || options.timeout != 2*time.Hour || options.dockerTests || !reflect.DeepEqual(options.inputs, selectedInputs{"spec.md", "design.md"}) {
		t.Fatalf("unexpected defaults: %+v", options)
	}
	options, err = parseRunOptions(runArgs("--provider", "claude", "--docker-tests", "--timeout", "1m"))
	if err != nil || !options.dockerTests || options.provider != "claude" || options.timeout != time.Minute {
		t.Fatalf("explicit options: %+v %v", options, err)
	}
}
func TestRunOptionsRejectInvalidAndResumeOverrides(t *testing.T) {
	cases := [][]string{nil, {"--reference", "TASK-1"}, runArgs("extra"), runArgs("--provider", "other"), runArgs("--input", ""), runArgs("--answer-file", "answer.txt"), runArgs("--timeout", "59s"), runArgs("--timeout", "25h"), runArgs("--timeout", "invalid")}
	for _, name := range []string{"provider", "model", "effort", "review-model", "review-effort", "base", "branch", "repo", "input"} {
		cases = append(cases, runArgs("--resume", "recorded", "--"+name, "override"))
	}
	cases = append(cases, runArgs("--resume", "recorded", "--docker-tests"))
	for _, args := range cases {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := parseRunOptions(runArgs("--resume", "recorded", "--answer-file", "answer.txt", "--timeout", "24h", "--dry-run")); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRunOptions([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help=%v", err)
	}
}
func TestRunHelpCreatesNoState(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	state := filepath.Join(directory, "state")
	t.Setenv("SDLC_STATE_DIR", state)
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"--help"}, &output); err != nil || !strings.Contains(output.String(), runUsage) {
		t.Fatalf("help=%s error=%v", output.String(), err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("help created installation state")
	}
}
func runGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Example User", "-c", "user.email=example@example.invalid"}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v: %s", err, output)
		}
	}
	git("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/.sdlc/work/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{"README.md": "Example repository\n", "spec.md": "Selected requirement\n", ".sdlc/project.json": `{"version":1,"checks":[["go","test","./..."]],"input_files":["README.md"]}`, ".sdlc/work/TASK-1/tickets/01-selected.md": "# Selected ticket\n", ".sdlc/work/TASK-1/tickets/02-other.md": "# Other ticket\n"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "README.md", "spec.md", ".sdlc/project.json")
	git("commit", "-m", "Create disposable fixture")
	t.Chdir(root)
	stateParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDLC_STATE_DIR", filepath.Join(stateParent, "private-state"))
	return root
}
func forbidConnectedRunCommands(t *testing.T, root string) string {
	t.Helper()
	directory := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "called")
	for _, name := range []string{"docker", "gh", "codex", "claude"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nprintf called > \"$(dirname \"$0\")/called\"\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}
func TestRunDryRunIsOfflineAndPreservesProviderRoles(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := runGitFixture(t)
			marker := forbidConnectedRunCommands(t, root)
			t.Setenv("CI", "true")
			var output bytes.Buffer
			if err := runCommand(context.Background(), runArgs("--provider", provider, "--repo", "example/project", "--branch", "work/TASK-1", "--input", "spec.md", "--input", "spec.md", "--input", "README.md", "--dry-run"), &output); err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(output.String(), "\n", 2)
			if len(parts) != 2 || !strings.Contains(parts[0], "Offline plan") {
				t.Fatalf("missing offline notice: %s", output.String())
			}
			var result struct {
				State string       `json:"state"`
				Plan  workrun.Plan `json:"plan"`
			}
			if err := json.Unmarshal([]byte(parts[1]), &result); err != nil {
				t.Fatal(err)
			}
			want := workrun.DefaultModels().Codex
			if provider == "claude" {
				want = workrun.DefaultModels().Claude
			}
			if result.State != "prepared" || result.Plan.Roles != want || result.Plan.Ticket != ".sdlc/work/TASK-1/tickets/01-selected.md" || result.Plan.Branch != "work/TASK-1" || result.Plan.DockerTests {
				t.Fatalf("selection=%+v", result)
			}
			wantInputs := []workrun.Input{{Path: result.Plan.Ticket}, {Path: "spec.md"}, {Path: "README.md"}}
			wantChecks := []workrun.Input{{Path: "README.md"}}
			if !reflect.DeepEqual(result.Plan.Inputs, wantInputs) || !reflect.DeepEqual(result.Plan.CheckInputs, wantChecks) {
				t.Fatalf("selected provider/check inputs omitted or misclassified: %+v", result.Plan)
			}
			for _, body := range []string{"Selected requirement", "Example repository", "Selected ticket", "Other ticket"} {
				if strings.Contains(output.String(), body) {
					t.Fatalf("offline plan disclosed input body %q", body)
				}
			}
			for _, path := range []string{marker, os.Getenv("SDLC_STATE_DIR")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("offline plan touched %s", path)
				}
			}
		})
	}
}
func TestRunDryRunRejectsMissingOrUnsafeExplicitInputs(t *testing.T) {
	root := runGitFixture(t)
	marker := forbidConnectedRunCommands(t, root)
	if err := os.Symlink(filepath.Join(root, "spec.md"), filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"missing.md", "../spec.md", filepath.Join(root, "spec.md"), "linked.md", ".git/config"} {
		var output bytes.Buffer
		if err := runCommand(context.Background(), runArgs("--repo", "example/project", "--input", path, "--dry-run"), &output); err == nil {
			t.Fatalf("accepted explicit input %q", path)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid input contacted connected command")
	}
}
