package workrun

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// Model the Docker tmpfs lifetime at the external boundary. Git and checkout
// validation run locally against disposable real repositories, without login.
type companionTestDocker struct {
	pairingRaceDocker
	source, name string
	live         bool
	operations   []string
}

func (docker *companionTestDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "run":
		if args[len(args)-1] == "identity" {
			return []byte(`{"id":123,"login":"example"}`), nil
		}
		for i, arg := range args {
			if arg == "--name" && strings.HasPrefix(args[i+1], "sdlc-companion-") {
				docker.name = args[i+1]
				docker.operations = append(docker.operations, "start")
				for _, option := range args {
					if option == "--detach" {
						docker.live = true
					}
				}
				return []byte("fake-container"), nil
			}
		}
	case "exec":
		if !docker.live || args[1] != docker.name {
			return nil, errors.New("source reader is stopped")
		}
		if args[2] == "/bin/tar" {
			docker.operations = append(docker.operations, "copy")
			return docker.exportSource(ctx)
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "--depth") || arg == "--single-branch" {
				return nil, errors.New("requested historical source would be absent")
			}
		}
		docker.operations = append(docker.operations, "clone")
		return nil, nil
	case "cp":
		return nil, errors.New("Docker cannot copy tmpfs through cp")
	case "ps":
		for _, arg := range args {
			if docker.name != "" && arg == "name=^/"+docker.name+"$" {
				return []byte(docker.name), nil
			}
		}
	case "rm":
		if docker.name != "" && args[len(args)-1] == docker.name {
			docker.operations = append(docker.operations, "remove")
			docker.live = false
			docker.name = ""
			return nil, nil
		}
	}
	return docker.pairingRaceDocker.Output(ctx, args...)
}

func (docker *companionTestDocker) exportSource(ctx context.Context) ([]byte, error) {
	directory, err := os.MkdirTemp("", "companion-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	checkout := filepath.Join(directory, "checkout")
	if _, err := isolatedGit(ctx, docker.source, "clone", "--no-hardlinks", docker.source, checkout); err != nil {
		return nil, err
	}
	if _, err := isolatedGit(ctx, checkout, "remote", "set-url", "origin", "https://github.com/example/companion.git"); err != nil {
		return nil, err
	}
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	archive := tar.NewWriter(compressed)
	err = filepath.Walk(checkout, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(checkout, file)
		header.Name = "./" + filepath.ToSlash(relative)
		if err := archive.WriteHeader(header); err != nil || info.IsDir() {
			return err
		}
		input, err := os.Open(file)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archive, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

func companionPublisherFixture(t *testing.T) (DockerPublisher, Plan, string, *companionTestDocker) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, _ := sourceFixture(t)
	docker := &companionTestDocker{pairingRaceDocker: pairingRaceDocker{
		image: "sha256:" + strings.Repeat("a", 64), volume: "sdlc-github-auth-" + strings.Repeat("d", 32) + "-github",
	}, source: source}
	runtime := runtimeimage.Manager{Directory: directory, Docker: docker}
	state, _ := json.Marshal(runtimeimage.State{Version: 1, Engine: "fake-engine", ImageID: docker.image})
	for name, data := range map[string][]byte{
		"runtime.json":             state,
		"github-installation.json": []byte(`{"id":"` + strings.Repeat("d", 32) + `"}`),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	auth := githubauth.New(runtime)
	auth.Docker = docker
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	identity := frozenTestIdentity()
	identity.GitHubVolume = docker.volume
	plan := Plan{Repository: "example/project", PublicationIdentity: identity}
	workspace, _ := sourceFixture(t)
	restoreCompanionTestPermissions(t, workspace)
	return DockerPublisher{Runtime: runtime, ImageID: docker.image, Auth: auth}, plan, workspace, docker
}

func restoreCompanionTestPermissions(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				return os.Chmod(path, 0700)
			}
			return err
		})
	})
}

func TestCompanionCloneRetainsTemporarySourceUntilCopy(t *testing.T) {
	publisher, plan, workspace, docker := companionPublisherFixture(t)
	original, err := isolatedGit(context.Background(), docker.source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, docker.source, "README.md", "new contract\n")
	if _, err := SafeGit(context.Background(), docker.source, "commit", "-am", "Change contract"); err != nil {
		t.Fatal(err)
	}
	revision, err := publisher.CloneCompanionRepository(context.Background(), plan, workspace, "example/companion")
	if err != nil {
		t.Fatal(err)
	}
	if !objectID.MatchString(revision) || strings.Join(docker.operations, ",") != "start,clone,copy,remove" || docker.live {
		t.Fatalf("source staging did not complete and clean up: %s, %v", revision, docker.operations)
	}
	for _, relative := range []string{".sdlc", ".sdlc/repositories", ".sdlc/repositories/example", ".sdlc/repositories/example/companion"} {
		info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(relative)))
		if err != nil || info.Mode().Perm()&0001 == 0 {
			t.Fatalf("worker cannot traverse %s: %v", relative, err)
		}
	}
	checkout := filepath.Join(workspace, ".sdlc", "repositories", "example", "companion")
	if contents, err := isolatedGit(context.Background(), checkout, "show", strings.TrimSpace(original)+":README.md"); err != nil || contents != "original\n" {
		t.Fatalf("historical contract was unavailable: %q, %v", contents, err)
	}
}

func TestCompanionCloneAdoptsCompletedSourceWithoutRecloning(t *testing.T) {
	publisher, plan, workspace, docker := companionPublisherFixture(t)
	first, err := publisher.CloneCompanionRepository(context.Background(), plan, workspace, "example/companion")
	if err != nil {
		t.Fatal(err)
	}
	// No journal exists: simulate stopping after retaining the checkout but
	// before the controller recorded its revision.
	second, err := publisher.CloneCompanionRepository(context.Background(), plan, workspace, "example/companion")
	if err != nil || first != second || len(docker.operations) != 4 {
		t.Fatalf("completed staging was not recovered: %s, %s, %v, %v", first, second, docker.operations, err)
	}
}

func TestCompanionRecoveryRejectsDirtySourceAndUnsafeMetadata(t *testing.T) {
	for _, mutation := range []string{"dirty", "origin", "filter", "symlink", "shallow"} {
		t.Run(mutation, func(t *testing.T) {
			publisher, plan, workspace, docker := companionPublisherFixture(t)
			if _, err := publisher.CloneCompanionRepository(context.Background(), plan, workspace, "example/companion"); err != nil {
				t.Fatal(err)
			}
			checkout := filepath.Join(workspace, ".sdlc", "repositories", "example", "companion")
			marker := filepath.Join(workspace, "filter-executed")
			_ = filepath.Walk(checkout, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				return os.Chmod(path, 0700)
			})
			switch mutation {
			case "dirty":
				sourceWrite(t, checkout, "README.md", "changed source\n")
			case "origin":
				if _, err := isolatedGit(context.Background(), checkout, "remote", "set-url", "origin", "https://example.invalid/other/source"); err != nil {
					t.Fatal(err)
				}
			case "filter":
				sourceWrite(t, checkout, ".gitattributes", "README.md filter=disposable\n")
				if _, err := isolatedGit(context.Background(), checkout, "config", "filter.disposable.clean", "touch "+marker); err != nil {
					t.Fatal(err)
				}
			case "shallow":
				revision, err := isolatedGit(context.Background(), checkout, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				sourceWrite(t, checkout, ".git/shallow", revision)
			case "symlink":
				if err := os.Chmod(filepath.Dir(checkout), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(checkout, checkout+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(checkout+"-preserved", checkout); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := publisher.CloneCompanionRepository(context.Background(), plan, workspace, "example/companion"); err == nil {
				t.Fatal("unsafe existing source was adopted")
			}
			if len(docker.operations) != 4 {
				t.Fatal("recovery replaced existing material", docker.operations)
			}
			if _, err := os.Lstat(checkout); err != nil {
				t.Fatal("recovery removed existing material", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("Git metadata executed a host command", err)
			}
		})
	}
}

func TestCompanionArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../outside", Typeflag: tar.TypeReg}}},
		{"absolute", []*tar.Header{{Name: "/outside", Typeflag: tar.TypeReg}}},
		{"escaping-link", []*tar.Header{{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "../outside"}}},
		{"hardlink", []*tar.Header{{Name: "alias", Typeflag: tar.TypeLink, Linkname: "source.txt"}}},
		{"special", []*tar.Header{{Name: "pipe", Typeflag: tar.TypeFifo}}},
		{"duplicate", []*tar.Header{{Name: "source.txt", Typeflag: tar.TypeReg}, {Name: "source.txt", Typeflag: tar.TypeReg}}},
		{"oversized", []*tar.Header{{Name: "source.txt", Typeflag: tar.TypeReg, Size: 128*1024*1024 + 1}}},
		{"link-ancestor", []*tar.Header{{Name: "inside", Typeflag: tar.TypeDir}, {Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "inside"}, {Name: "alias/newdir/source.txt", Typeflag: tar.TypeReg}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer
			compressed := gzip.NewWriter(&data)
			archive := tar.NewWriter(compressed)
			for _, header := range test.headers {
				if err := archive.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
			}
			_ = archive.Close() // The oversized fixture deliberately omits its body.
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := extractCompanionArchive(data.Bytes(), root); err == nil {
				t.Fatal("unsafe archive was extracted")
			}
			if _, err := os.Stat(filepath.Join(root, "inside", "newdir")); !os.IsNotExist(err) {
				t.Fatal("archive followed a symlink before checking its parent", err)
			}
		})
	}
}

func TestCompanionArchiveChecksGzipIntegrity(t *testing.T) {
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "source.txt", Typeflag: tar.TypeReg, Size: 3, Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	_, _ = archive.Write([]byte("abc"))
	_ = archive.Close()
	_ = compressed.Close()
	corrupt := append([]byte(nil), data.Bytes()...)
	corrupt[len(corrupt)-8] ^= 1
	if err := extractCompanionArchive(corrupt, t.TempDir()); err == nil {
		t.Fatal("corrupt gzip footer was accepted")
	}
}

func TestCompanionSourceLinksStayInsideCheckout(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "parent", "staged")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, root, "source.txt", "disposable source\n")
	sourceWrite(t, outer, "outside", "disposable outside marker\n")
	if err := os.Symlink("../source.txt", filepath.Join(root, "dir", "valid")); err != nil {
		t.Fatal(err)
	}
	if err := validateCompanionSourceLinks(root); err != nil {
		t.Fatal("rejected an internal parent-relative source link", err)
	}
	if err := os.Symlink("..", filepath.Join(root, "dir", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("alias/../../outside", filepath.Join(root, "dir", "evil")); err != nil {
		t.Fatal(err)
	}
	if err := validateCompanionSourceLinks(root); err == nil {
		t.Fatal("accepted a chained link outside the checkout")
	}
}
