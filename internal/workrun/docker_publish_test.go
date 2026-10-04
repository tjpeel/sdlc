package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
					PublicationIdentity: frozenTestIdentity(), GitHubProfile: "example",
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
			expected.Plan.Root, expected.Plan.Ticket = "", ""
			expected.Plan.Inputs, expected.Plan.CheckInputs, expected.Plan.Checks = nil, nil, nil
			expected.Plan.SigningImage, expected.Plan.DaemonImage = "", ""
			if !reflect.DeepEqual(projected, expected) {
				t.Fatal("publisher request changed the frozen publication boundary")
			}
			data, err := json.Marshal(projected)
			if err != nil {
				t.Fatal(err)
			}
			for _, excluded := range []string{"signing_image", "daemon_image", "/private/project", "/private/ticket.md", ".env"} {
				if strings.Contains(string(data), excluded) {
					t.Fatalf("controller-only field reached publisher: %s", excluded)
				}
			}
			if original.Plan.SigningImage != "controller-signing-pin" || original.Plan.DaemonImage != "controller-daemon-pin" || original.Plan.Root != "/private/project" || len(original.Plan.CheckInputs) != 1 {
				t.Fatal("projection mutated the retained controller plan")
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
