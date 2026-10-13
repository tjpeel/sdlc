package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func frozenTestIdentity() *PublicationIdentity {
	return &PublicationIdentity{RepositoryID: 99, RepositoryName: "example/project", ProfileID: "example", GitHubID: 123, GitHubLogin: "example", GitName: "Example User", GitEmail: "example@example.invalid", SSHPublicKey: "ssh-ed25519 AAAA", SSHFingerprint: "SHA256:example"}
}
func TestDockerPublisherRejectsLegacyIdentityWithoutHostCommands(t *testing.T) {
	publisher := DockerPublisher{}
	if _, err := publisher.invoke(context.Background(), t.TempDir(), PublisherRequest{Action: "publish"}); err == nil {
		t.Fatal("legacy run published")
	}
}

func TestDockerPublisherChecksPairBeforeRuntimeOrCredentialAccess(t *testing.T) {
	calls := 0
	publisher := DockerPublisher{ValidatePair: func() error { calls++; return errors.New("paired identity changed") }}
	_, err := publisher.invoke(context.Background(), t.TempDir(), PublisherRequest{Action: "publish", Plan: Plan{PublicationIdentity: frozenTestIdentity(), SigningProfile: "key"}})
	if err == nil || !strings.Contains(err.Error(), "paired identity changed") || calls != 1 {
		t.Fatal("pair mismatch reached runtime", err)
	}
}

type pairingRaceDocker struct {
	image, volume string
	acquired      bool
	identityCalls int
}

func (docker *pairingRaceDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "context":
		return []byte(`"unix:///fake.sock"`), nil
	case "info":
		return []byte(`{"id":"fake-engine","os":"linux"}`), nil
	case "image":
		return []byte(docker.image), nil
	case "volume":
		if args[1] == "ls" {
			return []byte(docker.volume), nil
		}
		return json.Marshal([]map[string]any{{"Name": docker.volume, "Driver": "local", "Options": map[string]string{}, "Labels": map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "github-auth", "io.sdlc.installation": strings.Repeat("d", 32), "io.sdlc.provider": "github"}}})
	case "ps", "rm":
		return nil, nil
	case "run":
		if args[len(args)-1] == "status" {
			docker.acquired = true
			return []byte(`{"state":"stored"}`), nil
		}
		docker.identityCalls++
	}
	return nil, errors.New("unexpected fake operation")
}
func (*pairingRaceDocker) Run(context.Context, ...string) error {
	return errors.New("unexpected fake runtime mutation")
}
func (*pairingRaceDocker) Interactive(context.Context, ...string) error {
	return errors.New("unexpected fake login")
}

type forbiddenPairPublisher struct{ calls int }

func (runner *forbiddenPairPublisher) Run(context.Context, io.Reader, io.Writer, ...string) error {
	runner.calls++
	return errors.New("unexpected publisher operation")
}

func TestDockerPublisherRechecksPairChangedDuringCredentialAcquisition(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	docker := &pairingRaceDocker{image: "sha256:" + strings.Repeat("a", 64), volume: "sdlc-github-auth-" + strings.Repeat("d", 32) + "-github"}
	runtime := runtimeimage.Manager{Directory: directory, Docker: docker}
	state, err := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: docker.image})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "runtime.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "github-installation.json"), []byte(`{"id":"`+strings.Repeat("d", 32)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth := githubauth.New(runtime)
	auth.Docker = docker
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	validations, keyCalls := 0, 0
	runner := &forbiddenPairPublisher{}
	publisher := DockerPublisher{Runtime: runtime, ImageID: docker.image, Auth: auth, Runner: runner, SigningKey: func(context.Context) ([]byte, error) {
		keyCalls++
		return nil, errors.New("unexpected secret retrieval")
	}, ValidatePair: func() error {
		validations++
		if docker.acquired {
			return errors.New("pair changed during acquisition")
		}
		return nil
	}}
	_, err = publisher.invoke(context.Background(), directory, PublisherRequest{Action: "publish", Plan: Plan{PublicationIdentity: frozenTestIdentity(), SigningProfile: "key"}})
	if err == nil || !strings.Contains(err.Error(), "pair changed during acquisition") || validations != 2 || keyCalls != 0 || runner.calls != 0 || docker.identityCalls != 0 {
		t.Fatal("publication bypassed changed pairing", err, validations, keyCalls, runner.calls, docker.identityCalls)
	}
}
func TestFrozenPublicationRejectsIdentityBeforeSourceOrRemoteMutation(t *testing.T) {
	for _, data := range []string{`{"id":124,"login":"example"}`, `{"id":123,"login":"other"}`, `invalid`} {
		calls := 0
		publisher := GitHubPublisher{Frozen: frozenTestIdentity(), ExpectedHead: testHead, ExpectedTree: testTree, SigningKeyPath: "/tmp/key", AllowedSignersPath: "/tmp/signers", Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if name != "gh" || len(args) != 2 || args[0] != "api" || args[1] != "user" {
				t.Fatal("unexpected boundary")
			}
			return []byte(data), nil
		}}
		if _, err := publisher.Publish(context.Background(), Plan{Repository: "example/project", BaseSHA: testBase, SourceSHA: testBase}, "", t.TempDir(), Publication{}, nil); err == nil || calls != 1 {
			t.Fatal("account mismatch passed")
		}
	}
}
func TestFrozenSettingsRequireTestedRevisionAndSigning(t *testing.T) {
	calls := 0
	publisher := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unexpected")
	}}
	if _, err := publisher.Publish(context.Background(), Plan{Repository: "example/project", BaseSHA: testBase, SourceSHA: testBase}, "", t.TempDir(), Publication{}, nil); err == nil || calls != 0 {
		t.Fatal("incomplete signing reached account")
	}
}
func TestPublisherOutputBounded(t *testing.T) {
	var output boundedPublisherOutput
	data := make([]byte, 1024*1024+1)
	if n, err := output.Write(data); n != len(data) || err != nil || !output.exceeded || output.Len() != 0 {
		t.Fatal("oversize output retained")
	}
}

func TestPublisherWireRequestRetainsFrozenPublicationBoundary(t *testing.T) {
	for _, action := range []string{"publish", "checks"} {
		t.Run(action, func(t *testing.T) {
			original := PublisherRequest{
				Action: action,
				Plan: Plan{
					Headroom:            headroom.Config{Mode: "optimize", ImageID: "sha256:" + strings.Repeat("a", 64), PolicyVersion: headroom.PolicyVersion},
					PublicationIdentity: frozenTestIdentity(), GitHubProfile: "example", SigningProfile: "example-key",
					Repository: "example/project", SourceSHA: testBase, BaseSHA: testBase,
					Base: "main", Branch: "example-ticket", PRTitle: "Example", PRBody: "Example body",
					Root: "/private/project", Ticket: "/private/ticket.md",
					Inputs:       []Input{{Path: "ticket.md", SHA256: "example"}},
					CheckInputs:  []Input{{Path: ".env", SHA256: "example"}},
					Checks:       [][]string{{"dotnet", "test"}},
					SigningImage: "controller-signing-pin", DaemonImage: "controller-daemon-pin",
				},
				Previous:     Publication{Number: 1, HeadSHA: testSigned, BaseSHA: testBase},
				ExpectedHead: testHead, ExpectedTree: testTree,
			}
			projected := publisherRequestForContainer(original)
			expected := original
			expected.Plan.Headroom = headroom.Config{}
			expected.Plan.Root, expected.Plan.Ticket = "", ""
			expected.Plan.Inputs, expected.Plan.CheckInputs, expected.Plan.Checks = nil, nil, nil
			expected.Plan.SigningImage, expected.Plan.DaemonImage = "", ""
			expected.Plan.SigningProfile = ""
			if !reflect.DeepEqual(projected, expected) {
				t.Fatal("publisher request changed the frozen publication boundary")
			}
			data, err := json.Marshal(projected)
			if err != nil {
				t.Fatal(err)
			}
			for _, excluded := range []string{"headroom", "signing_image", "signing_profile", "daemon_image", "/private/project", "/private/ticket.md", ".env"} {
				if strings.Contains(string(data), excluded) {
					t.Fatalf("controller-only field reached publisher: %s", excluded)
				}
			}
			if !original.Plan.Headroom.Enabled() || original.Plan.SigningProfile != "example-key" || original.Plan.SigningImage != "controller-signing-pin" || original.Plan.DaemonImage != "controller-daemon-pin" || original.Plan.Root != "/private/project" || len(original.Plan.CheckInputs) != 1 {
				t.Fatal("projection mutated the retained controller plan")
			}
		})
	}
}

func TestPublisherWireRequestOmitsEveryHeadroomMode(t *testing.T) {
	for _, mode := range []string{"", "off", "passthrough", "optimize"} {
		t.Run(mode, func(t *testing.T) {
			config := headroom.Config{Mode: mode}
			if config.Enabled() {
				config.ImageID = "sha256:" + strings.Repeat("a", 64)
				config.PolicyVersion = headroom.PolicyVersion
			}
			original := PublisherRequest{Plan: Plan{Headroom: config, PublicationIdentity: frozenTestIdentity()}}
			data, err := json.Marshal(publisherRequestForContainer(original))
			if err != nil || strings.Contains(string(data), "headroom") || original.Plan.Headroom != config {
				t.Fatal("Headroom reached the frozen publisher or changed the controller plan", err)
			}
		})
	}
}

func TestPublisherMountBoundary(t *testing.T) {
	args := publisherArguments("sha256:pinned", "publisher", "sdlc-github-auth-install-github", "/private/run/request.json", "/private/run/publisher")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--read-only", "--cap-drop ALL", "--log-driver none", "--user 1000:1000", "--tmpfs /tmp:", "type=bind,src=/private/run/request.json,dst=/request.json,readonly", "type=bind,src=/private/run/publisher,dst=/publisher", "type=volume,src=sdlc-github-auth-install-github,dst=/github-auth,readonly,volume-nocopy", "io.sdlc.installation=install", "io.sdlc.provider=github"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing boundary %s", required)
		}
	}
	for _, forbidden := range []string{"docker.sock", "/workspace", "/project", ".ssh", "/home/", "--privileged", "--env GH_TOKEN=real"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe mount/env %s", forbidden)
		}
	}
	mounts := 0
	for _, arg := range args {
		if arg == "--mount" {
			mounts++
		}
	}
	if mounts != 3 {
		t.Fatal("unexpected publisher mounts")
	}
}

func TestFrozenPublicationNeverReadsHostSettingsAndRejectsUntestedBundle(t *testing.T) {
	plan := Plan{Root: "/host/project", SourceSHA: testBase, Repository: "example/project", BaseSHA: testBase}
	directory, _ := testRun(t)
	fake := publicationFake{t: t, plan: plan, directory: directory}
	publisher := GitHubPublisher{Frozen: frozenTestIdentity(), ExpectedHead: testHead, ExpectedTree: testSigned, SigningKeyPath: "/tmp/key", AllowedSignersPath: "/tmp/signers", Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) == 2 && args[0] == "api" && args[1] == "user" {
			return []byte(`{"id":123,"login":"example"}`), nil
		}
		if name == "gh" && len(args) == 2 && args[0] == "api" && args[1] == "repos/example/project" {
			return []byte(`{"id":99,"full_name":"example/project"}`), nil
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "/host/project") || strings.Contains(joined, "config --get") {
			t.Fatal("frozen publisher loaded host settings")
		}
		if name == "git" && strings.Contains(joined, "bundle verify") {
			return nil, nil
		}
		return fake.command(ctx, name, args...)
	}}
	if _, err := publisher.Publish(context.Background(), plan, "", fake.directory, Publication{}, nil); err == nil || !strings.Contains(err.Error(), "differs from tested revision") {
		t.Fatalf("untested bundle accepted: %v", err)
	}
	if fake.pushes != 0 || fake.signing != 0 {
		t.Fatal("untested tree reached signing or push")
	}
}

func TestFrozenRepositoryAndBaseChangeStopBeforeSourceMutation(t *testing.T) {
	for _, changed := range []string{"repository", "base"} {
		t.Run(changed, func(t *testing.T) {
			publisher := GitHubPublisher{Frozen: frozenTestIdentity(), ExpectedHead: testHead, ExpectedTree: testTree, SigningKeyPath: "/tmp/key", AllowedSignersPath: "/tmp/signers", Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "gh" {
					t.Fatal("changed remote reached source commands")
				}
				switch args[1] {
				case "user":
					return []byte(`{"id":123,"login":"example"}`), nil
				case "repos/example/project":
					id := "99"
					if changed == "repository" {
						id = "100"
					}
					return []byte(`{"id":` + id + `,"full_name":"example/project"}`), nil
				default:
					return []byte(`{"commit":{"sha":"` + testTree + `"}}`), nil
				}
			}}
			if _, err := publisher.Publish(context.Background(), Plan{Repository: "example/project", BaseSHA: testBase, SourceSHA: testBase, Base: "main"}, "", t.TempDir(), Publication{}, nil); err == nil {
				t.Fatal("changed remote accepted")
			}
		})
	}
}

func TestPublisherStagingRejectsLinksAndOversizeBundles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source.bundle")
	if err := os.WriteFile(path, []byte("disposable bundle bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stagePublisherBundle(path, filepath.Join(directory, "copy")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := stagePublisherBundle(link, filepath.Join(directory, "linked-copy")); err == nil {
		t.Fatal("linked bundle accepted")
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(2*1024*1024*1024 + 1)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := stagePublisherBundle(path, filepath.Join(directory, "oversize-copy")); err == nil {
		t.Fatal("oversize bundle accepted")
	}
}

func TestPublisherParentRequiresPrivateTraversalBoundary(t *testing.T) {
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	if err := privatePublisherParent(directory); err != nil {
		t.Fatal(err)
	}
	os.Chmod(directory, 0755)
	if err := privatePublisherParent(directory); err == nil {
		t.Fatal("public parent accepted")
	}
}

func TestPublisherOutputBoundCannotBypassWriteThroughReaderFrom(t *testing.T) {
	var output boundedPublisherOutput
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 1024*1024+1))); err != nil {
		t.Fatal(err)
	}
	if !output.exceeded || output.Len() > 1024*1024 {
		t.Fatal("publisher IPC output cap bypassed")
	}
}

type frozenCIControllerDocker struct {
	pairingRaceDocker
	cleanupFailure bool
}

func (docker *frozenCIControllerDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "run" && args[len(args)-1] == "identity" {
		return []byte(`{"id":123,"login":"example"}`), nil
	}
	if args[0] == "ps" && docker.cleanupFailure && strings.Contains(strings.Join(args, " "), "sdlc-ci-") {
		return nil, errors.New("private cleanup error")
	}
	return docker.pairingRaceDocker.Output(ctx, args...)
}

type frozenCIControllerRunner struct {
	docker              *frozenCIControllerDocker
	failCleanupOnChecks bool
	t                   *testing.T
	fixture             *ciAnnotationFixture
	image, volume       string
	calls               int
}

func (runner *frozenCIControllerRunner) Run(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
	runner.calls++
	joined := strings.Join(args, " ")
	for _, required := range []string{"--read-only", "--entrypoint /usr/local/bin/gh", "dst=/github-auth,readonly,volume-nocopy", "GH_CONFIG_DIR=/github-auth", "--log-driver none"} {
		if !strings.Contains(joined, required) {
			runner.t.Fatal("missing frozen collector boundary", required)
		}
	}
	for _, forbidden := range []string{"sdlc-publisher", "/workspace", "docker.sock", "--privileged", "/source.bundle", "signing-key", "/request.json"} {
		if strings.Contains(joined, forbidden) {
			runner.t.Fatal("unsafe frozen collector mount or binary", forbidden)
		}
	}
	if input != nil {
		runner.t.Fatal("CI inspection received signing input")
	}
	position := -1
	for i, arg := range args {
		if arg == runner.image {
			position = i
			break
		}
	}
	if position < 0 || !strings.Contains(joined, runner.volume) {
		runner.t.Fatal("collector lost frozen image or selected credential volume")
	}
	data, err := runner.fixture.command(ctx, "gh", args[position+1:]...)
	if runner.failCleanupOnChecks && len(args) > position+2 && args[position+1] == "pr" && args[position+2] == "checks" {
		runner.docker.cleanupFailure = true
	}
	if _, writeErr := output.Write(data); writeErr != nil {
		return writeErr
	}
	return err
}
func TestDockerPublisherCurrentCollectorWorksWithFrozenLegacyImage(t *testing.T) {
	for _, failure := range []string{"", "cleanup", "cleanup checks", "API"} {
		t.Run(failure, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			docker := &frozenCIControllerDocker{pairingRaceDocker: pairingRaceDocker{image: "sha256:" + strings.Repeat("a", 64), volume: "sdlc-github-auth-" + strings.Repeat("d", 32) + "-github"}, cleanupFailure: failure == "cleanup"}
			runtime := runtimeimage.Manager{Directory: directory, Docker: docker}
			state, _ := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: docker.image})
			if err := os.WriteFile(filepath.Join(directory, "runtime.json"), state, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "github-installation.json"), []byte(`{"id":"`+strings.Repeat("d", 32)+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_CONTEXT", "")
			fixture := newCIAnnotationFixture()
			fixture.responses["user"] = map[string]any{"id": 123, "login": "example"}
			fixture.responses["repos/example/project"] = map[string]any{"id": 99, "full_name": "example/project"}
			fixture.responses["repos/example/project/branches/main"] = map[string]any{"commit": map[string]any{"sha": testBase}}
			if failure == "API" {
				fixture.fail = "repos/example/project/commits/" + testHead + "/check-runs?per_page=100&page=1"
			}
			runner := &frozenCIControllerRunner{t: t, fixture: fixture, image: docker.image, volume: docker.volume, docker: docker, failCleanupOnChecks: failure == "cleanup checks"}
			identity := frozenTestIdentity()
			identity.GitHubVolume = docker.volume
			auth := githubauth.New(runtime)
			auth.Docker = docker
			publisher := DockerPublisher{Runtime: runtime, ImageID: docker.image, Auth: auth, Runner: runner, SigningKey: func(context.Context) ([]byte, error) { t.Fatal("CI requested a signing key"); return nil, nil }}
			result, err := publisher.Checks(context.Background(), Plan{Repository: "example/project", Base: "main", BaseSHA: testBase, PublicationIdentity: identity}, annotationPublication())
			if failure != "" {
				if err == nil || len(result.Diagnostics) > 0 {
					t.Fatal("frozen collector failure became semantic repair")
				}
				return
			}
			if err != nil || result.Status != "failed" || len(result.Diagnostics) != 2 || runner.calls < 8 || publisher.ImageID != docker.image || result.Diagnostics[0].Kind != "cognitive_complexity" {
				t.Fatalf("legacy runtime did not execute current controller collector: %+v %v calls%d", result, err, runner.calls)
			}
		})
	}
}
